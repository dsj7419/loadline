package fsperm_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// sharedRoot returns a new directory inside one that lets every signed-in
// user modify what is below it, as LOADLINE_HOME on a second drive does.
func sharedRoot(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	fspermtest.Permissive(t, parent)
	root := filepath.Join(parent, "loadline")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPrivate_AdmitsOnlyTheUserSystemAndAdministrators(t *testing.T) {
	root := sharedRoot(t)
	if others := fspermtest.Others(t, root, fspermtest.Any); len(others) == 0 {
		t.Fatalf("%s admits nobody else before Private, so the test proves nothing", root)
	}

	if err := fsperm.Private(root); err != nil {
		t.Fatalf("Private: %v", err)
	}

	if others := fspermtest.Others(t, root, fspermtest.Any); len(others) > 0 {
		t.Errorf("%s admits %s, want only the user, SYSTEM and Administrators", root, strings.Join(others, ", "))
	}
	if !fspermtest.Protected(t, root) {
		t.Errorf("%s still takes what its parent passes down", root)
	}
}

func TestPrivate_ReplacesWhatExistingFilesInherited(t *testing.T) {
	root := sharedRoot(t)
	files := []string{
		filepath.Join(root, "token"),
		filepath.Join(root, "spool", "event.json"),
		filepath.Join(root, "bin", "loadline.exe"),
	}
	for _, path := range files {
		write(t, path)
	}

	if err := fsperm.Private(root); err != nil {
		t.Fatalf("Private: %v", err)
	}

	for _, path := range append(files, filepath.Join(root, "spool"), filepath.Join(root, "bin")) {
		if others := fspermtest.Others(t, path, fspermtest.Any); len(others) > 0 {
			t.Errorf("%s admits %s after Private, want what it inherited replaced", path, strings.Join(others, ", "))
		}
	}
}

func TestPrivate_WhatIsCreatedLaterInheritsIt(t *testing.T) {
	root := sharedRoot(t)
	if err := fsperm.Private(root); err != nil {
		t.Fatalf("Private: %v", err)
	}

	later := filepath.Join(root, "spool", "bad", "event.json")
	write(t, later)

	for _, path := range []string{later, filepath.Dir(later)} {
		if others := fspermtest.Others(t, path, fspermtest.Any); len(others) > 0 {
			t.Errorf("%s admits %s, want it to inherit the private access list", path, strings.Join(others, ", "))
		}
	}
	data, err := os.ReadFile(later)
	if err != nil || string(data) != "x" {
		t.Errorf("read %s back = %q, %v; want the user to keep full access", later, data, err)
	}
}

func TestPrivate_MissingDirFails(t *testing.T) {
	err := fsperm.Private(filepath.Join(t.TempDir(), "missing"))

	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Private = %v, want an error that matches fs.ErrNotExist", err)
	}
}

// moveIn writes a file in a folder that lets every signed-in user modify
// what is created below it and moves it to path, which keeps what it
// inherited there: its access list is then not what the folder at path
// passes down, which shows whether anything rewrote it since.
func moveIn(t *testing.T, path string) {
	t.Helper()
	outside := filepath.Join(sharedRoot(t), filepath.Base(path))
	write(t, outside)
	if err := os.Rename(outside, path); err != nil {
		t.Fatal(err)
	}
	if others := fspermtest.Others(t, path, fspermtest.Any); len(others) == 0 {
		t.Fatalf("%s admits nobody else, so the test proves nothing", path)
	}
}

// Applying a list to a folder makes Windows apply it to everything the
// folder holds again, rewriting what each file inherited, files another
// Loadline process is writing at that moment among them. A folder that has
// the private list already is left as it is.
func TestPrivate_ChangesNothingInAFolderThatIsPrivateAlready(t *testing.T) {
	root := filepath.Join(t.TempDir(), "loadline")
	if err := fsperm.MkdirAll(root); err != nil {
		t.Fatal(err)
	}
	inFlight := filepath.Join(root, ".token-1234")
	moveIn(t, inFlight)
	before := fspermtest.AccessList(t, inFlight)

	if err := fsperm.Private(root); err != nil {
		t.Fatalf("Private: %v", err)
	}

	if after := fspermtest.AccessList(t, inFlight); after != before {
		t.Errorf("Private of the private %s rewrote the access list of %s from %s to %s", root, inFlight, before, after)
	}
}

