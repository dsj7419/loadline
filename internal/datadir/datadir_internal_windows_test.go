package datadir

import (
	"os"
	"path/filepath"
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
