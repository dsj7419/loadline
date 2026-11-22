//go:build !windows

package install

// Renames do not fail because another process has a file open here, so
// nothing is worth waiting out.

func isSharingViolation(error) bool { return false }

func isHeld(error) bool { return false }
