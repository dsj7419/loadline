package cli_test

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

// exited reports whether the process pid has exited: Windows knows no such
// process, or the one it knows has ended.
func exited(t *testing.T, pid int) bool {
	t.Helper()
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid)) //nolint:gosec // G115: a process ID is far below 2^32
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return true
	}
	if err != nil {
		t.Fatalf("open process %d: %v", pid, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	event, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		t.Fatalf("wait for process %d: %v", pid, err)
	}
	return event == windows.WAIT_OBJECT_0
}
