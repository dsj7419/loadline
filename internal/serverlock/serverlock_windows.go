package serverlock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFileEx is windows.LockFileEx. Tests replace it: no file system at
// hand refuses locks.
var lockFileEx = windows.LockFileEx

// lock takes the exclusive lock on the first byte of file, or returns
// errLocked when another handle holds it. A lock is a handle's own, so two
// opens in one process exclude each other as two processes do.
func lock(file *os.File) error {
	var whole windows.Overlapped
	err := lockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &whole)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLocked
	}
	return lockError(err)
}

// unlock lets go of the lock at once. Windows would let go of it when the
// handle closes too, but only once it gets to it.
func unlock(file *os.File) error {
	var whole windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &whole)
}

// lockError is err, which LockFileEx gave, marked ErrUnsupported when it
// says the file system takes no lock at all.
func lockError(err error) error {
	if errors.Is(err, windows.ERROR_INVALID_FUNCTION) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) {
		return errors.Join(ErrUnsupported, err)
	}
	return err
}
