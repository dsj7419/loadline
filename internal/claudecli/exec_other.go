//go:build !windows

package claudecli

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// hideWindow does nothing: only Windows gives a child process a console.
func hideWindow(*exec.Cmd) {}

// processTree is the process group that the child leads. Killing the child
// alone leaves the descendants running, and with a launcher script the real
// program is one.
type processTree struct{}

// newProcessTree starts cmd in a group of its own and makes its cancellation
// kill the whole group. Only Cancel, which Run calls when it gives up on the
// child, ends the group: what a child that exits by itself left running stays.
func newProcessTree(cmd *exec.Cmd) *processTree {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// A negative ID addresses the group, whose ID is the leader's.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return &processTree{}
}

// adopt does nothing: the child was in its own group from the start, so
// there is no window in which a descendant escapes it.
func (*processTree) adopt(*os.Process) {}

// close does nothing: a process group has no handle to release.
func (*processTree) close() {}
