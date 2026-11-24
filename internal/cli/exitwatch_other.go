//go:build !windows

package cli

import (
	"errors"
	"syscall"
)

// exitWatch tells whether one process has exited, by signal 0, which only
// checks that the process exists.
type exitWatch struct {
	pid int
}

func watchExit(pid int) (*exitWatch, error) { return &exitWatch{pid: pid}, nil }

func (w *exitWatch) exited() (bool, error) {
	err := syscall.Kill(w.pid, 0)
	switch {
	case errors.Is(err, syscall.ESRCH):
		return true, nil
	case err == nil, errors.Is(err, syscall.EPERM):
		// It runs, as another user's if it may not be signaled.
		return false, nil
	default:
		return false, err
	}
}

func (w *exitWatch) close() {}
