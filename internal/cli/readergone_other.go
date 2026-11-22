//go:build !windows

package cli

import (
	"errors"
	"syscall"
)

// isReaderGone reports whether err says a write failed only because nothing
// reads the other end of the pipe any more.
func isReaderGone(err error) bool {
	return errors.Is(err, syscall.EPIPE)
}
