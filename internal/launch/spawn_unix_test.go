//go:build unix

package launch_test

import (
	"os"

	"golang.org/x/sys/unix"
)

// wantServerHidden: Unix has no window to hide.
const wantServerHidden = false

// inspect reports whether this process leads a session of its own.
func inspect(r *helperReport) {
	sid, err := unix.Getsid(0)
	r.Detached = err == nil && sid == os.Getpid()
}
