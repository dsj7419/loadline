package cli

import (
	"errors"

	"golang.org/x/sys/windows"
)

// accessDeniedReason says why a port refused with WSAEACCES cannot be used:
// it lies in a range Windows reserves (for Hyper-V or WinNAT), or another
// program holds it with an exclusive bind.
const accessDeniedReason = "is reserved by Windows or held exclusively by another program"

// isAccessDenied reports whether err says the system refuses this program
// the address.
func isAccessDenied(err error) bool {
	return errors.Is(err, windows.WSAEACCES)
}
