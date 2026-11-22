//go:build unix

package launch_test

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/dsj7419/loadline/internal/launch"
)

// wantServerHidden: Unix has no window to hide.
const wantServerHidden = false

// inspect reports whether this process leads a session of its own.
func inspect(r *helperReport) {
	sid, err := unix.Getsid(0)
	r.Detached = err == nil && sid == os.Getpid()
}

// isSharingViolation is false everywhere but Windows, where an open file can
// refuse other opens.
func isSharingViolation(error) bool { return false }

// A browser that has exited stays a zombie, holding its process ID, until
// the process that started it waits for it, and the server that opened it
// may run for hours. A zombie still answers signal 0, so only a reaped
// browser is gone to it.
func TestOpenBrowser_LeavesNoZombie(t *testing.T) {
	c := expectChild(t, false)
	if err := c.openBrowser(dashboardURL, launch.Browser{Pref: testBinary(t)}); err != nil {
		t.Fatalf("OpenBrowser: %v", err)
	}
	report := c.read(t)
	c.waitExit(t)

	if !c.waitReaped(t) {
		return
	}

	if err := unix.Kill(report.PID, 0); !errors.Is(err, unix.ESRCH) {
		t.Errorf("signal 0 to process %d once it was reaped gave %v, want ESRCH: it is still there, as a zombie if it has exited", report.PID, err)
	}
}
