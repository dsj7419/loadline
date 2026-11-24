package serverlock_test

import (
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// Whoever could open the lock file could hold it, and keep every server
// from starting. Its folder, made where it is missing, admits only the
// user, SYSTEM and Administrators, and so does the file, whatever the folder
// above passes down.
func TestAcquire_TheLockFileIsPrivate(t *testing.T) {
	above := t.TempDir()
	fspermtest.Permissive(t, above)
	path := filepath.Join(above, "data", "server.lock")

	release(t, acquire(t, path))

	for _, p := range []string{filepath.Dir(path), path} {
		if others := fspermtest.Others(t, p, fspermtest.Any); len(others) != 0 {
			t.Errorf("%s admits %v, want only the user, SYSTEM and Administrators", p, others)
		}
	}
}
