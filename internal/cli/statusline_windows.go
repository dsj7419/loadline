package cli

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"os/exec"
	"syscall"
	"unicode/utf16"
)

// statusLineCommand runs command through Windows PowerShell. The command
// travels as -EncodedCommand, base64 of its UTF-16LE text, so that no layer
// of command-line quoting can change it on the way. The window PowerShell
// would open stays hidden.
func statusLineCommand(ctx context.Context, command string) *exec.Cmd {
	// The command is the user's own status line, which Claude Code would
	// otherwise run itself.
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", //nolint:gosec // G204: see above
		"-EncodedCommand", encodedCommand(command))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

// encodedCommand is command in the form -EncodedCommand takes.
func encodedCommand(command string) string {
	units := utf16.Encode([]rune(command))
	text := make([]byte, 0, 2*len(units))
	for _, unit := range units {
		text = binary.LittleEndian.AppendUint16(text, unit)
	}
	return base64.StdEncoding.EncodeToString(text)
}
