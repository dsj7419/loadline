package cli

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"os/exec"
	"syscall"
	"unicode/utf16"
)

// statusLineCommand runs command through PowerShell. The command travels as
// -EncodedCommand, base64 of its UTF-16LE text, so that no layer of
// command-line quoting can change it on the way. The window PowerShell would
// open stays hidden.
func statusLineCommand(ctx context.Context, command string) *exec.Cmd {
	// The command is the user's own status line, which Claude Code would
	// otherwise run itself.
	cmd := exec.CommandContext(ctx, powerShell(exec.LookPath), "-NoProfile", "-NonInteractive", //nolint:gosec // G204: see above
		"-EncodedCommand", encodedCommand(command))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

// powerShell names the PowerShell to run a status line with: PowerShell 7
// when lookPath finds it, since it also runs what was written for Windows
// PowerShell 5.1 but not the other way round, and otherwise Windows
// PowerShell, which every Windows has.
func powerShell(lookPath func(file string) (string, error)) string {
	if path, err := lookPath("pwsh.exe"); err == nil {
		return path
	}
	return "powershell.exe"
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
