package fsperm_test

import (
	"errors"
	"io/fs"
	"os"
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
