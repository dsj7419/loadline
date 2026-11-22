package claudecli

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// createNoWindow is the CREATE_NO_WINDOW process creation flag.
const createNoWindow = 0x08000000

// hideWindow keeps claude, or the cmd.exe that runs its npm shim, from
// flashing a console window each time Loadline polls.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// processTree is a job object that holds a child and everything it starts.
// Killing the child alone leaves the descendants running, and with an npm
// shim the real program is one.
//
// The job does not set JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE. Closing the job
// when the call is over would then also kill what claude started on purpose
// and left running, such as the daemon that supervises background sessions.
// Only Cancel, which Run calls when it gives up on the child, ends the job's
// processes.
//
// If the job cannot be created, or the child cannot join it, processTree
// degrades to killing the child alone.
type processTree struct {
	job windows.Handle
}

// newProcessTree makes cmd's cancellation kill every process in its job.
func newProcessTree(cmd *exec.Cmd) *processTree {
	tree := &processTree{}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return tree
	}
	tree.job = job

	cmd.Cancel = func() error {
		// The job holds the child once adopted, but kill it directly too, for
		// the moment before it has joined and for a child that could not.
		_ = windows.TerminateJobObject(job, 1)
		return cmd.Process.Kill()
	}
	return tree
}

// adopt moves the started process into the job. It is best effort: a process
// that has exited cannot join, and then there is nothing left to stop.
//
// The child runs for a moment before it joins, and a descendant it starts in
// that moment is outside the job and survives a kill. The window is small for
// a program that is still loading. Closing it would take starting the child
// suspended, which os/exec cannot do.
//
// Process IDs are not reused while cmd has not been waited for, so the ID
// still names the child.
func (t *processTree) adopt(process *os.Process) {
	if t.job == 0 {
		return
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(process.Pid)) //nolint:gosec // G115: a process ID is far below 2^32
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	_ = windows.AssignProcessToJobObject(t.job, handle)
}

// close releases the job handle. The processes in the job keep running.
func (t *processTree) close() {
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
	}
}