// A folder that keeps nothing of what its parent passes down has the
// private list only when it has that list itself. Any other list of its
// own is replaced with it.
func TestPrivate_ReplacesAnyOtherListOfTheFoldersOwn(t *testing.T) {
	user := fspermtest.UserSID(t)
	reference := filepath.Join(t.TempDir(), "reference")
	if err := os.Mkdir(reference, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fsperm.Private(reference); err != nil {
		t.Fatal(err)
	}
	want := fspermtest.AccessList(t, reference)

	for name, list := range map[string]string{
		"one more entry":        "D:P(A;OICI;FA;;;" + user + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FR;;;AU)",
		"another trustee":       "D:P(A;OICI;FA;;;" + user + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;AU)",
		"fewer rights":          "D:P(A;OICI;FR;;;" + user + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)",
		"nothing passed down":   "D:P(A;;FA;;;" + user + ")(A;;FA;;;SY)(A;;FA;;;BA)",
		"a denial, not a grant": "D:P(D;OICI;FA;;;" + user + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)",
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "loadline")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			fspermtest.SetAccessList(t, root, list)

			if err := fsperm.Private(root); err != nil {
				t.Fatalf("Private: %v", err)
			}

			if got := fspermtest.AccessList(t, root); got != want {
				t.Errorf("after Private, %s, which had %s, has %s; want %s", root, list, got, want)
			}
		})
	}
}

// A folder with the private entries that still takes what its parent
// passes down is not private either: it takes whatever its parent passes
// down later.
func TestPrivate_ProtectsAFolderWithThePrivateEntriesThatStillInherits(t *testing.T) {
	user := fspermtest.UserSID(t)
	// A parent that passes nothing down, so that the folder's list holds
	// the private entries alone.
	parent := t.TempDir()
	fspermtest.SetAccessList(t, parent, "D:P(A;;FA;;;"+user+")")
	root := filepath.Join(parent, "loadline")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.SetAccessList(t, root, "D:(A;OICI;FA;;;"+user+")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")

	if err := fsperm.Private(root); err != nil {
		t.Fatalf("Private: %v", err)
	}

	if !fspermtest.Protected(t, root) {
		t.Errorf("%s still takes what its parent passes down after Private", root)
	}
}

// Windows refuses to open a file whose deletion is pending, which another
// process deleting it causes for a moment, with "access denied". Making
// the folder private passes over such a file.
func TestPrivate_TakesAFolderWhileAFileInItIsBeingDeleted(t *testing.T) {
	root := sharedRoot(t)
	going := filepath.Join(root, "spool", "1-0000000a.json")
	write(t, going)
	fspermtest.DeletePending(t, going)

	if err := fsperm.Private(root); err != nil {
		t.Errorf("Private: %v", err)
	}
}

// Every way of looking at a file whose deletion is pending is refused in a
// way Transient takes for one that may pass, and once the file is gone, the
// answer is that it does not exist, which is no refusal.
func TestTransient_IsHowWindowsRefusesAFileBeingDeleted(t *testing.T) {
	going := filepath.Join(t.TempDir(), ".token-1234")
	write(t, going)
	release := fspermtest.DeletePending(t, going)

	_, linkErr := fsperm.IsLink(going)
	_, exposureErr := fsperm.Exposure(going)
	for name, err := range map[string]error{
		"IsLink":        linkErr,
		"Exposure":      exposureErr,
		"ClearExplicit": fsperm.ClearExplicit(going),
	} {
		if !fsperm.Transient(err) {
			t.Errorf("%s of a file being deleted = %v, want a refusal Transient takes", name, err)
		}
	}

	release()
	_, err := fsperm.IsLink(going)
	if !errors.Is(err, fs.ErrNotExist) || fsperm.Transient(err) {
		t.Errorf("IsLink of the deleted file = %v, want fs.ErrNotExist, which is not transient", err)
	}
}

