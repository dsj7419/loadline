package cli_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/protocol"
)

func runStatus(t *testing.T) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.RunStatus(t.Context(), cli.IO{In: strings.NewReader(""), Out: &stdout, Err: &stderr})
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// statusFixture is a server whose Loadline home is LOADLINE_HOME, as the
// status command finds it.
func statusFixture(t *testing.T, accounts string) *serveFixture {
	t.Helper()
	t.Setenv("LOADLINE_HOME", t.TempDir())
	f := newServeFixture(t)
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	f.paths = paths
	f.writeConfig(fmt.Sprintf(`{"port":%d,"accounts":%s}`, f.port, accounts))
	return f
}

func TestStatus_PrintsOneRowPerSessionAndPerAccountWithout(t *testing.T) {
	f := statusFixture(t, `[{"configDir":"~/.claude","label":"Main"},{"configDir":"~/.claude-b","label":"B"},`+
		`{"configDir":"~/.claude-work","label":"Work"}]`)
	f.claude.setAgents(f.defaultDir(), `[{"sessionId":"s1","kind":"interactive","pid":101,"status":"waiting",`+
		`"waitingFor":"permission prompt","name":"copper-lantern","cwd":"/p","startedAt":1795299000000}]`)
	f.start()
	waitFor(t, "the poll of s1", func() bool {
		s, ok := sessionByID(f.snapshot(), "s1")
		return ok && s.State == domain.StateNeedsYou
	})

	resets := time.Date(2026, time.November, 23, 9, 0, 0, 0, time.Local)
	f.send(forward.StatusPath, f.envelope(protocol.KindStatus, f.defaultDir(), t0, fmt.Sprintf(
		`{"session_id":"s1","model":{"id":"claude-opus-5-5","display_name":"Opus 5.5"},`+
			`"context_window":{"used_percentage":31},"rate_limits":{"seven_day":{"used_percentage":93,"resets_at":%d}}}`,
		resets.Unix())))
	f.hook(f.defaultDir(), "s2", "SessionStart")
	f.hook(f.defaultDir(), "s2", "SessionEnd")
	f.send(forward.StatusPath, f.envelope(protocol.KindStatus, f.dir(".claude-work"), t0,
		`{"session_id":"aaaaaaaa-0000-4000-8000-000000000003","model":{"id":"claude-sonnet-5-5","display_name":"Sonnet 5.5"},`+
			`"context_window":{"used_percentage":8}}`))

	got := runStatus(t)

	want := "" +
		"ACCOUNT  WEEK  RESETS     SESSION         STATE      MODEL       CTX\n" +
		"Main     93%   Mon 09:00  copper-lantern  needs_you  Opus 5.5    31%\n" +
		"B        -     -          -               -          -           -\n" +
		"Work     -     -          aaaaaaaa        idle       Sonnet 5.5  8%\n"
	if got.code != 0 || got.stdout != want || got.stderr != "" {
		t.Errorf("status = %d, stderr %q, stdout:\n%s\nwant 0 and:\n%s", got.code, got.stderr, got.stdout, want)
	}
}

func TestStatus_NotRunning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", home)
	contents := fmt.Sprintf(`{"port":%d}`, freePort(t))
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got := runStatus(t)

	if got.code != 1 || got.stderr != "loadline is not running\n" || got.stdout != "" {
		t.Errorf("status = %d, stdout %q, stderr %q; want 1 and %q", got.code, got.stdout, got.stderr, "loadline is not running\n")
	}
}

func TestStatus_AnotherProgramOnThePortIsNotLoadline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", home)
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
	contents := fmt.Sprintf(`{"port":%d}`, portOf(t, l.Addr()))
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if got := runStatus(t); got.code != 1 || got.stderr != "loadline is not running\n" {
		t.Errorf("status = %d, stderr %q; want 1 and %q", got.code, got.stderr, "loadline is not running\n")
	}
}
