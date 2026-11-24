package serverlock

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// A file system that takes no lock, as some network shares do not, answers
// LockFileEx with one of these; nothing else is taken for it.
func TestLockError_TellsAFileSystemThatTakesNoLock(t *testing.T) {
	for _, errno := range []windows.Errno{windows.ERROR_INVALID_FUNCTION, windows.ERROR_NOT_SUPPORTED} {
		if err := lockError(errno); !errors.Is(err, ErrUnsupported) || !errors.Is(err, errno) {
			t.Errorf("lockError(%v) = %v, want ErrUnsupported, keeping the cause", errno, err)
		}
	}
	for _, errno := range []windows.Errno{windows.ERROR_ACCESS_DENIED, windows.ERROR_INVALID_HANDLE} {
		if err := lockError(errno); errors.Is(err, ErrUnsupported) {
			t.Errorf("lockError(%v) = %v, want no ErrUnsupported", errno, err)
		}
	}
}

// A lock LockFileEx refuses as unsupported reaches Acquire's caller as
// ErrUnsupported.
func TestAcquire_ReportsAFileSystemThatTakesNoLock(t *testing.T) {
	previous := lockFileEx
	lockFileEx = func(windows.Handle, uint32, uint32, uint32, uint32, *windows.Overlapped) error {
		return windows.ERROR_NOT_SUPPORTED
	}
	t.Cleanup(func() { lockFileEx = previous })

	lock, err := Acquire(filepath.Join(t.TempDir(), "server.lock"), 0, nil)

	if lock != nil || !errors.Is(err, ErrUnsupported) {
		t.Errorf("Acquire = %v, %v; want ErrUnsupported", lock, err)
	}
}
