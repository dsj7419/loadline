// Package launch starts Loadline's server apart from the hook, status line
// or terminal that asks for it, and opens the dashboard in a browser.
package launch

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// SpawnDetached starts exe with args and leaves it running on its own: the
// server it starts outlives the hook or tap that starts it, keeps none of
// that process's standard streams, which Claude Code reads to their end,
// and never shows a console window. It runs in the user's home directory,
// so that it holds no project directory open. SpawnDetached returns once the
// program has started, without waiting for it.
func SpawnDetached(exe string, args ...string) error {
	cmd, err := serverCommand(exe, args...)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", exe, err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release %s: %w", exe, err)
	}
	return nil
}

// serverCommand is the command SpawnDetached runs.
func serverCommand(exe string, args ...string) (*exec.Cmd, error) {
	return homeCommand(serverAttr(), exe, args...)
}

// homeCommand returns the command that runs name with args in the user's
// home directory, started as attr says. Its standard streams are left nil,
// which gives the program the null device and passes on none of the
// caller's, and its environment is the caller's.
func homeCommand(attr *syscall.SysProcAttr, name string, args ...string) (*exec.Cmd, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find the home directory to run %s in: %w", name, err)
	}
	// name is this program's own executable or the browser the config
	// names. No context is given, since ending it would end the program too.
	cmd := exec.Command(name, args...) //nolint:gosec,noctx // G204: see above
	cmd.Dir = home
	cmd.SysProcAttr = attr
	return cmd, nil
}
