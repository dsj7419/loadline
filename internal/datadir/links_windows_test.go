package datadir_test

// Links in and around the data directory: a junction named like a file
// Loadline writes before it puts it in place, which is a link among
// Loadline's own paths and refused, a junction above the data directory,
// and a data directory that is itself a link.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// elsewhere returns a folder that has nothing to do with Loadline, a
// project in a folder every signed-in user may work in, and a file in it.
func elsewhere(t *testing.T, top string) (project, doc string) {
	t.Helper()
	shared := filepath.Join(top, "team")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.Permissive(t, shared)
	project = filepath.Join(shared, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	doc = filepath.Join(project, "doc.txt")
	writeFile(t, doc)
	if others := fspermtest.Others(t, doc, fspermtest.Read); len(others) == 0 {
		t.Fatalf("%s is not shared, so the test proves nothing: %s", doc, fspermtest.AccessList(t, doc))
	}
	return project, doc
}

// openDataDir returns a data directory that is not private yet, in a folder
// that lets every signed-in user in, as LOADLINE_HOME on a second drive was
// before Loadline first secured it, and that folder.
func openDataDir(t *testing.T, top string) (dir, parent string) {
	t.Helper()
	parent = filepath.Join(top, "drive")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.Permissive(t, parent)
	dir = filepath.Join(parent, "loadline")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, parent
}

func TestSecure_RefusesAJunctionNamedLikeAFileBeingWritten(t *testing.T) {
	for _, name := range inFlight {
		t.Run(filepath.Base(name), func(t *testing.T) {
			top := privateTop(t)
			project, doc := elsewhere(t, top)
			dir, parent := openDataDir(t, top)
			link := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			junction(t, link, project)
			beforeProject, beforeDoc := fspermtest.AccessList(t, project), fspermtest.AccessList(t, doc)

			err := datadir.Secure(dir, parent)

			if err == nil || !strings.Contains(err.Error(), link+" is a link") {
				t.Errorf("Secure = %v, want it to refuse %s as a link", err, link)
			}
			if got := fspermtest.AccessList(t, project); !got.Equal(beforeProject) {
				t.Errorf("Secure changed the access list of %s, outside the data directory, from %s to %s", project, beforeProject, got)
			}
			if got := fspermtest.AccessList(t, doc); !got.Equal(beforeDoc) {
				t.Errorf("Secure changed the access list of %s, outside the data directory, from %s to %s", doc, beforeDoc, got)
			}
		})
	}
}

// The same on the path every hook takes when there is no token yet.
func TestMake_RefusesAJunctionNamedLikeAFileBeingWritten(t *testing.T) {
	top := privateTop(t)
	project, doc := elsewhere(t, top)
	dir, _ := openDataDir(t, top)
	link := filepath.Join(dir, ".token-1234")
	junction(t, link, project)
	before := fspermtest.AccessList(t, doc)

	err := datadir.Make(dir)

	if err == nil || !strings.Contains(err.Error(), link+" is a link") {
		t.Errorf("Make = %v, want it to refuse %s as a link", err, link)
	}
	if got := fspermtest.AccessList(t, doc); !got.Equal(before) {
		t.Errorf("Make changed the access list of %s, outside the data directory, from %s to %s", doc, before, got)
	}
}

// Doctor reports it as it reports any link among Loadline's paths.
func TestCheck_ReportsAJunctionNamedLikeAFileBeingWritten(t *testing.T) {
	home, top := loadlineHome(t)
	project, _ := elsewhere(t, top)
	link := filepath.Join(home, ".token-1234")
	junction(t, link, project)

	got, err := datadir.Check(home, top)

	if err != nil || !strings.Contains(got, link+" is a link") {
		t.Errorf("Check = %q, %v; want it to report %s as a link", got, err, link)
	}
}

// openItself opens the junction at path itself, not the folder it leads to.
func openItself(t *testing.T, path string, access uint32) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatalf("open the junction %s: %v", path, err)
	}
	return handle
}

// setListOfLink gives the junction at path itself the protected access list
// sddl describes.
func setListOfLink(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	handle := openItself(t, path, windows.READ_CONTROL|windows.WRITE_DAC)
	defer func() { _ = windows.CloseHandle(handle) }()
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatalf("give the junction %s the access list %s: %v", path, sddl, err)
	}
}

