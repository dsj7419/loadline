package cli

import "golang.org/x/sys/windows"

// processElevated reports whether this process runs with an administrator's
// full token, as one started from an elevated shell does.
func processElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}
