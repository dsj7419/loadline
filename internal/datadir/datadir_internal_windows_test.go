package datadir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// going writes one of Loadline's files and has it deleted soon after, as a
// drain deletes a spooled event between the walk that lists it and the look
// at its access list: Windows refuses to open it meanwhile, with "access
// denied".
func going(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "1-0000000a.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(20*time.Millisecond, fspermtest.DeletePending(t, path))
	return path
}

func TestClearOwnEntries_WaitsOutADeletionAndLeavesOutWhatHasGone(t *testing.T) {
	path := going(t)

	if err := clearOwnEntries(path); err != nil {
		t.Errorf("clearOwnEntries(%s), deleted meanwhile: %v; want nil", path, err)
	}
}

func TestEntryProblem_WaitsOutADeletionAndLeavesOutWhatHasGone(t *testing.T) {
	path := going(t)

	if got, err := entryProblem(path); err != nil || got != "" {
		t.Errorf("entryProblem(%s), deleted meanwhile = %q, %v; want \"\", nil", path, got, err)
	}
}

func TestOwnerProblem_WaitsOutADeletionAndLeavesOutWhatHasGone(t *testing.T) {
	path := going(t)

	if got, err := ownerProblem(filepath.Dir(path), path); err != nil || got != "" {
		t.Errorf("ownerProblem(%s), deleted meanwhile = %q, %v; want \"\", nil", path, got, err)
	}
}

// keepsTheUserOut gives folder, for the rest of the test, an access list that
// keeps the user from listing it.
func keepsTheUserOut(t *testing.T, folder string) {
	t.Helper()
	fspermtest.SetAccessListAsIs(t, folder, "D:AI(A;OICIID;FA;;;SY)")
	t.Cleanup(func() { _ = fsperm.ClearExplicit(folder) })
}

// takeown /r follows a junction out of the folder it is given, and takes
// what the junction leads to, and goes into folders whose contents the user
// cannot see. So the data directory is taken back with what it holds only
// while nothing anywhere in it is a link, the browser profile included, and
// every folder of it can be listed.
func TestMovable_TakesBackOnlyTheDataDirectoryWhenTakeownCouldLeaveIt(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, dir string){
		"a junction among Loadline's files": func(t *testing.T, dir string) {
			t.Helper()
			junctionAt(t, filepath.Join(dir, "loadline.log"), t.TempDir())
		},
		"a folder of Loadline's that keeps the user out": func(t *testing.T, dir string) {
			t.Helper()
			keepsTheUserOut(t, filepath.Join(dir, "spool"))
		},
		"a junction in the browser profile": func(t *testing.T, dir string) {
			t.Helper()
			profile := filepath.Join(dir, browserProfile)
			if err := fsperm.MkdirAll(profile); err != nil {
				t.Fatal(err)
			}
			junctionAt(t, filepath.Join(profile, "Default"), t.TempDir())
		},
		"a folder in the browser profile that keeps the user out": func(t *testing.T, dir string) {
			t.Helper()
			locked := filepath.Join(dir, browserProfile, "Default")
			if err := fsperm.MkdirAll(locked); err != nil {
				t.Fatal(err)
			}
			keepsTheUserOut(t, locked)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := home(t)
			plant(t, dir)
			found := ownedFolder(dir, gone)
			movableAs(t, found)
			profileHolds(t, onDisk(t, dir))
			want := folderReport(found) + offered(onDisk(t, dir), false)

			if err := Verify(dir, filepath.Dir(dir)); err == nil || err.Error() != want {
				t.Errorf("Verify = %v;\nwant %s", err, want)
			}
		})
	}
}

