package install_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/install"
)

// The forms install gives a user's status-line command under bash.
const (
	plainForm   = iota // after the pipe as it is
	groupedForm        // in a group, so the pipe still feeds all of it
	leftAlone          // not tapped at all
)

// bashFormCases are status-line commands and the form install puts each in.
var bashFormCases = []struct {
	original string
	form     int
}{
	{"cat", plainForm},
	{"   cat", plainForm},
	{"bash ~/.claude/statusline.sh", plainForm},
	{`printf '%s' "$(cat)"`, groupedForm},
	{"cd / && cat", groupedForm},
	{"cat | tr a-z A-Z", groupedForm},
	{"{ cat; echo; }", groupedForm},
	{`FOO=bar bash -c 'echo "$FOO $(cat)"'`, groupedForm},
	{"read line; echo \"got $line\"", groupedForm},
	{"read line\necho \"got $line\"", groupedForm},
	{"export GREETING=hi; cat; echo $GREETING", groupedForm},
	{"! false && cat", groupedForm},
	{"time cat", groupedForm},
	{"if true; then cat; fi", groupedForm},
	{"cat # a trailing comment", groupedForm},
	{"for word in a b; do echo $word; done; cat", groupedForm},
	{"cat <<'EOF'\nfrom a here-document\nEOF", groupedForm},
	// A trailing backslash, escaped or not, can turn the group's closing
	// line into a continuation once Windows and Git Bash have passed the
	// command along.
	{`echo a\`, leftAlone},
	{`echo a\\`, leftAlone},
}

func TestBashForm_GroupsAllButPlainCommands(t *testing.T) {
	for _, tc := range bashFormCases {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, tc.original) + `}}`)

		out, _, _ := plan(t, settings, options(install.ShellBash))

		want := map[int]string{
			plainForm:   `"` + goldenExe + `" tap | ` + tc.original,
			groupedForm: `"` + goldenExe + `" tap | { ` + tc.original + "\n}",
			leftAlone:   tc.original,
		}[tc.form]
		if got := statusCommand(t, out); got != want {
			t.Errorf("%q became %q, want %q", tc.original, got, want)
		}
	}
}

func TestUsesPowerShellWrapper(t *testing.T) {
	own := []byte(`{"statusLine": {"type": "command", "command": "my-status"}}`)
	wrapped, _, _ := plan(t, own, options(install.ShellPowerShell))
	printing, _, _ := plan(t, []byte(`{}`), options(install.ShellPowerShell))
	piped, _, _ := plan(t, own, options(install.ShellBash))

	tests := []struct {
		name     string
		settings []byte
		want     bool
	}{
		{name: "the PowerShell form around the user's command", settings: wrapped, want: true},
		{name: "the same after a byte-order mark", settings: append([]byte(utf8BOM), wrapped...), want: true},
		{name: "the PowerShell form that prints its own line", settings: printing},
		{name: "the bash pipe form", settings: piped},
		{name: "the user's own command", settings: own},
		{name: "no status line", settings: []byte(`{}`)},
		{
			name:     "a PowerShell form changed by hand",
			settings: []byte(`{"statusLine": {"command": "& \"` + goldenExe + `\" tap --run-b64 'not base64'"}}`),
		},
		{name: "invalid JSON", settings: []byte(`{"statusLine": `)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := install.UsesPowerShellWrapper(tt.settings, goldenExe); got != tt.want {
				t.Errorf("UsesPowerShellWrapper = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBashForm_KeepsTheOriginalsMeaning runs each original and the command
// install makes of it through real shells, with a stand-in for tap that
// copies stdin to stdout as tap does, and expects the same output.
func TestBashForm_KeepsTheOriginalsMeaning(t *testing.T) {
	shellPaths := availableShells()
	if len(shellPaths) == 0 {
		t.Skip("no POSIX shell here; TestBashForm_GroupsAllButPlainCommands checks the commands")
	}
	tapStandIn := filepath.Join(t.TempDir(), "loadline")
	if err := os.WriteFile(tapStandIn, []byte("#!/bin/sh\nexec cat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	o := options(install.ShellBash)
	o.Exe = filepath.ToSlash(tapStandIn)

	for _, tc := range bashFormCases {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, tc.original) + `}}`)
		out, _, _ := plan(t, settings, o)
		wrapped := statusCommand(t, out)

		for _, shell := range shellPaths {
			want, wantCode := runShell(t, shell, tc.original)
			got, gotCode := runShell(t, shell, wrapped)

			if got != want || gotCode != wantCode {
				t.Errorf("%s: %q printed %q (exit %d) behind tap, %q (exit %d) on its own",
					filepath.Base(shell), tc.original, got, gotCode, want, wantCode)
			}
		}
	}
}

// availableShells finds bash and sh. On Windows that means Git for Windows,
// never the bash.exe in System32, which starts WSL.
func availableShells() []string {
	var candidates []string
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("ProgramFiles"); dir != "" {
			candidates = append(candidates,
				filepath.Join(dir, "Git", "bin", "bash.exe"), filepath.Join(dir, "Git", "bin", "sh.exe"))
		}
	} else {
		for _, name := range []string{"bash", "sh"} {
			if path, err := exec.LookPath(name); err == nil {
				candidates = append(candidates, path)
			}
		}
	}
	var found []string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil { //nolint:gosec // G703: probes where the shells under test are installed
			found = append(found, c)
		}
	}
	return found
}

// runShell runs command with shell -c, feeding it a status-line payload, and
// returns what it printed and its exit code.
func runShell(t *testing.T, shell, command string) (string, int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-c", command) //nolint:gosec // G204: runs the shell under test on a command the test built
	cmd.Stdin = strings.NewReader("{\"model\":\"opus\"}\n")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%s -c %q timed out", shell, command)
	}
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("%s -c %q: %v", shell, command, err)
		}
		code = exitErr.ExitCode()
	}
	return stdout.String(), code
}
