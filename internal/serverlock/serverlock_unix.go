//go:build unix

package serverlock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// flock is unix.Flock. Tests replace it: no file system at hand refuses
// locks.
var flock = unix.Flock

// lock takes the exclusive lock on file, or returns errLocked when another
// open of it holds the lock. flock's locks belong to an open file, not to a
// process as fcntl's do, so two opens in one process exclude each other as
// two processes do.
func lock(file *os.File) error {
	for {
		err := flock(fd(file), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case errors.Is(err, unix.EINTR):
			// A signal came first; it says nothing of the lock.
		case errors.Is(err, unix.EWOULDBLOCK):
			return errLocked
		default:
			return lockError(err)
		}
	}
}

func unlock(file *os.File) error {
	return unix.Flock(fd(file), unix.LOCK_UN)
}

func fd(file *os.File) int { return int(file.Fd()) }

// lockError is err, which flock gave, marked ErrUnsupported when it says
// the file system takes no lock at all.
func lockError(err error) error {
	if errors.Is(err, unix.ENOLCK) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTSUP) {
		return errors.Join(ErrUnsupported, err)
	}
	return err
}