// shortNameOf returns the 8.3 short name of long, an existing folder, joined
// to the folder that holds it, or long itself on a volume that makes no
// short names.
func shortNameOf(t *testing.T, long string) string {
	t.Helper()
	name, err := windows.UTF16PtrFromString(long)
	if err != nil {
		t.Fatal(err)
	}
	var buf [windows.MAX_LONG_PATH]uint16
	n, err := windows.GetShortPathName(name, &buf[0], windows.MAX_LONG_PATH)
	if err != nil || n >= windows.MAX_LONG_PATH {
		t.Fatalf("GetShortPathName(%s): %d, %v", long, n, err)
	}
	return filepath.Join(filepath.Dir(long), filepath.Base(windows.UTF16ToString(buf[:n])))
}

// The profile is asked about the folder where it really is, and the command
// takes back that folder, whatever junction or short name the data
// directory's path holds: the folder the profile was asked about is the one
// the command names.
func TestOwnerProblem_OffersTheFolderWhereItReallyIs(t *testing.T) {
	top := t.TempDir()
	target := filepath.Join(top, "a folder with a long name")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(top, "link")
	junctionAt(t, link, target)
	short := shortNameOf(t, target)
	for _, tt := range []struct{ name, given string }{
		{"through a junction", link},
		{"by a short name", short},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.given == target {
				t.Skipf("the volume of %s makes no 8.3 short names (fsutil 8dot3name query shows it)", top)
			}
			dir := homeAt(t, filepath.Join(tt.given, tt.name))
			token := filepath.Join(dir, "token")
			ownedBy(t, token, gone)
			held := filepath.Join(onDisk(t, target), tt.name)
			profileHolds(t, held)

			err := Make(dir)

			if want := ownFileReport(token, gone) + offered(held, true); err == nil || err.Error() != want {
				t.Errorf("Make = %v;\nwant %s", err, want)
			}
		})
	}
}

// A file another process is writing may start being deleted the moment
// after the walk judged it, before Make puts it right: its writer is done
// with it, and a scanner holds it a while. Windows then refuses every read
// of it, and the clear of its list, with "access denied", for longer than
// Make waits. Secure passes over it, as over any temporary file that goes,
// and takes the data directory, which is not private yet: what its folder,
// and the data directory, pass down passes over it too.
func TestSecure_LeavesATemporaryFileThatStartsBeingDeletedAfterItWasJudged(t *testing.T) {
	for _, name := range []string{".token-1234", filepath.Join("spool", ".put-1234.tmp"), filepath.Join("bin", ".loadline-1234.tmp")} {
		t.Run(filepath.Base(name), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "loadline")
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			started := false
			previous := afterTemporaryJudged
			afterTemporaryJudged = func(judged string) {
				if judged != path || started {
					return
				}
				started = true
				fspermtest.DeletePending(t, judged)
			}
			t.Cleanup(func() { afterTemporaryJudged = previous })

			if err := Secure(dir, filepath.Dir(dir)); err != nil {
				t.Errorf("Secure = %v, want it to pass over %s", err, path)
			}
			if !started {
				t.Fatalf("nothing started deleting %s once it was judged, so the test proves nothing", path)
			}
			if private, err := fsperm.DirPrivate(dir); err != nil || !private {
				t.Errorf("%s is not private after Secure (%v)", dir, err)
			}
		})
	}
}

// lockedSpool returns a data directory (home) whose spool holds path, a
// file another process is writing, and keeps the user out
// (keepsTheUserOut), as one copied in with another account's permissions
// does: the check Make runs first cannot list the spool, and only Make's
// walk, once it has made the spool listable, comes to path. prepare runs on
// path before the spool is locked.
func lockedSpool(t *testing.T, prepare func(path string)) (dir, path string) {
	t.Helper()
	dir = home(t)
	spool := filepath.Join(dir, "spool")
	path = filepath.Join(spool, ".put-1234.tmp")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepare(path)
	keepsTheUserOut(t, spool)
	if err := Verify(dir, filepath.Dir(dir)); err != nil {
		t.Fatalf("Verify = %v, which saw into %s, so the test proves nothing", err, spool)
	}
	return dir, path
}