func TestTransient_TakesOnlyARefusalThatMayPass(t *testing.T) {
	for name, tt := range map[string]struct {
		err  error
		want bool
	}{
		"access denied":            {&fs.PathError{Op: "open", Path: "token", Err: windows.ERROR_ACCESS_DENIED}, true},
		"sharing violation":        {fmt.Errorf("read the access list of token: %w", windows.ERROR_SHARING_VIOLATION), true},
		"not found":                {&fs.PathError{Op: "open", Path: "token", Err: windows.ERROR_FILE_NOT_FOUND}, false},
		"invalid parameter":        {windows.ERROR_INVALID_PARAMETER, false},
		"an error of another kind": {errors.New("access denied"), false},
		"none":                     {nil, false},
	} {
		if got := fsperm.Transient(tt.err); got != tt.want {
			t.Errorf("%s: Transient(%v) = %t, want %t", name, tt.err, got, tt.want)
		}
	}
}

func TestExposure_NamesWhoElseTheAccessListAdmits(t *testing.T) {
	root := sharedRoot(t)

	exposure, err := fsperm.Exposure(root)
	if err != nil {
		t.Fatalf("Exposure: %v", err)
	}

	users := fspermtest.AuthenticatedUsers(t)
	if !strings.HasPrefix(exposure, "its access list also admits ") || !strings.Contains(exposure, users) {
		t.Errorf("Exposure = %q, want it to say the access list also admits %s", exposure, users)
	}
}

func TestExposure_IsEmptyOncePrivate(t *testing.T) {
	root := sharedRoot(t)
	if err := fsperm.Private(root); err != nil {
		t.Fatalf("Private: %v", err)
	}

	exposure, err := fsperm.Exposure(root)
	if err != nil || exposure != "" {
		t.Errorf("Exposure = %q, %v; want \"\" for a private directory", exposure, err)
	}
}

// A null access list, as a file system without access control gives, or a
// tool that resets one, lets everyone do anything.
func TestExposure_NoAccessListAdmitsEveryone(t *testing.T) {
	dir := t.TempDir()
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, nil, nil); err != nil {
		t.Fatalf("give %s a null access list: %v", dir, err)
	}

	exposure, err := fsperm.Exposure(dir)

	if want := "it has no access list, which admits everyone"; err != nil || exposure != want {
		t.Errorf("Exposure = %q, %v; want %q", exposure, err, want)
	}
}

// Windows keeps allow entries of several kinds, each laying its trustee out
// in its own way, and lets the trustee of any of them in. Exposure names
// the trustee of each.
func TestExposure_NamesWhoEveryKindOfAllowEntryAdmits(t *testing.T) {
	for name, entry := range map[string]fspermtest.Entry{
		"plain":                              {},
		"object, with no object type":        {Kind: fspermtest.Object},
		"object, with an object type":        {Kind: fspermtest.Object, ObjectType: true},
		"object, with an inherited type":     {Kind: fspermtest.Object, InheritedObjectType: true},
		"object, with both types":            {Kind: fspermtest.Object, ObjectType: true, InheritedObjectType: true},
		"callback":                           {Kind: fspermtest.Callback},
		"callback object":                    {Kind: fspermtest.CallbackObject},
		"callback object, with both types":   {Kind: fspermtest.CallbackObject, ObjectType: true, InheritedObjectType: true},
		"compound, through a trusted server": {Kind: fspermtest.Compound},
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "loadline")
			if err := fsperm.MkdirAll(root); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "token")
			write(t, file)
			entry.Mask = fspermtest.FullControl
			fspermtest.SetEntry(t, file, entry)

			exposure, err := fsperm.Exposure(file)

			if users := fspermtest.AuthenticatedUsers(t); err != nil || exposure != "its access list also admits "+users {
				t.Errorf("Exposure = %q, %v; want it to say the access list also admits %s", exposure, err, users)
			}
		})
	}
}

// An inheritable entry for CREATOR GROUP lets in the primary group of each
// account that creates a file below, which other accounts are often in
// too. One for CREATOR OWNER lets in only the creator, who is the user in a
// folder only the user may write to.
func TestExposure_NamesCreatorGroupButNotCreatorOwner(t *testing.T) {
	user := fspermtest.UserSID(t)
	for placeholder, want := range map[string]bool{"CG": true, "CO": false} {
		t.Run(placeholder, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "loadline")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			fspermtest.SetAccessList(t, root, "D:P(A;OICIIO;FA;;;"+placeholder+")(A;OICI;FA;;;"+user+")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")

			exposure, err := fsperm.Exposure(root)

			if err != nil || (exposure != "") != want {
				t.Errorf("Exposure of a folder with an inheritable entry for %s = %q, %v; want it named: %t", placeholder, exposure, err, want)
			}
		})
	}
}

