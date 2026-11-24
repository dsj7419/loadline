//go:build !windows

package cli

import "os"

// processElevated reports whether this process runs as root.
func processElevated() bool {
	return os.Geteuid() == 0
}
