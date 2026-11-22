//go:build !windows

package cli

import (
	"context"
	"os/exec"
)

// statusLineCommand runs command through sh, as a status line runs.
func statusLineCommand(ctx context.Context, command string) *exec.Cmd {
	// The command is the user's own status line, which Claude Code would
	// otherwise run itself.
	return exec.CommandContext(ctx, "sh", "-c", command) //nolint:gosec // G204: see above
}
