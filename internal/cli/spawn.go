package cli

import (
	"fmt"
	"os/exec"
)

// spawnDetached starts exe with args and leaves it running: the server it
// starts outlives the hook or tap that starts it. The child's standard
// streams are the null device, so nothing it prints reaches Claude Code.
func spawnDetached(exe string, args ...string) error {
	// exe is this program's own executable. No context is given, since
	// ending it would end the child too.
	cmd := exec.Command(exe, args...) //nolint:gosec,noctx // G204: see above
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", exe, err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release %s: %w", exe, err)
	}
	return nil
}
