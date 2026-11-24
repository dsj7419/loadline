//go:build unix

package serverlock_test

import (
	"os"
	"path/filepath"
	"testing"
)

// Whoever could open the lock file could hold it, and keep every server
// from starting. Its folder, made where it is missing, has mode 0700, and
// the file 0600, even one another program left open to others.
func TestAcquire_TheLockFileIsPrivate(t *testing.T) {
	t.Run("made", func(t *testing.T) {
		path := lockPath(t)

		release(t, acquire(t, path))

		assertMode(t, filepath.Dir(path), 0o700)
		assertMode(t, path, 0o600)
	})
	t.Run("left open", func(t *testing.T) {
		path := lockPath(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}

		release(t, acquire(t, path))

		assertMode(t, path, 0o600)
	})
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s has mode %#o, want %#o", path, got, want)
	}
}
