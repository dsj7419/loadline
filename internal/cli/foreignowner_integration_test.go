package cli_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/config"
)

// The TestIntegration_ tests check a data directory that belongs to an
// account other than the user's, which takes what a developer machine seldom
// has: a second account, and on Windows the right to give a folder to it.
// Each skips until the variable it reads is set, as CI sets it, and once it
// is set fails when what that names cannot be used, so that it never passes
// by being skipped. CONTRIBUTING.md says how to run them.

// assertRefusedByAll checks that install, the server and doctor each refuse
// the data directory LOADLINE_HOME names with one and the same sentence,
// which starts with refusal, and returns doctor's line about the directory.
func assertRefusedByAll(t *testing.T, refusal string) (doctorLine string) {
	t.Helper()

	install := run(t, "install", "--config-dir", t.TempDir(), "--yes")
	serveCode, serveStderr := serveWhereLoadlineHomePoints(t)
	doctor := run(t, "doctor", "--config-dir", t.TempDir())

	sentence, _ := strings.CutPrefix(install.stderr, "loadline: ")
	if install.code != 1 || !strings.HasPrefix(sentence, refusal) {
		t.Errorf("install = %d, stderr %q; want 1 and a refusal that starts %q", install.code, install.stderr, refusal)
	}
	if serveCode != 1 || serveStderr != install.stderr {
		t.Errorf("serve = %d, stderr %q; want 1 and the refusal install gave, %q", serveCode, serveStderr, install.stderr)
	}
	doctorLine = dataDirectoryLine(doctor.stdout)
	if want := "✖ data directory: " + sentence; doctor.code != 1 || doctorLine != want {
		t.Errorf("doctor = %d, stdout:\n%s\nwant 1 and the line %q", doctor.code, doctor.stdout, want)
	}
	return doctorLine
}

// serveWhereLoadlineHomePoints runs the server with the paths LOADLINE_HOME
// gives until it returns, and returns its exit code and what it wrote to
// stderr.
func serveWhereLoadlineHomePoints(t *testing.T) (code int, stderr string) {
	t.Helper()
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	f := newServeFixture(t)
	f.paths = paths
	var errOut strings.Builder
	// Bounded, so that a server that wrongly starts gives way to the check.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	code = cli.Serve(ctx, f.options(), cli.IO{In: strings.NewReader(""), Out: io.Discard, Err: &errOut})

	return code, errOut.String()
}

// dataDirectoryLine returns doctor's line about the data directory, with its
// mark and its newline, or "" when stdout has none.
func dataDirectoryLine(stdout string) string {
	for line := range strings.Lines(stdout) {
		if _, check, _ := strings.Cut(line, " "); strings.HasPrefix(check, "data directory: ") {
			return line
		}
	}
	return ""
}