// There too, a file another process is writing that belongs to someone
// else is refused.
func TestMake_RefusesATemporaryFileOfSomeoneElsesInAFolderThatKeptTheUserOut(t *testing.T) {
	dir, path := lockedSpool(t, func(path string) { ownedByStranger(t, path) })

	if err := Make(dir); err == nil || !strings.HasPrefix(err.Error(), path+" belongs to a stranger") {
		t.Errorf("Make = %v, want it refused, starting %q", err, path+" belongs to a stranger")
	}
}

// There too, what the walk finds under a temporary name may be a link put
// in the file's place meanwhile, or a file with a name elsewhere: it is
// refused, and nothing is done to it, which would be done to the file
// wherever else it is. Here the look finds a link where a file stands that
// lets others read it: the file keeps its list.
func TestMake_ChangesNothingUnderATemporaryNameItRefusesInAFolderThatKeptTheUserOut(t *testing.T) {
	var before fspermtest.List
	dir, path := lockedSpool(t, func(path string) {
		fspermtest.Grant(t, path, fspermtest.Read, windows.NO_INHERITANCE)
		before = fspermtest.AccessList(t, path)
	})
	lookAs(t, path, func() (fsperm.Entry, error) {
		return fsperm.Entry{Link: true, Reparse: fsperm.NameSurrogate, Names: 1}, nil
	})

	if err := Make(dir); err == nil || !strings.HasPrefix(err.Error(), path+" is a link") {
		t.Errorf("Make = %v, want it refused, starting %q", err, path+" is a link")
	}
	if after := fspermtest.AccessList(t, path); !after.Equal(before) {
		t.Errorf("Make changed the access list of %s, which it refused, from %s to %s", path, before, after)
	}
}

// There too, a file another process is writing whose permissions the user
// may not read, or may not change, is refused, in the words install's first
// check and doctor use for it: the walk looks at it again once Windows has
// refused a step on it for as long as Make waits, and finds it there, not
// being deleted.
func TestMake_RefusesATemporaryFileThatKeepsTheUserOutInAFolderThatKeptTheUserOut(t *testing.T) {
	user := fspermtest.UserSID(t)
	for name, list := range map[string]string{
		"from reading its permissions":  "D:P(D;;RC;;;OW)(A;;FA;;;WD)",
		"from changing its permissions": "D:P(D;;WD;;;OW)(A;;FA;;;" + user + ")(A;;FR;;;AU)",
	} {
		t.Run(name, func(t *testing.T) {
			dir, path := lockedSpool(t, func(path string) { fspermtest.SetAccessListAsIs(t, path, list) })
			want := path + " does not let you both read and change who may use it, so Loadline cannot make sure it is private; "

			if err := Make(dir); err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("Make = %v, want it refused, starting %q", err, want)
			}
		})
	}
}

// ownerKeptFrom makes reading the owner of path, for the rest of the test,
// fail as Windows fails it for a file that belongs to someone else and keeps
// the user out, with "access denied" however long Loadline waits: an account
// that is not elevated cannot give a file another owner.
func ownerKeptFrom(t *testing.T, path string) {
	t.Helper()
	refused := fmt.Errorf("read the owner of %s: %w", path, windows.ERROR_ACCESS_DENIED)
	previousOwner, previousName := foreignOwner, ownerName
	foreignOwner = func(p string) (fsperm.Owner, error) {
		if p == path {
			return fsperm.Owner{}, refused
		}
		return previousOwner(p)
	}
	ownerName = func(p string) (string, error) {
		if p == path {
			return "", refused
		}
		return previousName(p)
	}
	t.Cleanup(func() { foreignOwner, ownerName = previousOwner, previousName })
}