// A junction is a folder of its own, with an access list of its own, which
// Windows reads for its name. One above the data directory that every
// signed-in user may delete is a folder above it that others can move aside.
func TestVerify_RefusesAJunctionAboveThatOthersCanDelete(t *testing.T) {
	top := privateTop(t)
	target := filepath.Join(top, "safe")
	if err := fsperm.MkdirAll(target); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(top, "tools")
	junction(t, link, target)
	setListOfLink(t, link, "D:P(A;;FA;;;"+fspermtest.UserSID(t)+")(A;;SD;;;AU)")

	err := datadir.Verify(filepath.Join(link, "loadline"), top)

	if err == nil || !strings.Contains(err.Error(), link+" lets ") {
		t.Errorf("Verify = %v, want it to refuse %s, which every signed-in user may delete", err, link)
	}
}

// linkedHome returns a data directory that is a junction, in a private
// folder, to a folder made the ordinary way in what stands for the root of
// a second drive, which lets every signed-in user modify what it holds.
func linkedHome(t *testing.T) (top, home, target string) {
	t.Helper()
	top = privateTop(t)
	drive := filepath.Join(top, "drive")
	if err := fsperm.MkdirAll(drive); err != nil {
		t.Fatal(err)
	}
	fspermtest.Grant(t, drive, 0x1301bf, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT|windows.INHERIT_ONLY)
	target = filepath.Join(drive, "loadline-data")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(top, "profile")
	if err := fsperm.MkdirAll(profile); err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(profile, "loadline")
	junction(t, home, target)
	return top, home, target
}

// The same folder, named itself, is a data directory like any other, and is
// made private.
func TestSecure_MakesTheFolderALinkLeadsToPrivateWhenNamedItself(t *testing.T) {
	top, _, target := linkedHome(t)

	if err := datadir.Secure(target, top); err != nil {
		t.Fatalf("Secure(%s) = %v", target, err)
	}

	if others := fspermtest.Others(t, target, fspermtest.Any); len(others) != 0 {
		t.Errorf("%s admits %v after Secure", target, others)
	}
}

// A data directory that is itself a link is made private as the link, while
// the folder it leads to, which holds the token and the binary, keeps what
// it had. Install, the server and the creation of the token refuse it,
// saying where it leads, and doctor reports it.
func TestSecure_RefusesADataDirectoryThatIsALink(t *testing.T) {
	top, home, target := linkedHome(t)
	before := fspermtest.AccessList(t, target)
	want := home + " is a link to " + target

	for step, err := range map[string]error{
		"Verify": datadir.Verify(home, top), "Make": datadir.Make(home), "Secure": datadir.Secure(home, top),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), "set LOADLINE_HOME to "+target) {
			t.Errorf("%s = %v; want it refused, starting %q and suggesting %s", step, err, want, target)
		}
	}
	if got, err := datadir.Check(home, top); err != nil || !strings.HasPrefix(got, want) {
		t.Errorf("Check = %q, %v; want it to report %q", got, err, want)
	}
	if after := fspermtest.AccessList(t, target); !after.Equal(before) {
		t.Errorf("the access list of %s, where the link leads, went from %s to %s", target, before, after)
	}
}

// refusedAsALink fails the test unless every way into the data directory
// refuses home as a link. Verify, Secure and Check judge the folders above
// first, and know the trusted top only by its plain name: for a name
// written otherwise (plain false), any refusal of theirs will do, and Make,
// which looks at home alone, must still say it is a link.
func refusedAsALink(t *testing.T, home, top string, plain bool) {
	t.Helper()
	refused := func(err error) bool {
		return err != nil && (!plain || strings.Contains(err.Error(), "is a link"))
	}
	if err := datadir.Verify(home, top); !refused(err) {
		t.Errorf("Verify(%q) = %v, want it refused as a link", home, err)
	}
	if err := datadir.Make(home); err == nil || !strings.Contains(err.Error(), "is a link") {
		t.Errorf("Make(%q) = %v, want it refused as a link", home, err)
	}
	if err := datadir.Secure(home, top); !refused(err) {
		t.Errorf("Secure(%q) = %v, want it refused as a link", home, err)
	}
	got, err := datadir.Check(home, top)
	if err != nil || got == "" || (plain && !strings.Contains(got, "is a link")) {
		t.Errorf("Check(%q) = %q, %v; want it reported as a link", home, got, err)
	}
}

