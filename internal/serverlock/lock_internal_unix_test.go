//go:build unix

package serverlock

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// A file system that takes no lock, as some network shares do not, answers
// flock with one of these; nothing else is taken for it.
func TestLockError_TellsAFileSystemThatTakesNoLock(t *testing.T) {
	for _, errno := range []unix.Errno{unix.ENOLCK, unix.EOPNOTSUPP, unix.ENOTSUP} {
		if err := lockError(errno); !errors.Is(err, ErrUnsupported) || !errors.Is(err, errno) {
			t.Errorf("lockError(%v) = %v, want ErrUnsupported, keeping the cause", errno, err)
		}
	}
	for _, errno := range []unix.Errno{unix.EBADF, unix.EINVAL} {
		if err := lockError(errno); errors.Is(err, ErrUnsupported) {
			t.Errorf("lockError(%v) = %v, want no ErrUnsupported", errno, err)
		}
	}
}

// A lock flock refuses as unsupported reaches Acquire's caller as
// ErrUnsupported.
func TestAcquire_ReportsAFileSystemThatTakesNoLock(t *testing.T) {
	previous := flock
	flock = func(int, int) error { return unix.ENOLCK }
	t.Cleanup(func() { flock = previous })

	lock, err := Acquire(filepath.Join(t.TempDir(), "server.lock"), 0, nil)

	if lock != nil || !errors.Is(err, ErrUnsupported) {
		t.Errorf("Acquire = %v, %v; want ErrUnsupported", lock, err)
	}
}