// A file another process is writing that belongs to someone else and keeps
// the user out cannot be judged by its owner, nor put right, though it is
// there and not being deleted: install, its first check, the server and the
// creation of the token refuse it, and doctor reports it, in the same words.
func TestSecure_RefusesATemporaryFileOfSomeoneElsesThatKeepsTheUserOut(t *testing.T) {
	dir := home(t)
	path := filepath.Join(dir, ".token-1234")
	ownerKeptFrom(t, path)
	want := path + " does not let you both read and change who may use it, so Loadline cannot make sure it is private; " +
		"its owner cannot be read: remove it, or have an administrator remove it if you cannot, then run loadline install"

	for step, err := range map[string]error{
		"Verify": Verify(dir, filepath.Dir(dir)), "Make": Make(dir), "Secure": Secure(dir, filepath.Dir(dir)),
	} {
		if err == nil || err.Error() != want {
			t.Errorf("%s = %v;\nwant %s", step, err, want)
		}
	}
	if got, err := Check(dir, filepath.Dir(dir)); err != nil || got != want {
		t.Errorf("Check = %q, %v;\nwant %q", got, err, want)
	}
}

// One that Make would put right, as one begun before the data directory was
// private, may start being deleted the moment after its list was read: the
// question whether the user may change that list is refused then, as Windows
// refuses every question at a file being deleted. Install's first check
// looks again, finds the file going, and passes over it.
func TestVerify_PassesOverATemporaryFileThatStartsBeingDeletedOnceItsListWasRead(t *testing.T) {
	dir := home(t)
	path := filepath.Join(dir, ".token-1234")
	fspermtest.Grant(t, path, fspermtest.Read, windows.NO_INHERITANCE)
	started := false
	previous := canChangeList
	canChangeList = func(p string) (bool, error) {
		if p == path && !started {
			started = true
			fspermtest.DeletePending(t, p)
		}
		return previous(p)
	}
	t.Cleanup(func() { canChangeList = previous })

	if err := Verify(dir, filepath.Dir(dir)); err != nil {
		t.Errorf("Verify = %v, want it to pass over %s", err, path)
	}
	if !started {
		t.Fatalf("nothing started deleting %s once its list was read, so the test proves nothing", path)
	}
}

// The other steps that come to a temporary file after the walk judged it
// pass over one that starts being deleted meanwhile too: install's first
// check and doctor, which read its owner and its list, and Make, which reads
// them, and its list again to clear it. The walk's look finds the file here
// as the walk found it before its deletion started, with one name; the look
// again after a refused step finds it as it is. Where the read of the list
// is under test, the owner is answered for without a read.
func TestEveryStep_PassesOverATemporaryFileThatStartsBeingDeletedAfterItWasJudged(t *testing.T) {
	check := func(dir string) error {
		problem, err := Check(dir, filepath.Dir(dir))
		if problem != "" {
			return errors.New(problem)
		}
		return err
	}
	for _, tt := range []struct {
		name string
		// ownerAnswered answers for the file's owner without a read, so that
		// the read of its list meets the refusal.
		ownerAnswered bool
		step          func(dir string) error
	}{
		{name: "Verify reads its owner", step: func(dir string) error { return Verify(dir, filepath.Dir(dir)) }},
		{name: "Verify reads its list", ownerAnswered: true, step: func(dir string) error { return Verify(dir, filepath.Dir(dir)) }},
		{name: "Check reads its owner", step: check},
		{name: "Check reads its list", ownerAnswered: true, step: check},
		{name: "Make reads its list", ownerAnswered: true, step: Make},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := home(t)
			path := filepath.Join(dir, ".token-1234")
			lookAs(t, path, func() (fsperm.Entry, error) { return fsperm.Entry{Names: 1}, nil })
			if tt.ownerAnswered {
				ownedBy(t, path, fsperm.Owner{})
			}
			fspermtest.DeletePending(t, path)

			if err := tt.step(dir); err != nil {
				t.Errorf("%s = %v, want it to pass over %s", tt.name, err, path)
			}
		})
	}
}