// However the data directory's path is written, with a separator at its
// end, a dot, another case or the \\?\ prefix, it names the same link,
// which is refused, and nothing is written where it leads.
func TestSecure_RefusesALinkedDataDirectoryHoweverItIsWritten(t *testing.T) {
	top, home, target := linkedHome(t)
	before := fspermtest.AccessList(t, target)
	upper := filepath.Join(filepath.Dir(home), strings.ToUpper(filepath.Base(home)))

	for _, written := range []string{home, home + `\`, home + `\.`, upper} {
		refusedAsALink(t, written, top, true)
	}
	refusedAsALink(t, `\\?\`+home, top, false)
	if _, err := config.EnsureToken(filepath.Join(home, "token")); err == nil || !strings.Contains(err.Error(), "is a link") {
		t.Errorf("EnsureToken through the link = %v, want the token's creation refused", err)
	}

	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Errorf("%s, where the link leads, holds %d entries (%v), want nothing written", target, len(entries), err)
	}
	if after := fspermtest.AccessList(t, target); !after.Equal(before) {
		t.Errorf("the access list of %s went from %s to %s", target, before, after)
	}
}

// A link to a link: each is refused in turn, and the folder at the end,
// named itself, is taken and made private.
func TestSecure_RefusesALinkToALink(t *testing.T) {
	top, home, target := linkedHome(t)
	second := filepath.Join(filepath.Dir(home), "second")
	junction(t, second, home)

	refusedAsALink(t, second, top, true)

	if err := datadir.Secure(target, top); err != nil {
		t.Errorf("Secure(%s), the folder itself = %v", target, err)
	}
	if others := fspermtest.Others(t, target, fspermtest.Any); len(others) != 0 {
		t.Errorf("%s admits %v", target, others)
	}
}

// A link that leads nowhere is still a link: nothing is made through it or
// in its place.
func TestSecure_RefusesALinkedDataDirectoryThatLeadsNowhere(t *testing.T) {
	top, home, target := linkedHome(t)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	_, tokenErr := config.EnsureToken(filepath.Join(home, "token"))
	for step, err := range map[string]error{"Verify": datadir.Verify(home, top), "Make": datadir.Make(home), "EnsureToken": tokenErr} {
		if err == nil {
			t.Errorf("%s took %s, a link that leads nowhere", step, home)
		}
	}
	if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s was made where the link leads (%v)", target, err)
	}
	// Doctor reports a link, which it is, not a directory not made yet.
	if got, err := datadir.Check(home, top); err != nil || !strings.HasPrefix(got, home+" is a link") {
		t.Errorf("Check = %q, %v; want it reported as a link", got, err)
	}
}

// A file of Loadline's with a second name, a hard link to a file elsewhere,
// is that file: making it private would take it from those who share it
// there. It is refused like a link, whether the folder it is in can be
// listed at first or only once Secure has put that folder's list right.
func TestSecure_LeavesAFileOutsideTheDataDirectoryAsItIs(t *testing.T) {
	for _, locked := range []bool{false, true} {
		name := "in a spool that can be listed"
		if locked {
			name = "in a locked spool"
		}
		t.Run(name, func(t *testing.T) {
			home, top := loadlineHome(t)
			_, doc := elsewhere(t, top)
			spool := filepath.Join(home, "spool")
			event := filepath.Join(spool, "1-0000000b.json")
			if err := os.Link(doc, event); err != nil {
				t.Skipf("cannot make a hard link here: %v", err)
			}
			if locked {
				// Stored as it is: SetNamedSecurityInfo would pass the new
				// list down to the event, and so change the file elsewhere
				// before Secure ever ran.
				fspermtest.SetAccessListAsIs(t, spool, "D:P(A;OICI;FA;;;SY)")
				t.Cleanup(func() { _ = fsperm.ClearExplicit(spool) })
				if _, err := os.ReadDir(spool); err == nil {
					t.Fatalf("%s can still be listed, so the test proves nothing", spool)
				}
			}
			before := fspermtest.AccessList(t, doc)

			err := datadir.Secure(home, top)

			if want := event + " has another name"; err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("Secure = %v, want it refused, starting %q", err, want)
			}
			if after := fspermtest.AccessList(t, doc); !after.Equal(before) {
				t.Errorf("Secure changed the access list of %s, outside the data directory, from %s to %s", doc, before, after)
			}
			if got, err := datadir.Check(home, top); err != nil || !strings.HasPrefix(got, event+" has another name") {
				t.Errorf("Check = %q, %v; want it to report %s", got, err, event)
			}
		})
	}
}

// A data directory that is not private yet holds a spool whose list keeps the
// user out but takes what the directory passes down. Had the directory's new
// list been passed down at once, it would have let the user into the spool,
// and been passed on to the file with a second name hidden there, before the
// walk could see it. Secure passes it down only once the walk is through.
func TestSecure_LeavesAFileOutsideAsItIsWhenTheDataDirectoryIsNotPrivateYet(t *testing.T) {
	top := privateTop(t)
	_, doc := elsewhere(t, top)
	dir, parent := openDataDir(t, top)
	spool := filepath.Join(dir, "spool")
	if err := os.Mkdir(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	event := filepath.Join(spool, "1-0000000b.json")
	if err := os.Link(doc, event); err != nil {
		t.Skipf("cannot make a hard link here: %v", err)
	}
	// Stored as it is, so that nothing reaches the event yet: as though the
	// spool had only inherited SYSTEM's entry.
	fspermtest.SetAccessListAsIs(t, spool, "D:AI(A;OICIID;FA;;;SY)")
	t.Cleanup(func() { _ = fsperm.ClearExplicit(spool) })
	if _, err := os.ReadDir(spool); err == nil {
		t.Fatalf("%s can still be listed, so the test proves nothing", spool)
	}
	if private, err := fsperm.EntryPrivate(dir); err != nil || private {
		t.Fatalf("%s is private already (%v), so the test proves nothing", dir, err)
	}
	before := fspermtest.AccessList(t, doc)

	err := datadir.Secure(dir, parent)

	if want := event + " has another name"; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Secure = %v, want it refused, starting %q", err, want)
	}
	if after := fspermtest.AccessList(t, doc); !after.Equal(before) {
		t.Errorf("Secure changed the access list of %s, outside the data directory, from %s to %s", doc, before, after)
	}
	// The directory itself is private first, refused or not.
	if others := fspermtest.Others(t, dir, fspermtest.Any); len(others) > 0 || !fspermtest.Protected(t, dir) {
		t.Errorf("%s admits %v (protected: %t), want the private list", dir, others, fspermtest.Protected(t, dir))
	}
	if got, err := datadir.Check(dir, parent); err != nil || !strings.HasPrefix(got, event+" has another name") {
		t.Errorf("Check = %q, %v; want it to report %s", got, err, event)
	}
}

// A file's own leftover temporary name beside it excuses no other name. In a
// spool that keeps the user out, a spooled event has both, its temporary
// name and a name elsewhere: Secure removes the leftover, judges the event
// again, and refuses it for the name elsewhere, which keeps its list.
func TestSecure_RefusesANameElsewhereHiddenBesideALeftoverTemporaryName(t *testing.T) {
	home, top := loadlineHome(t)
	_, doc := elsewhere(t, top)
	spool := filepath.Join(home, "spool")
	event := filepath.Join(spool, "1-0000000b.json")
	for _, name := range []string{event, filepath.Join(spool, ".put-1234.tmp")} {
		if err := os.Link(doc, name); err != nil {
			t.Skipf("cannot make a hard link here: %v", err)
		}
	}
	fspermtest.SetAccessListAsIs(t, spool, "D:P(A;OICI;FA;;;SY)")
	t.Cleanup(func() { _ = fsperm.ClearExplicit(spool) })
	before := fspermtest.AccessList(t, doc)

	err := datadir.Secure(home, top)

	if want := event + " has another name"; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Secure = %v, want it refused, starting %q", err, want)
	}
	if after := fspermtest.AccessList(t, doc); !after.Equal(before) {
		t.Errorf("Secure changed the access list of %s, outside the data directory, from %s to %s", doc, before, after)
	}
}

// A refused Secure still puts right each of Loadline's files and folders
// that the walk judged before it came to what it refused: a file as soon as
// it is judged, since its list reaches nothing else, and a folder once the
// walk has been through what it holds, which its list reaches. The data
// directory passes nothing down, so a file another process began there
// before it was private keeps what it had. Nothing outside changes.
func TestSecure_RepairsWhatItHasJudgedEvenWhenItRefuses(t *testing.T) {
	top := privateTop(t)
	_, doc := elsewhere(t, top)
	dir, parent := openDataDir(t, top)
	bin := filepath.Join(dir, "bin")
	binary := filepath.Join(bin, "loadline.exe")
	spool := filepath.Join(dir, "spool")
	for _, folder := range []string{bin, spool} {
		if err := os.Mkdir(folder, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, binary)
	fspermtest.Grant(t, binary, windows.FILE_GENERIC_READ, windows.NO_INHERITANCE)
	begun := filepath.Join(dir, ".token-1234")
	writeFile(t, begun)
	// The token's name comes after the spool's, so the binary, which the walk
	// comes to before the spool, stands for Loadline's files here.
	if err := os.Link(doc, filepath.Join(spool, "1-0000000b.json")); err != nil {
		t.Skipf("cannot make a hard link here: %v", err)
	}
	fspermtest.SetAccessListAsIs(t, spool, "D:P(A;OICI;FA;;;SY)")
	t.Cleanup(func() { _ = fsperm.ClearExplicit(spool) })
	for _, path := range []string{bin, binary, begun} {
		if others := fspermtest.Others(t, path, fspermtest.Any); len(others) == 0 {
			t.Fatalf("%s admits nobody else, so the test proves nothing", path)
		}
	}
	beforeBegun, beforeDoc := fspermtest.AccessList(t, begun), fspermtest.AccessList(t, doc)

	if err := datadir.Secure(dir, parent); err == nil || !strings.Contains(err.Error(), "has another name") {
		t.Fatalf("Secure = %v, want it refused for the hard link in the spool", err)
	}

	for _, path := range []string{bin, binary} {
		if others := fspermtest.Others(t, path, fspermtest.Any); len(others) > 0 {
			t.Errorf("the refused Secure left %s admitting %v, want it put right", path, others)
		}
	}
	// Put right, the folder takes what the data directory passes down, not
	// the list of its own it was given to be listed.
	if fspermtest.Protected(t, bin) {
		t.Errorf("the refused Secure left %s with a list of its own: %s", bin, fspermtest.AccessList(t, bin))
	}
	if after := fspermtest.AccessList(t, begun); !after.Equal(beforeBegun) {
		t.Errorf("the refused Secure passed a list down to %s: %s became %s", begun, beforeBegun, after)
	}
	if after := fspermtest.AccessList(t, doc); !after.Equal(beforeDoc) {
		t.Errorf("the refused Secure changed the access list of %s, outside the data directory, from %s to %s", doc, beforeDoc, after)
	}
}

// A junction hidden in a folder that keeps the user out, and that keeps them
// out itself: locking the spool passed its list down to the junction. Doctor
// reports the spool; Secure makes the spool listable, finds the junction
// and refuses it, changing nothing where it leads; doctor then reports the
// junction.
func TestSecure_RefusesAJunctionHiddenInAFolderThatKeepsTheUserOut(t *testing.T) {
	home, top := loadlineHome(t)
	project, doc := elsewhere(t, top)
	spool := filepath.Join(home, "spool")
	hidden := filepath.Join(spool, "x")
	junction(t, hidden, project)
	beforeProject, beforeDoc := fspermtest.AccessList(t, project), fspermtest.AccessList(t, doc)
	fspermtest.SetAccessList(t, spool, "D:P(A;OICI;FA;;;SY)")
	t.Cleanup(func() { _ = fsperm.ClearExplicit(spool) })

	if got, err := datadir.Check(home, top); err != nil || !strings.HasPrefix(got, spool+" keeps you from seeing what it holds") {
		t.Errorf("Check before = %q, %v; want it to report %s", got, err, spool)
	}
	err := datadir.Secure(home, top)

	if err == nil || !strings.HasPrefix(err.Error(), hidden+" is a link") {
		t.Errorf("Secure = %v, want it to refuse %s as a link", err, hidden)
	}
	if after := fspermtest.AccessList(t, project); !after.Equal(beforeProject) {
		t.Errorf("the access list of %s, where the junction leads, went from %s to %s", project, beforeProject, after)
	}
	if after := fspermtest.AccessList(t, doc); !after.Equal(beforeDoc) {
		t.Errorf("the access list of %s went from %s to %s", doc, beforeDoc, after)
	}
	if got, err := datadir.Check(home, top); err != nil || !strings.HasPrefix(got, hidden+" is a link") {
		t.Errorf("Check after = %q, %v; want it to report %s as a link", got, err, hidden)
	}
}
