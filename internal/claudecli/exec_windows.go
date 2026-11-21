package claudecli

import (
	"os/exec"
	"syscall"
)

// caseInsensitiveEnv is whether environment variable names differ only by case
// on this platform.
const caseInsensitiveEnv = true

// createNoWindow is the CREATE_NO_WINDOW process creation flag.
const createNoWindow = 0x08000000

// hideWindow keeps claude, or the cmd.exe that runs its npm shim, from
// flashing a console window each time Loadline polls.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
