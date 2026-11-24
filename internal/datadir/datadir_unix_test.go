//go:build !windows

package datadir_test

import (
	"io/fs"
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

// A data directory that is itself a symbolic link is refused, naming where
// it leads: making it private would change the mode of the folder it leads
// to, but Loadline's files would then be wherever the link points next.
func TestSecure_RefusesADataDirectoryThatIsASymlink(t *testing.T) {
	target := t.TempDir()
	home := filepath.Join(t.TempDir(), "loadline")
	if err := os.Symlink(target, home); err != nil {
		t.Fatal(err)
	}

	err := datadir.Secure(home, filepath.Dir(home))

	if want := home + " is a link to " + target; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Secure = %v, want it refused, starting %q", err, want)
	}
}

// Doctor says why install would refuse to make a data directory that does
// not exist yet, in the words install would use.
func TestCheck_SaysWhyInstallWouldRefuseADataDirectoryNotMadeYet(t *testing.T) {
	top := t.TempDir()
	shared := filepath.Join(top, "shared")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(shared, "apps", "loadline")
	refused := datadir.Secure(home, top)
	if refused == nil {
		t.Fatalf("Secure(%s) succeeded, want it refused", home)
	}

	got, err := datadir.Check(home, top)

	if want := home + " does not exist yet, and loadline install would refuse to make it: " + refused.Error(); err != nil || got != want {
		t.Errorf("Check = %q, %v; want %q", got, err, want)
	}
}

// sharedFile returns a file that group and others may read, and a check that
// its mode is as it was.
func sharedFile(t *testing.T) (path string, unchanged func(*testing.T)) {
	t.Helper()
	const shared fs.FileMode = 0o644
	path = filepath.Join(t.TempDir(), "shared.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, shared); err != nil {
		t.Fatal(err)
	}
	return path, func(t *testing.T) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != shared {
			t.Errorf("%s, outside the data directory, has mode %v; want %v as it was", path, info.Mode().Perm(), shared)
		}
	}
}

// A file of Loadline's with a second name elsewhere is refused like a link.
func TestSecure_RefusesAFileOfLoadlinesWithAnotherName(t *testing.T) {
	home := filepath.Join(t.TempDir(), "loadline")
	if err := os.MkdirAll(filepath.Join(home, "spool"), 0o700); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(t.TempDir(), "shared.txt")
	if err := os.WriteFile(elsewhere, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	event := filepath.Join(home, "spool", "1-0000000b.json")
	if err := os.Link(elsewhere, event); err != nil {
		t.Fatal(err)
	}

	err := datadir.Secure(home, filepath.Dir(home))

	if want := event + " has another name"; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Secure = %v, want it refused, starting %q", err, want)
	}
}
