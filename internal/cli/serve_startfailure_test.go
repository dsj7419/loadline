package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
)

// startFailureMessage is the message of the log record a server that could
// not start leaves.
const startFailureMessage = `msg="loadline could not start"`

// serveOnceBound is how long serveOnce lets Serve run: far longer than a
// server takes to refuse to start, or to leave a port another one serves.
const serveOnceBound = 5 * time.Second

// serveOnce runs Serve with o until it returns, and returns its exit code and
// what it wrote to stderr. It is bounded, so that a server that wrongly
// starts gives way to the check; one the bound ended fails the test, since a
// server that serves until it is stopped exits 0, as one that leaves does.
func serveOnce(t *testing.T, o cli.ServeOptions) (code int, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), serveOnceBound)
	defer cancel()
	var stdout, errOut bytes.Buffer
	code = cli.Serve(ctx, o, cli.IO{In: strings.NewReader(""), Out: &stdout, Err: &errOut})
	if ctx.Err() != nil {
		t.Errorf("Serve = %d ran until the %s bound ended it, want it to return by itself", code, serveOnceBound)
	}
	if stdout.Len() != 0 {
		t.Errorf("Serve wrote %q to stdout, want nothing", stdout.String())
	}
	return code, errOut.String()
}

// logLines returns the lines of the log in the fixture's data directory,
// none when there is no log.
func logLines(t *testing.T, f *serveFixture) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.paths.DataDir, "loadline.log"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// holdPort listens on a free loopback port with a server that is not
// Loadline's, until the test ends, and returns the port.
func holdPort(t *testing.T) int {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	other := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"app":"something-else"}`)
		}),
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = other.Serve(l) }()
	t.Cleanup(func() { _ = other.Close() })
	return portOf(t, l.Addr())
}

// A server that a hook or a status line started has a stderr that goes
// nowhere, so why it could not start goes to its log as well: one line,
// which never holds the token.
func TestServe_LogsWhyItCouldNotStart(t *testing.T) {
	for name, setUp := range map[string]func(t *testing.T, f *serveFixture) (reason string){
		"a bad config": func(_ *testing.T, f *serveFixture) string {
			f.writeBrokenConfig(`{"idleExitMinutes":-5}`)
			return "idleExitMinutes -5 is below 0"
		},
		"a port another program holds": func(t *testing.T, f *serveFixture) string {
			t.Helper()
			f.port = holdPort(t)
			return fmt.Sprintf("port %d is in use by another program", f.port)
		},
		"a token file that holds no token": func(t *testing.T, f *serveFixture) string {
			t.Helper()
			// Nearly a token, which the log must not repeat.
			if err := os.WriteFile(f.paths.TokenFile, []byte(strings.Repeat("5e", 31)), 0o600); err != nil {
				t.Fatalf("write token file: %v", err)
			}
			return "does not hold 64 hex characters"
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newServeFixture(t)
			// Install made the data directory and the token.
			token := f.token()
			reason := setUp(t, f)

			code, stderr := serveOnce(t, f.options())

			if code != 1 || !strings.Contains(stderr, reason) {
				t.Fatalf("Serve = %d, stderr %q; want 1 and %q", code, stderr, reason)
			}
			lines := logLines(t, f)
			if len(lines) != 1 || !strings.Contains(lines[0], "level=ERROR") ||
				!strings.Contains(lines[0], startFailureMessage) || !strings.Contains(lines[0], reason) {
				t.Errorf("log lines %q, want one error saying %q", lines, reason)
			}
			for _, secret := range []string{string(token), strings.Repeat("5e", 31)} {
				if strings.Contains(strings.Join(lines, ""), secret) {
					t.Errorf("log %q holds %q", lines, secret)
				}
			}
		})
	}
}

// Each start that fails adds its own line, after what the log already holds.
func TestServe_StartFailuresAppendToTheLog(t *testing.T) {
	f := newServeFixture(t)
	f.token()
	f.start().stop(t)
	before := logLines(t, f)
	f.writeBrokenConfig(`{"idleExitMinutes":-5}`)

	serveOnce(t, f.options())
	serveOnce(t, f.options())

	lines := logLines(t, f)
	if len(lines) != len(before)+2 || !slices.Equal(lines[:len(before)], before) {
		t.Fatalf("log lines %q, want %q and two more", lines, before)
	}
	for _, line := range lines[len(before):] {
		if !strings.Contains(line, startFailureMessage) {
			t.Errorf("log line %q, want it to say why the server could not start", line)
		}
	}
}

// The log goes only into a data directory that is the user's alone: one that
// is not there yet is the job of a start that gets far enough to secure it,
// and one that Loadline refuses may hold a link, or a file of someone
// else's, where the log would go.
func TestServe_LeavesAStartFailureOutOfADataDirectoryItCannotTrust(t *testing.T) {
	t.Run("no data directory yet", func(t *testing.T) {
		f := newServeFixture(t)
		f.writeBrokenConfig(`{"idleExitMinutes":-5}`)

		if code, stderr := serveOnce(t, f.options()); code != 1 || stderr == "" {
			t.Fatalf("Serve = %d, stderr %q; want 1 and why", code, stderr)
		}
		if _, err := os.Stat(f.paths.DataDir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat data directory: %v; want it never created", err)
		}
	})
	t.Run("a data directory that holds other things", func(t *testing.T) {
		f := newServeFixture(t)
		f.token()
		if err := os.WriteFile(filepath.Join(f.paths.DataDir, "notes.txt"), []byte("mine"), 0o600); err != nil {
			t.Fatalf("write notes: %v", err)
		}

		if code, stderr := serveOnce(t, f.options()); code != 1 || !strings.Contains(stderr, "notes.txt") {
			t.Fatalf("Serve = %d, stderr %q; want 1 and the refusal", code, stderr)
		}
		if lines := logLines(t, f); lines != nil {
			t.Errorf("log lines %q in a refused data directory, want no log", lines)
		}
	})
}

// A second server that finds a Loadline on the port has nothing to report.
func TestServe_LogsNothingWhenAnotherLoadlineServes(t *testing.T) {
	f := newServeFixture(t)
	f.start()

	if code, stderr := serveOnce(t, f.options()); code != 0 || stderr != "" {
		t.Fatalf("second Serve = %d, stderr %q; want 0 and nothing", code, stderr)
	}
	for _, line := range logLines(t, f) {
		if strings.Contains(line, startFailureMessage) {
			t.Errorf("log line %q, want no start failure", line)
		}
	}
}
