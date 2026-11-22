package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/cli"
)

type result struct {
	code   int
	stdout string
	stderr string
}

func run(t *testing.T, args ...string) result {
	t.Helper()

	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), args, cli.IO{
		In:  strings.NewReader(""),
		Out: &stdout,
		Err: &stderr,
	})
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func setVersion(t *testing.T, version string) {
	t.Helper()

	previous := cli.Version
	cli.Version = version
	t.Cleanup(func() { cli.Version = previous })
}

func TestRun_Version(t *testing.T) {
	setVersion(t, "v1.2.3")

	got := run(t, "version")

	if got.code != 0 {
		t.Errorf("exit code = %d, want 0", got.code)
	}
	if want := "loadline v1.2.3\n"; got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}
}

func TestVersion_DefaultsToDev(t *testing.T) {
	if cli.Version != "dev" {
		t.Errorf("Version = %q, want %q", cli.Version, "dev")
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	got := run(t, "frobnicate")

	if got.code != 2 {
		t.Errorf("exit code = %d, want 2", got.code)
	}
	if !strings.Contains(got.stderr, `unknown command "frobnicate"`) {
		t.Errorf("stderr = %q, want it to name the unknown command", got.stderr)
	}
	if !strings.Contains(got.stderr, "Usage:") {
		t.Errorf("stderr = %q, want it to include usage", got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty", got.stdout)
	}
}

func TestRun_NoArguments(t *testing.T) {
	got := run(t)

	if got.code != 2 {
		t.Errorf("exit code = %d, want 2", got.code)
	}
	if !strings.Contains(got.stderr, "Usage:") {
		t.Errorf("stderr = %q, want usage", got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty", got.stdout)
	}
}

func TestRun_Help(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			got := run(t, arg)

			if got.code != 0 {
				t.Errorf("exit code = %d, want 0", got.code)
			}
			if !strings.Contains(got.stdout, "Usage:") || !strings.Contains(got.stdout, "version") {
				t.Errorf("stdout = %q, want usage listing the version command", got.stdout)
			}
			if got.stderr != "" {
				t.Errorf("stderr = %q, want empty", got.stderr)
			}
		})
	}
}

func TestRun_HelpListsServeAndStatus(t *testing.T) {
	got := run(t, "help")

	for _, command := range []string{"\n  serve ", "\n  status "} {
		if !strings.Contains(got.stdout, command) {
			t.Errorf("usage %q does not list %q", got.stdout, strings.TrimSpace(command))
		}
	}
}

func TestRun_DispatchesServeAndStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", home)
	contents := fmt.Sprintf(`{"port":%d}`, freePort(t))
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if got := run(t, "serve", "--port", "80"); got.code != 2 || !strings.Contains(got.stderr, "--port") {
		t.Errorf("serve --port 80 = %d, stderr %q; want serve's usage error", got.code, got.stderr)
	}
	if got := run(t, "status"); got.code != 1 || got.stderr != "loadline is not running\n" {
		t.Errorf("status = %d, stderr %q; want the status command's answer", got.code, got.stderr)
	}
}

// status and version take no arguments, so anything after them is a usage
// error that shows the command's usage, never something they quietly ignore.
func TestRun_StatusAndVersionRejectArguments(t *testing.T) {
	t.Setenv("LOADLINE_HOME", t.TempDir())
	for _, tc := range []struct {
		args []string
		says string
	}{
		{[]string{"status", "extra"}, `loadline status: unexpected argument "extra"`},
		{[]string{"status", "--bogus"}, "flag provided but not defined: -bogus"},
		{[]string{"version", "extra"}, `loadline version: unexpected argument "extra"`},
		{[]string{"version", "--short"}, "flag provided but not defined: -short"},
	} {
		got := run(t, tc.args...)

		if got.code != 2 || got.stdout != "" || !strings.Contains(got.stderr, tc.says) ||
			!strings.Contains(got.stderr, "Usage: loadline "+tc.args[0]+"\n") {
			t.Errorf("%q = %d, stdout %q, stderr %q; want 2, %q and the usage", tc.args, got.code, got.stdout, got.stderr, tc.says)
		}
	}
}

// Like every command that parses its flags, status and version show their
// usage for -h and exit 0. status then asks no server anything.
func TestRun_StatusAndVersionHelp(t *testing.T) {
	t.Setenv("LOADLINE_HOME", t.TempDir())
	for _, command := range []string{"status", "version"} {
		for _, flag := range []string{"-h", "--help"} {
			got := run(t, command, flag)

			if want := "Usage: loadline " + command + "\n"; got.code != 0 || got.stdout != "" || got.stderr != want {
				t.Errorf("%s %s = %d, stdout %q, stderr %q; want 0 and %q", command, flag, got.code, got.stdout, got.stderr, want)
			}
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestRun_StdoutWriteFailure(t *testing.T) {
	var stderr bytes.Buffer

	code := cli.Run(context.Background(), []string{"version"}, cli.IO{
		In:  strings.NewReader(""),
		Out: failingWriter{},
		Err: &stderr,
	})

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "broken pipe") {
		t.Errorf("stderr = %q, want it to report the write error", stderr.String())
	}
}
