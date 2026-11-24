//go:build !windows

package cli_test

import (
	"errors"
	"syscall"
	"testing"
)

// exited reports whether the process pid has exited, and been reaped: the
// system knows no such process.
func exited(t *testing.T, pid int) bool {
	t.Helper()
	err := syscall.Kill(pid, 0)
	switch {
	case errors.Is(err, syscall.ESRCH):
		return true
	case err == nil, errors.Is(err, syscall.EPERM):
		return false
	default:
		t.Fatalf("signal process %d: %v", pid, err)
		return false
	}
}
