//go:build !windows

package claudecli

import (
	"errors"
	"syscall"
)

// processAlive reports whether the process with the given ID exists. A
// process that exists but belongs to someone else counts as alive.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
