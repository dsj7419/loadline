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

// bashFormCases are status-line commands, and the command install makes of
// each: plain commands follow the pipe as they are, and anything else runs
// in a group so the pipe still feeds the whole of it.
var bashFormCases = []struct {
	original string
	grouped  bool
}{
	{"cat", false},
	{"bash ~/.claude/statusline.sh", false},
	{`printf '%s' "$(cat)"`, true},
	{"cd / && cat", true},
	{"read line; echo \"got $line\"", true},
	{"read line\necho \"got $line\"", true},
	{"export GREETING=hi; cat; echo $GREETING", true},
	{"! false && cat", true},
	{"time cat", true},
	{"if true; then cat; fi", true},
	{"cat # a trailing comment", true},
	{"for word in a b; do echo $word; done; cat", true},
	{"cat <<'EOF'\nfrom a here-document\nEOF", true},
}

func TestBashForm_GroupsAllButPlainCommands(t *testing.T) {
	for _, tc := range bashFormCases {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, tc.original) + `}}`)

		out, _, _ := plan(t, settings, options(install.ShellBash))

		want := `"` + goldenExe + `" tap | ` + tc.original
		if tc.grouped {
			want = `"` + goldenExe + `" tap | { ` + tc.original + "\n}"
		}
		if got := statusCommand(t, out); got != want {
			t.Errorf("%q became %q, want %q", tc.original, got, want)
		}
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
