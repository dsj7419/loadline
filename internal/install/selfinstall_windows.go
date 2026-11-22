package install

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isSharingViolation reports whether err says another process has the file
// open in a way that forbids renaming it, which antivirus scanners and the
// loader of a starting process do for a moment.
func isSharingViolation(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

// isHeld reports whether err may come from another process holding a file
// for a moment. Scanners can make a rename fail with access denied too.
func isHeld(err error) bool {
	return isSharingViolation(err) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
