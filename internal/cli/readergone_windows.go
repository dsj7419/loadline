package cli

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isReaderGone reports whether err says a write failed only because nothing
// reads the other end of the pipe any more. Windows says so with
// ERROR_NO_DATA, the pipe is being closed, or ERROR_BROKEN_PIPE, the pipe
// has ended; syscall.EPIPE is a value of Go's own on Windows, which no write
// returns.
func isReaderGone(err error) bool {
	return errors.Is(err, windows.ERROR_NO_DATA) || errors.Is(err, windows.ERROR_BROKEN_PIPE)
}