// An object entry that lets every signed-in user delete a folder above the
// data directory lets them move it aside, as a plain one does.
func TestMovableAncestor_CountsEveryKindOfAllowEntry(t *testing.T) {
	for name, kind := range map[string]byte{"plain": 0, "object": fspermtest.Object, "compound": fspermtest.Compound} {
		t.Run(name, func(t *testing.T) {
			top := privateTop(t)
			tools := filepath.Join(top, "tools")
			if err := os.Mkdir(tools, 0o700); err != nil {
				t.Fatal(err)
			}
			fspermtest.SetEntry(t, tools, fspermtest.Entry{Kind: kind, Mask: windows.DELETE, Protected: true})
			t.Cleanup(func() { fspermtest.SetAccessList(t, tools, "D:(A;OICI;FA;;;WD)") })

			got, err := fsperm.MovableAncestor(filepath.Join(tools, "loadline"), top)

			if want := tools + " lets " + fspermtest.AuthenticatedUsers(t) + " delete or rename it"; err != nil || got != want {
				t.Errorf("MovableAncestor = %q, %v; want %q", got, err, want)
			}
		})
	}
}

// broughtIn writes a file in a folder whose list is the protected one
// sddl describes, and moves it into root, which keeps what it inherited
// there.
func broughtIn(t *testing.T, root, sddl string) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.SetAccessList(t, source, sddl)
	file := filepath.Join(source, "loadline.log")
	write(t, file)
	moved := filepath.Join(root, "loadline.log")
	if err := os.Rename(file, moved); err != nil {
		t.Fatal(err)
	}
	return moved
}

// A file or folder in a private folder holds nothing of its own when all it
// has is what that folder passes down, full control for the user, SYSTEM
// and Administrators, or when it is a folder with the private list itself.
// Anything else is its own.
func TestEntryPrivate_TakesOnlyWhatAPrivateFolderGives(t *testing.T) {
	user := fspermtest.UserSID(t)
	trusted := "(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	for name, tt := range map[string]struct {
		make func(t *testing.T, root string) string
		want bool
	}{
		"a file the folder made": {func(t *testing.T, root string) string {
			t.Helper()
			path := filepath.Join(root, "token")
			write(t, path)
			return path
		}, true},
		"a folder with the private list": {func(t *testing.T, root string) string {
			t.Helper()
			path := filepath.Join(root, "spool")
			if err := fsperm.MkdirAll(path); err != nil {
				t.Fatal(err)
			}
			return path
		}, true},
		"a file brought from a folder that passes the same down": {func(t *testing.T, root string) string {
			t.Helper()
			return broughtIn(t, root, "D:P(A;OICI;FA;;;"+user+")"+trusted)
		}, true},
		"an entry given explicitly": {func(t *testing.T, root string) string {
			t.Helper()
			path := filepath.Join(root, "token")
			write(t, path)
			fspermtest.SetAccessList(t, path, "D:(A;;FA;;;"+user+")")
			return path
		}, false},
		"another list of its own": {func(t *testing.T, root string) string {
			t.Helper()
			path := filepath.Join(root, "spool")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			fspermtest.SetAccessList(t, path, "D:P(A;OICI;FA;;;"+user+")(A;OICI;FA;;;SY)")
			return path
		}, false},
		"a denial brought from elsewhere": {func(t *testing.T, root string) string {
			t.Helper()
			return broughtIn(t, root, "D:P(D;OICIIO;CC;;;"+user+")(A;OICI;FA;;;"+user+")"+trusted)
		}, false},
		"less than full control brought from elsewhere": {func(t *testing.T, root string) string {
			t.Helper()
			return broughtIn(t, root, "D:P(A;OICIIO;FR;;;"+user+")(A;;FA;;;"+user+")"+trusted)
		}, false},
		"an entry for others brought from elsewhere": {func(t *testing.T, root string) string {
			t.Helper()
			return broughtIn(t, root, "D:P(A;OICI;FR;;;AU)(A;OICI;FA;;;"+user+")"+trusted)
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "loadline")
			if err := fsperm.MkdirAll(root); err != nil {
				t.Fatal(err)
			}
			path := tt.make(t, root)

			got, err := fsperm.EntryPrivate(path)

			if err != nil || got != tt.want {
				t.Errorf("EntryPrivate(%s), whose list is %s = %t, %v; want %t", path, fspermtest.AccessList(t, path), got, err, tt.want)
			}
		})
	}
}

