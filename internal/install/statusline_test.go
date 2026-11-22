package install_test

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	wrappedForm        // in a subshell within one, so the pipe still feeds all of it
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
	{`printf '%s' "$(cat)"`, wrappedForm},
	{"cd / && cat", wrappedForm},
	{"cat | tr a-z A-Z", wrappedForm},
	{"{ cat; echo; }", wrappedForm},
	{"(cat; echo)", wrappedForm},
	{`FOO=bar bash -c 'echo "$FOO $(cat)"'`, wrappedForm},
	{"read line; echo \"got $line\"", wrappedForm},
	{"read line\necho \"got $line\"", wrappedForm},
	{"export GREETING=hi; cat; echo $GREETING", wrappedForm},
	{"! false && cat", wrappedForm},
	{"time cat", wrappedForm},
	{"if true; then cat; fi", wrappedForm},
	{"cat # a trailing comment", wrappedForm},
	{"cat #", wrappedForm},
	{"for word in a b; do echo $word; done; cat", wrappedForm},
	{`cat <<< "a here-string"`, wrappedForm},
	{"cat; exit 4", wrappedForm},
	{"exit 3", plainForm},
	{"exec cat", plainForm},
	{"FOO=1 cat", plainForm},
	{"cat\n", plainForm},
	{"echo 'a\"b'", plainForm},
	{`echo a\ `, wrappedForm},
	{"true \\\n&& cat", wrappedForm},
	{"case x in x) cat;; esac", wrappedForm},
	{"cat 2>&1 1>/dev/null; echo done", wrappedForm},
	{"set -o pipefail; false | cat", wrappedForm},
	{`printf '%s\n' 'a\\b'`, wrappedForm},
	// An EXIT trap the original sets runs when the original ends. bash 3.2,
	// macOS's /bin/bash and /bin/sh, skips it for a group in a pipeline.
	{"trap 'echo bye' EXIT; cat", wrappedForm},
	{"trap 'echo bye' 0; cat", wrappedForm},
	{"cleanup() { trap 'echo bye' EXIT; }; cleanup; cat", wrappedForm},
	{"trap 'echo bye' EXIT; cat; exit 3", wrappedForm},
	{"trap 'echo bye' EXIT; trap - EXIT; cat", wrappedForm},
	// On its own, a background command reads its input from /dev/null; bash
	// 4.4 and later give it the pipe instead inside a subshell the pipe feeds.
	{"trap 'echo bye' EXIT; cat &\nwait", wrappedForm},
	// A here-document would take the wrap's closing line as its own when it
	// is not terminated, so none is wrapped.
	{"cat <<'EOF'\nfrom a here-document\nEOF", leftAlone},
	{"cat <<EOF\nan unterminated here-document", leftAlone},
	{"cat <<-EOF\n\tindented\n\tEOF", leftAlone},
	// Nothing to run, and an empty subshell is a syntax error.
	{"# just a comment", leftAlone},
	{"\n  # one\n\n\t# two\n", leftAlone},
	// A trailing backslash, escaped or not, can turn the wrap's closing line
	// into a continuation once Windows and Git Bash have passed the command
	// along.
	{`echo a\`, leftAlone},
	{`echo a\\`, leftAlone},
}

func TestBashForm_WrapsAllButPlainCommands(t *testing.T) {
	for _, tc := range bashFormCases {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, tc.original) + `}}`)

		out, _, _ := plan(t, settings, options(install.ShellBash))

		want := map[int]string{
			plainForm:   `"` + goldenExe + `" tap | ` + tc.original,
			wrappedForm: `"` + goldenExe + `" tap | ( ( ` + tc.original + "\n) )",
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
// copies stdin to stdout as tap does, and expects the same output. Starting
// a shell per case is slow, so with the race detector on, which these
// shells do not run under anyway, or with -short, it runs one shell and
// every third case; TestBashForm_WrapsAllButPlainCommands still checks
// every command.
func TestBashForm_KeepsTheOriginalsMeaning(t *testing.T) {
	shellPaths := availableShells()
	if len(shellPaths) == 0 {
		t.Skip("no POSIX shell here; TestBashForm_WrapsAllButPlainCommands checks the commands")
	}
	shellPaths = shellPaths[:scaled(len(shellPaths), 1)]
	t.Logf("shells: %q", shellPaths)
	tapStandIn := filepath.Join(t.TempDir(), "loadline")
	if err := os.WriteFile(tapStandIn, []byte("#!/bin/sh\nexec cat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	o := options(install.ShellBash)
	o.Exe = filepath.ToSlash(tapStandIn)
	env := standInHome(t, shellPaths)

	for i := 0; i < len(bashFormCases); i += scaled(1, 3) {
		tc := bashFormCases[i]
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, tc.original) + `}}`)
		out, _, _ := plan(t, settings, o)
		wrapped := statusCommand(t, out)

		for _, shell := range shellPaths {
			want, wantCode := runShell(t, shell, env, tc.original)
			got, gotCode := runShell(t, shell, env, wrapped)

			if got != want || gotCode != wantCode {
				t.Errorf("%s: %q printed %q (exit %d) behind tap, %q (exit %d) on its own",
					shell, tc.original, got, gotCode, want, wantCode)
			}
		}
	}
}

// availableShells finds bash and sh. On Windows that means Git for Windows,
// never the bash.exe in System32, which starts WSL. Elsewhere it takes the
// system's /bin/bash and /bin/sh first, the shells users have, and then those
// on PATH, which may be newer: macOS ships bash 3.2 and a runner may put a
// later bash first on PATH. A shell found twice runs once.
func availableShells() []string {
	var candidates []string
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("ProgramFiles"); dir != "" {
			candidates = append(candidates,
				filepath.Join(dir, "Git", "bin", "bash.exe"), filepath.Join(dir, "Git", "bin", "sh.exe"))
		}
	} else {
		candidates = append(candidates, "/bin/bash", "/bin/sh")
		for _, name := range []string{"bash", "sh"} {
			if path, err := exec.LookPath(name); err == nil {
				candidates = append(candidates, path)
			}
		}
	}
	var found []string
	seen := make(map[string]bool)
	for _, c := range candidates {
		resolved, err := filepath.EvalSymlinks(c)
		if err != nil {
			continue
		}
		// bash keeps to POSIX when it runs as sh, so the name it runs under
		// is part of which shell it is.
		shell := resolved + "\x00" + filepath.Base(c)
		if !seen[shell] {
			seen[shell] = true
			found = append(found, c)
		}
	}
	return found
}

// standInHome returns the environment for the shells under test, with HOME
// in a temporary directory. A command such as bash ~/.claude/statusline.sh
// then runs a stand-in that copies its input, never the user's own status
// line. It stops the test before anything runs unless every shell sees that
// home.
func standInHome(t *testing.T, shells []string) []string {
	t.Helper()

	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	if err := os.Mkdir(claude, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"statusline.sh": "cat\n", "stand-in": ""} {
		if err := os.WriteFile(filepath.Join(claude, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// exec keeps the last of duplicate variables, so this HOME wins.
	env := append(os.Environ(), "HOME="+home)
	for _, shell := range shells {
		if _, code := runShell(t, shell, env, "test -e ~/.claude/stand-in"); code != 0 {
			t.Fatalf("%s does not take HOME from the environment, so it would run the real ~/.claude", shell)
		}
	}
	return env
}

// runShell runs command with shell -c in env, feeding it a status-line
// payload, and returns what it printed and its exit code.
func runShell(t *testing.T, shell string, env []string, command string) (string, int) {
	t.Helper()

	got := runInShell(t, env, strings.NewReader("{\"model\":\"opus\"}\n"), shell, "-c", command)
	return got.stdout, got.code
}

// shellRun is what a shell printed and how it exited.
type shellRun struct {
	stdout, stderr string
	code           int
}

// runInShell runs shell with args in env and input on its stdin.
func runInShell(t *testing.T, env []string, input io.Reader, shell string, args ...string) shellRun {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, args...) //nolint:gosec // G204: runs the shell under test on a command the test built
	// A background command can outlive a shell killed at the timeout and
	// hold its output open; this stops Wait from waiting on it.
	cmd.WaitDelay = time.Second
	cmd.Env = env
	cmd.Stdin = input
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%s %q timed out", shell, args)
	}
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("%s %q: %v", shell, args, err)
		}
		code = exitErr.ExitCode()
	}
	return shellRun{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// scaled returns full, or reduced when the race detector or -short would
// make the full count too slow for CI.
func scaled(full, reduced int) int {
	if raceEnabled || testing.Short() {
		return reduced
	}
	return full
}
