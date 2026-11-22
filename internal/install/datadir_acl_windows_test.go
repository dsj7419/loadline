package install_test

// Under LOADLINE_HOME on a drive other than the profile's, such as D:\, whose
// default access list lets every signed-in user modify what is created below
// it, what Loadline writes must still admit only the user: the token lets its
// reader feed and read the dashboard, and every hook runs the installed
// binary.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
	"github.com/dsj7419/loadline/internal/install"
)

// sharedDataDir returns where a fresh data directory goes under a folder
// that lets every signed-in user modify what is below it.
func sharedDataDir(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	fspermtest.Permissive(t, parent)
	return filepath.Join(parent, "loadline")
}

func TestEnsureToken_TheDataDirAdmitsOnlyTheUser(t *testing.T) {
	dir := sharedDataDir(t)
	tokenFile := filepath.Join(dir, "token")

	if _, err := config.EnsureToken(tokenFile); err != nil {
		t.Fatal(err)
	}
	// What Loadline writes there later, the spool, the log and the history,
	// takes the access list the data directory passes down.
	later := []string{
		filepath.Join(dir, "spool", "event.json"),
		filepath.Join(dir, "loadline.log"),
		filepath.Join(dir, "loadline.db"),
	}
	for _, path := range later {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, path := range append([]string{dir, tokenFile, filepath.Join(dir, "spool")}, later...) {
		if others := fspermtest.Others(t, path, fspermtest.Read); len(others) > 0 {
			t.Errorf("%s can be read by %s, want only the user", path, strings.Join(others, ", "))
		}
	}
}

func TestSelfInstall_TheBinaryIsWritableOnlyByTheUser(t *testing.T) {
	dir := sharedDataDir(t)
	src, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	exe, err := install.SelfInstall(src, dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{filepath.FromSlash(exe), filepath.Dir(filepath.FromSlash(exe))} {
		if others := fspermtest.Others(t, path, fspermtest.Write); len(others) > 0 {
			t.Errorf("%s, which every hook runs, can be modified by %s, want only the user", path, strings.Join(others, ", "))
		}
	}
}

func TestSelfInstall_AnUpgradeMakesAnOlderBinDirPrivate(t *testing.T) {
	dir := sharedDataDir(t)
	old := filepath.Join(dir, "bin", "loadline.exe")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	// As an earlier Loadline left it, with what the parent passes down.
	if err := os.WriteFile(old, []byte("an older loadline"), 0o700); err != nil {
		t.Fatal(err)
	}
	src, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := install.SelfInstall(src, dir); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Dir(old)
	entries, err := os.ReadDir(bin)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{bin}
	for _, entry := range entries {
		paths = append(paths, filepath.Join(bin, entry.Name()))
	}
	for _, path := range paths {
		if others := fspermtest.Others(t, path, fspermtest.Write); len(others) > 0 {
			t.Errorf("%s can be modified by %s, want only the user", path, strings.Join(others, ", "))
		}
	}
}
