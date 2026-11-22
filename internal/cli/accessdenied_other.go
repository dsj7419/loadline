//go:build !windows

package cli

import (
	"errors"
	"syscall"
)

// accessDeniedReason says why a port refused with EACCES cannot be used.
const accessDeniedReason = "is not open to this program"

// isAccessDenied reports whether err says the system refuses this program
// the address.
func isAccessDenied(err error) bool {
	return errors.Is(err, syscall.EACCES)
}
