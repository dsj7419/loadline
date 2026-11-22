//go:build !windows

package cli

import (
	"errors"
	"syscall"
)

// isAddrInUse reports whether err says the address is taken.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
