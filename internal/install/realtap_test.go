package install_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/install"
)

// nonReaders are status-line commands that never read their input, so the
// tap in front of them writes into a pipe whose reader has gone.
var nonReaders = []string{
	"true",
	"echo hi",
	"exit 3",
	"set -e; trap 'echo bye' EXIT; false; cat",
	"sleep 0.2; echo late",
	"echo one\necho two >&2\nexit 5",
}

// TestBashForm_RealTapBeforeOriginalsThatNeverRead runs loadline's own tap,
// as this test binary in its helper mode, in the command install makes of
// each original, through real shells. The input is larger than a pipe holds,
// so the tap's echo meets the closed pipe rather than fitting in its buffer.
// Behind the tap each original must print the same on stdout and on stderr,
// and exit the same, as on its own; under pipefail too, where the tap's exit
// code would otherwise count. With the race detector on, or with -short, it
// runs one shell.
func TestBashForm_RealTapBeforeOriginalsThatNeverRead(t *testing.T) {
	shells := availableShells()
	if len(shells) == 0 {
		t.Skip("no POSIX shell here")
	}
	shells = shells[:scaled(len(shells), 1)]
	t.Logf("shells: %q", shells)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	o := options(install.ShellBash)
	o.Exe = filepath.ToSlash(exe)
	// The tap sends nothing of an input without a session, so it needs no
	// server; the home it is given must stay empty.
	loadlineHome := t.TempDir()
	env := append(standInHome(t, shells), helperEnv+"=loadline", "LOADLINE_HOME="+loadlineHome)
	input := largeStatusInput(t)

	for _, original := range nonReaders {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, original) + `}}`)
		out, _, _ := plan(t, settings, o)
		wrapped := statusCommand(t, out)

		for _, shell := range shells {
			want := runInShell(t, env, openFile(t, input), shell, "-c", original)
			runs := map[string][]string{"": {"-c", wrapped}}
			if hasPipefail(t, shell, env) {
				runs[" under pipefail"] = []string{"-o", "pipefail", "-c", wrapped}
			}

			for how, args := range runs {
				if got := runInShell(t, env, openFile(t, input), shell, args...); got != want {
					t.Errorf("%s%s: %q behind the tap printed %q, %q on stderr (exit %d); on its own %q, %q (exit %d)",
						shell, how, original, got.stdout, got.stderr, got.code, want.stdout, want.stderr, want.code)
				}
			}
		}
	}
	if names := entries(t, loadlineHome); len(names) != 0 {
		t.Errorf("the tap wrote %q to its Loadline home, want it to pass its input through and nothing else", names)
	}
}

// largeStatusInput writes a status-line input of 256 KiB, more than any
// platform's pipe buffer, to a file and returns its path. It has no
// session_id, so the tap forwards none of it.
func largeStatusInput(t *testing.T) string {
	t.Helper()

	payload := `{"model":{"id":"opus"},"padding":"` + strings.Repeat("x", 256<<10) + `"}` + "\n"
	path := filepath.Join(t.TempDir(), "status.json")
	writeFile(t, path, []byte(payload))
	return path
}

// openFile opens path as one run's stdin, read from its start, and closes it
// after the test.
func openFile(t *testing.T, path string) *os.File {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

// hasPipefail reports whether shell takes -o pipefail; dash, for one, does
// not.
func hasPipefail(t *testing.T, shell string, env []string) bool {
	t.Helper()

	return runInShell(t, env, strings.NewReader(""), shell, "-o", "pipefail", "-c", "true").code == 0
}

// entries lists the names in dir.
func entries(t *testing.T, dir string) []string {
	t.Helper()

	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list))
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}
