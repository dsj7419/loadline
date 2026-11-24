package cli_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
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

// powerShellLine returns the line of the instruction that PowerShell is to
// run: the one after "End it once, in PowerShell:".
func powerShellLine(t *testing.T, text string) string {
	t.Helper()
	lines := slices.Collect(strings.Lines(text))
	for i, line := range lines {
		if strings.TrimSpace(line) == "End it once, in PowerShell:" && i+1 < len(lines) {
			return strings.TrimSpace(lines[i+1])
		}
	}
	t.Fatalf("instruction %q holds no PowerShell line", text)
	return ""
}

// stopProcessStandIn takes Stop-Process's place, as a function of that name
// does in PowerShell: it prints the ID of each process it is given, and
// ends none.
const stopProcessStandIn = "function Stop-Process { param([Parameter(ValueFromPipeline = $true)] $InputObject, [switch] $PassThru) " +
	"process { $InputObject.Id } }; "

// runPowerShell runs command in Windows PowerShell and returns what it
// printed, a word at a time. The command goes encoded, so that no quoting
// of the command line can change it.
func runPowerShell(t *testing.T, command string) []string {
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
	//nolint:gosec // G204: Windows PowerShell, running the line the test checks
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

// The line that ends a server from 0.1.0 goes by the port the server
// listens on, and by its name: 0.1.0's open and serve run the binary the
// user ran, often the one in the download's folder, not the one install put
// in bin, and after install moves a running binary aside Windows still
// reports the path it started from. Run as it is printed, with
// Stop-Process standing in, it selects that server alone, wherever it runs
// from, and says so when nothing of Loadline's listens there.
func TestOldServerInstruction_SelectsTheServerListeningOnThePort(t *testing.T) {
	// inBin starts a server from a bin folder under a folder named name, and
	// moves its binary aside as install does, with a copy run from there
	// that does not listen.
	inBin := func(name string) func(t *testing.T, port int) *exetest.Process {
		return func(t *testing.T, port int) *exetest.Process {
			t.Helper()
			exe := filepath.Join(t.TempDir(), name, "bin", "loadline.exe")
			server := exetest.Serve(t, exe, port)
			if err := os.Rename(exe, exe+".old"); err != nil {
				t.Fatalf("move the running binary aside: %v", err)
			}
			exetest.Run(t, exe)
			return server
		}
	}
	rows := map[string]func(t *testing.T, port int) *exetest.Process{
		"a server from bin": inBin("plain"),
		"a copy started outside bin, from the download": func(t *testing.T, port int) *exetest.Process {
			t.Helper()
			return exetest.Serve(t, filepath.Join(t.TempDir(), "Downloads", "loadline_0.1.0", "loadline.exe"), port)
		},
	}
	for _, name := range []string{"100%", "%TEMP%", "$HOME", "$(calc)", "wow!", "!!"} {
		rows["a server from bin under "+name] = inBin(name)
	}
	for name, start := range rows {
		t.Run(name, func(t *testing.T) {
			port := freePort(t)
			server := start(t, port)

			line := powerShellLine(t, cli.OldServerInstruction("windows", port))
			got := runPowerShell(t, stopProcessStandIn+line)

			if want := []string{strconv.Itoa(server.PID())}; !slices.Equal(got, want) {
				t.Errorf("%s\nselects %q, want the server's process %q alone", line, got, want)
			}
		})
	}

	// The port's listener is not Loadline's: nothing is ended, and the line
	// says so.
	t.Run("another program on the port", func(t *testing.T) {
		port := freePort(t)
		exetest.Serve(t, filepath.Join(t.TempDir(), "bin", "other.exe"), port)

		line := powerShellLine(t, cli.OldServerInstruction("windows", port))
		got := strings.Join(runPowerShell(t, stopProcessStandIn+line), " ")

		if want := fmt.Sprintf("Nothing was ended: no Loadline server listens on port %d.", port); got != want {
			t.Errorf("%s\nprints %q, want %q", line, got, want)
		}
	})
}
