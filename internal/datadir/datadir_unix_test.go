//go:build !windows

package datadir_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/datadir"
)

func TestSecure_RefusesAFolderOthersCanWriteWithoutTheStickyBit(t *testing.T) {
	top := t.TempDir()
	shared := filepath.Join(top, "shared")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}

	err := datadir.Secure(filepath.Join(shared, "loadline"), top)

	if err == nil || !strings.Contains(err.Error(), shared+" lets group and others rename the folders in it") {
		t.Errorf("Secure = %v, want it refused, naming %s", err, shared)
	}
}

func TestSecure_CreatesTheDataDirectoryWithMode0700(t *testing.T) {
	home := filepath.Join(t.TempDir(), "loadline")

	if err := datadir.Secure(home, filepath.Dir(home)); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %#o, want 0700", got)
	}
}

func TestSecure_RefusesASymlinkAmongLoadlinesFiles(t *testing.T) {
	home := filepath.Join(t.TempDir(), "loadline")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(home, "token")
	if err := os.Symlink(filepath.Join(t.TempDir(), "theirs"), token); err != nil {
		t.Fatal(err)
	}

	err := datadir.Secure(home, filepath.Dir(home))

	if err == nil || !strings.Contains(err.Error(), token+" is a link") {
		t.Errorf("Secure = %v, want it refused, naming %s as a link", err, token)
	}
}

// A symlink named like a file Loadline writes before it puts it in place is
// no such file, and is refused like any link among Loadline's paths.
func TestSecure_RefusesASymlinkNamedLikeAFileBeingWritten(t *testing.T) {
	home := filepath.Join(t.TempDir(), "loadline")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".token-1234")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}

	err := datadir.Secure(home, filepath.Dir(home))

	if err == nil || !strings.Contains(err.Error(), link+" is a link") {
		t.Errorf("Secure = %v, want it refused, naming %s as a link", err, link)
	}
}
