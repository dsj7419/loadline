package cli_test

import (
	"bytes"
	"context"
	"errors"
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
