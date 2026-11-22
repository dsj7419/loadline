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