func TestExposure_MissingPathFails(t *testing.T) {
	_, err := fsperm.Exposure(filepath.Join(t.TempDir(), "missing"))

	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Exposure = %v, want an error that matches fs.ErrNotExist", err)
	}
}

func TestMkdirAll_CreatesEachMissingFolderPrivateFromTheStart(t *testing.T) {
	parent := t.TempDir()
	fspermtest.Permissive(t, parent)
	dir := filepath.Join(parent, "tools", "loadline", "bin")

	if err := fsperm.MkdirAll(dir); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	for _, path := range []string{filepath.Join(parent, "tools"), filepath.Join(parent, "tools", "loadline"), dir} {
		if !fspermtest.Protected(t, path) {
			t.Errorf("%s takes what its parent passes down, want a private access list of its own", path)
		}
		if others := fspermtest.Others(t, path, fspermtest.Any); len(others) > 0 {
			t.Errorf("%s admits %s, want only the user, SYSTEM and Administrators", path, strings.Join(others, ", "))
		}
	}
	later := filepath.Join(dir, "loadline.exe")
	write(t, later)
	if others := fspermtest.Others(t, later, fspermtest.Any); len(others) > 0 {
		t.Errorf("%s admits %s, want it to inherit the private access list", later, strings.Join(others, ", "))
	}
}

func TestMkdirAll_LeavesAnExistingFolderAsItIs(t *testing.T) {
	root := sharedRoot(t)

	if err := fsperm.MkdirAll(root); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if fspermtest.Protected(t, root) || len(fspermtest.Others(t, root, fspermtest.Any)) == 0 {
		t.Errorf("MkdirAll changed the access list of %s, which already existed", root)
	}
}

func TestMkdirAll_AFileInTheWayFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	write(t, file)

	if err := fsperm.MkdirAll(filepath.Join(file, "bin")); err == nil {
		t.Error("MkdirAll below a file succeeded, want an error")
	}
	if err := fsperm.MkdirAll(file); err == nil {
		t.Error("MkdirAll of a file succeeded, want an error")
	}
}

// The tests below take the temporary folder they build in, with every folder
// above it, for a drive root. They make it private first, so that what the
// machine's own temporary folder passes down, which may let other accounts
// in, is no test's subject.
func privateTop(t *testing.T) string {
	t.Helper()
	top := t.TempDir()
	if err := fsperm.Private(top); err != nil {
		t.Fatal(err)
	}
	return top
}

