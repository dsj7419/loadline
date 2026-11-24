package cli

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// exitWatch tells whether one process has exited, through a handle on it.
type exitWatch struct {
	// handle is zero for a process that had exited before the watch began.
	handle windows.Handle
}

// openProcess is windows.OpenProcess. Tests replace it: no process at hand
// refuses to be opened for watching.
var openProcess = windows.OpenProcess

// watchExit starts to watch the process pid. A handle keeps the process's
// record, and its ID, from going to another process once it exits.
func watchExit(pid int) (*exitWatch, error) {
	handle, err := openProcess(windows.SYNCHRONIZE, false, uint32(pid)) //nolint:gosec // G115: a process ID is far below 2^32
	switch {
	case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
		// Windows knows no such process: it has exited already.
		return &exitWatch{}, nil
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		// It runs, as another account's or one more privileged.
		return nil, fmt.Errorf("open process %d: %w", pid, errors.Join(errCannotWatch, err))
	case err != nil:
		return nil, fmt.Errorf("open process %d: %w", pid, err)
	}
	return &exitWatch{handle: handle}, nil
}

func (w *exitWatch) exited() (bool, error) {
	if w.handle == 0 {
		return true, nil
	}
	event, err := windows.WaitForSingleObject(w.handle, 0)
	if err != nil {
		return false, err
	}
	return event == windows.WAIT_OBJECT_0, nil
}

func (w *exitWatch) close() {
	if w.handle != 0 {
		_ = windows.CloseHandle(w.handle)
	}
}
