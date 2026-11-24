package cli_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/exetest"
)

// powerShellLine returns the line of text that PowerShell is to run: the one
// that ends in Stop-Process.
func powerShellLine(t *testing.T, text string) string {
	t.Helper()
	for line := range strings.Lines(text) {
		if line = strings.TrimSpace(line); strings.HasSuffix(line, "| Stop-Process") {
			return line
		}
	}
	t.Fatalf("instruction %q holds no line that ends in Stop-Process", text)
	return ""
}

// selectWith runs command in Windows PowerShell and returns what it printed,
// a word at a time. The command goes encoded, so that no quoting of the
// command line can change it.
func selectWith(t *testing.T, command string) []string {
	t.Helper()
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatalf("find Windows PowerShell: %v", err)
	}
	// -EncodedCommand takes UTF-16LE in base64.
	units := utf16.Encode([]rune(command))
	encoded := make([]byte, 0, 2*len(units))
	for _, u := range units {
		encoded = binary.LittleEndian.AppendUint16(encoded, u)
	}
	ctx, cancel := context.WithTimeout(t.Context(), hangBound)
	defer cancel()
	//nolint:gosec // G204: Windows PowerShell, running the selection the test checks
	cmd := exec.CommandContext(ctx, powershell, "-NoProfile", "-NonInteractive",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
	// Its progress and errors go to stderr, kept for a failure's message.
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("powershell: %v\n%s", err, stderr.String())
	}
	return strings.Fields(string(out))
}

// After install moves the running binary aside, Windows still reports the
// path the process started from, so the line that ends a server from 0.1.0
// goes by the port the server listens on. Run with Select-Object in place of
// Stop-Process, it selects that server, and nothing else from its folder:
// not a binary run from there that does not listen.
func TestOldServerInstruction_SelectsTheServerListeningOnThePort(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	exe := filepath.Join(bin, "loadline.exe")
	port := freePort(t)
	server := exetest.Serve(t, exe, port)
	// As install moves a running binary aside.
	if err := os.Rename(exe, exe+".old"); err != nil {
		t.Fatalf("move the running binary aside: %v", err)
	}
	exetest.Run(t, exe)

	line := powerShellLine(t, cli.OldServerInstruction("windows", bin, port))
	selection := strings.TrimSuffix(line, "| Stop-Process") + "| Select-Object -ExpandProperty Id"
	got := selectWith(t, selection)

	if want := []string{strconv.Itoa(server.PID())}; !slices.Equal(got, want) {
		t.Errorf("%s selects %q, want the server's process %q alone", selection, got, want)
	}
}