func TestMovableAncestor_NamesTheNearestFolderOthersCanDeleteOrRename(t *testing.T) {
	top := privateTop(t)
	fspermtest.Permissive(t, top)
	tools := filepath.Join(top, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := fsperm.MovableAncestor(filepath.Join(tools, "loadline"), top)

	users := fspermtest.AuthenticatedUsers(t)
	if err != nil || !strings.HasPrefix(got, tools+" lets ") || !strings.Contains(got, users) || !strings.HasSuffix(got, " delete or rename it") {
		t.Errorf("MovableAncestor = %q, %v; want %s named, as one %s can delete or rename", got, err, tools, users)
	}
}

func TestMovableAncestor_DeletingWhatAFolderHoldsCounts(t *testing.T) {
	top := privateTop(t)
	shared := filepath.Join(top, "shared")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.Grant(t, shared, windows.ACCESS_MASK(fileDeleteChild), windows.NO_INHERITANCE)

	got, err := fsperm.MovableAncestor(filepath.Join(shared, "loadline"), top)

	if err != nil || !strings.HasPrefix(got, shared+" lets ") || !strings.HasSuffix(got, " delete or rename the folders in it") {
		t.Errorf("MovableAncestor = %q, %v; want %s named, as a folder whose entries others can delete or rename", got, err, shared)
	}
}

func TestMovableAncestor_FullControlCounts(t *testing.T) {
	top := privateTop(t)
	tools := filepath.Join(top, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.Grant(t, tools, windows.GENERIC_ALL, windows.NO_INHERITANCE)

	got, err := fsperm.MovableAncestor(filepath.Join(tools, "loadline"), top)

	if err != nil || !strings.HasPrefix(got, tools+" lets ") {
		t.Errorf("MovableAncestor = %q, %v; want %s named: GENERIC_ALL includes DELETE", got, err, tools)
	}
}

// An entry that only children inherit, such as a drive root's for
// Authenticated Users, grants nothing on the folder itself.
func TestMovableAncestor_AnEntryForChildrenOnlyDoesNotCount(t *testing.T) {
	top := privateTop(t)
	root := filepath.Join(top, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.Grant(t, root, windows.GENERIC_ALL, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT|windows.INHERIT_ONLY)

	got, err := fsperm.MovableAncestor(filepath.Join(root, "loadline"), top)

	if err != nil || got != "" {
		t.Errorf("MovableAncestor = %q, %v; want \"\": the entry applies only below %s", got, err, root)
	}
}

func TestMovableAncestor_FoldersLoadlineCreatesPass(t *testing.T) {
	top := privateTop(t)
	fspermtest.Permissive(t, top)
	home := filepath.Join(top, "tools", "loadline")
	if err := fsperm.MkdirAll(home); err != nil {
		t.Fatal(err)
	}

	got, err := fsperm.MovableAncestor(filepath.Join(home, "bin"), top)

	if err != nil || got != "" {
		t.Errorf("MovableAncestor = %q, %v; want \"\" for folders Loadline made private", got, err)
	}
}

// The default data directory, %LOCALAPPDATA%\loadline, must pass: the
// profile folders above it admit only the user, SYSTEM and Administrators,
// and the system drive's root lets others only add folders.
func TestMovableAncestor_TheDefaultDataDirectoryPasses(t *testing.T) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		t.Skip("%LOCALAPPDATA% is not set")
	}

	got, err := fsperm.MovableAncestor(filepath.Join(local, "loadline"), "")

	if err != nil || got != "" {
		t.Errorf("MovableAncestor of the default data directory = %q, %v; want \"\"", got, err)
	}
}

// fileDeleteChild is FILE_DELETE_CHILD: the right to delete, and so to
// rename, what a folder holds, whatever their own access lists say.
const fileDeleteChild = 0x40

func TestClearExplicit_LeavesOnlyWhatThePrivateFolderPassesDown(t *testing.T) {
	dir := privateTop(t)
	file := filepath.Join(dir, "token")
	write(t, file)
	fspermtest.Grant(t, file, fspermtest.Read, windows.NO_INHERITANCE)
	sub := filepath.Join(dir, "bin")
	if err := fsperm.MkdirAll(sub); err != nil {
		t.Fatal(err)
	}
	fspermtest.Grant(t, sub, fspermtest.Write, windows.NO_INHERITANCE)

	for _, path := range []string{file, sub} {
		if err := fsperm.ClearExplicit(path); err != nil {
			t.Fatalf("ClearExplicit(%s): %v", path, err)
		}
	}

	for _, path := range []string{file, sub} {
		if others := fspermtest.Others(t, path, fspermtest.Any); len(others) > 0 {
			t.Errorf("%s admits %s, want only what its private folder passes down", path, strings.Join(others, ", "))
		}
		if fspermtest.Protected(t, path) {
			t.Errorf("%s keeps an access list of its own, want it to take its folder's", path)
		}
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "x" {
		t.Errorf("read %s back = %q, %v; want the user to keep full access", file, data, err)
	}
}

func TestIsLink_TellsAJunctionFromAFolder(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bin")
	//nolint:gosec // G204: the paths are the test's own temporary folders
	if out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
	file := filepath.Join(dir, "token")
	write(t, file)

	for path, want := range map[string]bool{link: true, target: false, file: false} {
		got, err := fsperm.IsLink(path)
		if err != nil || got != want {
			t.Errorf("IsLink(%s) = %v, %v; want %v", path, got, err, want)
		}
	}
	if _, err := fsperm.IsLink(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("IsLink of a missing path = %v, want an error that matches fs.ErrNotExist", err)
	}
}
