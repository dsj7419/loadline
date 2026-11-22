package cli

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isAddrInUse reports whether err says the address is taken. Winsock says so
// with WSAEADDRINUSE; syscall.EADDRINUSE is a value of Go's own on Windows,
// which no socket call returns.
func isAddrInUse(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE)
}
