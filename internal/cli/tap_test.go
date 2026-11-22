package cli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/protocol"
)

// statusInput is a status-line payload with every field Loadline reads and
// some it must not forward.
func statusInput(t *testing.T) string {
	t.Helper()
	return marshal(t, map[string]any{
		"session_id":      "s1",
		"session_name":    "demo",
		"transcript_path": filepath.Join("home", canary+".jsonl"),
		"cwd":             filepath.Join("projects", "demo"),
		"model":           map[string]any{"id": "claude-opus-5-5", "display_name": "Opus 5.5"},
		"workspace":       map[string]any{"current_dir": canary, "project_dir": filepath.Join("projects", "demo")},
		"output_style":    map[string]any{"name": canary},
		"context_window":  map[string]any{"used_percentage": 42.4, "context_window_size": 1000000},
		"rate_limits": map[string]any{
			"seven_day": map[string]any{"used_percentage": 97.6, "resets_at": 1800000000},
		},
	})
}

func (f *fixture) tap(input string, mode cli.TapMode) (code int, stdout string) {
	var out bytes.Buffer
	code = cli.RunTap(f.t.Context(), strings.NewReader(input), &out, f.env(), f.paths, f.cfg, mode)
	return code, out.String()
}

func TestRunTap_EchoesAndForwardsTheAllowlist(t *testing.T) {
	cli.SetTapSendTimeout(t, postWindow)
	f := newFixture(t)
	srv := f.serve()
	input := statusInput(t)

	code, stdout := f.tap(input, cli.TapMode{})

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stdout != input {
		t.Errorf("stdout = %q, want the input unchanged", stdout)
	}
	posts := srv.received()
	if len(posts) != 1 || posts[0].path != "/ingest/status" {
		t.Fatalf("server received %+v, want one post to /ingest/status", posts)
	}
	assertNoContent(t, posts[0].raw)
	f.assertEnvelope(t, posts[0].envelope, protocol.KindStatus)
	fields := payloadFields(t, posts[0].envelope)
	if _, ok := fields["transcript_path"]; ok {
		t.Error("payload carries transcript_path, which is not allowlisted")
	}
	events := srv.sink.applied()
	if len(events) != 1 {
		t.Fatalf("server applied %d events, want 1", len(events))
	}
	status, ok := events[0].(domain.Status)
	if !ok || status.Session != "s1" || status.Metrics.Model != "Opus 5.5" || status.Limits.SevenDay == nil {
		t.Errorf("server applied %#v, want s1's status with its model and weekly limit", events[0])
	}
	if started := f.spawns.started(); len(started) != 0 {
		t.Errorf("started %v, want nothing", started)
	}
}

// tapAgainstAHungServer runs the tap against a server that takes its post
// and does not answer for hold, with its output a pipe that is read as it
// is written. It returns how long after the start the echo was complete,
// that is the tap had closed its output, and the tap had returned.
func tapAgainstAHungServer(t *testing.T, hold time.Duration) (srv *ingestServer, echoAt, total time.Duration) {
	t.Helper()
	f := newFixture(t)
	srv = f.serve(func(s *ingestServer) { s.hold = hold })
	input := statusInput(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	echoed := make(chan time.Duration, 1)
	var got bytes.Buffer
	start := time.Now()
	go func() {
		_, _ = io.Copy(&got, reader) // ends when the tap closes its output
		echoed <- time.Since(start)
	}()

	code := cli.RunTap(t.Context(), strings.NewReader(input), writer, f.env(), f.paths, f.cfg, cli.TapMode{})

	total = time.Since(start)
	echoAt = <-echoed
	t.Logf("echo complete after %v, tap returned after %v", echoAt, total)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if got.String() != input {
		t.Errorf("echoed %q, want the input", got.String())
	}
	return srv, echoAt, total
}

// The status line shows the echo, so it must not wait for the server: the
// output is complete, and closed, while the post still waits for a server
// that is slow to answer.
func TestRunTap_EchoesBeforeForwarding(t *testing.T) {
	cli.SetTapSendTimeout(t, postWindow)
	const hold = 500 * time.Millisecond

	srv, echoAt, total := tapAgainstAHungServer(t, hold)

	if bound := hotPathBound(20 * time.Millisecond); echoAt > bound {
		t.Errorf("echo complete after %v, want within %v", echoAt, bound)
	}
	if total < hold {
		t.Errorf("tap returned after %v, before the server answered at %v; want it to wait for its post", total, hold)
	}
	if posts := srv.received(); len(posts) != 1 {
		t.Errorf("server received %d posts, want 1", len(posts))
	}
}

// The status line process does not linger on a server that does not answer:
// the tap echoes at once and gives up on its post at its 100 ms bound.
func TestRunTap_GivesUpOnAHungServer(t *testing.T) {
	_, echoAt, total := tapAgainstAHungServer(t, 2*time.Second)

	if bound := hotPathBound(20 * time.Millisecond); echoAt > bound {
		t.Errorf("echo complete after %v, want within %v", echoAt, bound)
	}
	if bound := hotPathBound(150 * time.Millisecond); total > bound {
		t.Errorf("tap took %v against a hung server, want at most %v", total, bound)
	}
}

func TestRunTap_EnsureStampRateLimitsSpawn(t *testing.T) {
	f := newFixture(t)
	f.token()
	f.serverDown()
	stamp := filepath.Join(f.paths.DataDir, "ensure.stamp")
	steps := []struct {
		advance time.Duration
		spawns  int
	}{
		{advance: 0, spawns: 1},                // no stamp yet
		{advance: 10 * time.Second, spawns: 1}, // stamped 10 s ago
		{advance: 20 * time.Second, spawns: 1}, // stamped 30 s ago
		{advance: time.Second, spawns: 2},      // stamped 31 s ago
		{advance: time.Second, spawns: 2},      // stamped again just now
	}
	for i, step := range steps {
		f.clock.Advance(step.advance)

		code, stdout := f.tap(statusInput(t), cli.TapMode{})

		if code != 0 || stdout != statusInput(t) {
			t.Errorf("step %d: tap = %d, %q; want 0 and the echo", i, code, stdout)
		}
		if got := len(f.spawns.started()); got != step.spawns {
			t.Errorf("step %d: %d spawns so far, want %d", i, got, step.spawns)
		}
	}
	if started := f.spawns.started(); started[0] != testExe+" serve" {
		t.Errorf("started %v, want %s serve", started, testExe)
	}
	info, err := os.Stat(stamp)
	if err != nil {
		t.Fatalf("stat stamp: %v", err)
	}
	if want := t0.Add(31 * time.Second); !info.ModTime().Equal(want) {
		t.Errorf("stamp time = %v, want the last spawn's %v", info.ModTime(), want)
	}
}

// A stamp from the future, left by a clock that has since stepped back,
// must not hold off starting the server.
func TestRunTap_StampFromTheFutureIsStale(t *testing.T) {
	f := newFixture(t)
	f.token()
	f.serverDown()
	stamp := filepath.Join(f.paths.DataDir, "ensure.stamp")
	if err := os.WriteFile(stamp, nil, 0o600); err != nil {
		t.Fatalf("write stamp: %v", err)
	}
	if err := os.Chtimes(stamp, t0.Add(time.Hour), t0.Add(time.Hour)); err != nil {
		t.Fatalf("set stamp time: %v", err)
	}

	f.tap(statusInput(t), cli.TapMode{})

	if got := len(f.spawns.started()); got != 1 {
		t.Errorf("%d spawns, want 1", got)
	}
}

func TestRunTap_NeverSpools(t *testing.T) {
	f := newFixture(t)
	f.token()
	f.serverDown()

	f.tap(statusInput(t), cli.TapMode{})

	if spooled := f.spooled(); len(spooled) != 0 {
		t.Errorf("spooled %d status events, want none", len(spooled))
	}
}

func TestRunTap_UnforwardableInputIsStillEchoed(t *testing.T) {
	inputs := map[string]string{
		"empty":      "",
		"not JSON":   "status " + canary,
		"no session": `{"model":{"display_name":"Opus 5.5"}}`,
		"over 1 MiB": `{"session_id":"s1","pad":"` + strings.Repeat("p", 1<<20) + `"}`,
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			srv := f.serve()

			code, stdout := f.tap(input, cli.TapMode{})

			if code != 0 || stdout != input {
				t.Errorf("tap = %d with %d bytes out, want 0 and the %d byte input", code, len(stdout), len(input))
			}
			if posts := srv.received(); len(posts) != 0 {
				t.Errorf("server received %d posts, want none", len(posts))
			}
			if started := f.spawns.started(); len(started) != 0 {
				t.Errorf("started %v, want nothing", started)
			}
		})
	}
}

// brokenWriter is an output whose reader has gone.
type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestRunTap_BrokenStdoutStillForwards(t *testing.T) {
	cli.SetTapSendTimeout(t, postWindow)
	f := newFixture(t)
	srv := f.serve()

	code := cli.RunTap(t.Context(), strings.NewReader(statusInput(t)), brokenWriter{}, f.env(), f.paths, f.cfg, cli.TapMode{})

	if code != 1 {
		t.Errorf("exit code = %d, want 1 for an echo that failed", code)
	}
	if posts := srv.received(); len(posts) != 1 {
		t.Errorf("server received %d posts, want 1", len(posts))
	}
}

func TestRunTap_ClosedStdoutPipe(t *testing.T) {
	cli.SetTapSendTimeout(t, postWindow)
	f := newFixture(t)
	srv := f.serve()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	done := make(chan int, 1)

	go func() {
		done <- cli.RunTap(t.Context(), strings.NewReader(statusInput(t)), writer, f.env(), f.paths, f.cfg, cli.TapMode{})
	}()

	select {
	case code := <-done:
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tap hung on a closed stdout")
	}
	if posts := srv.received(); len(posts) != 1 {
		t.Errorf("server received %d posts, want 1", len(posts))
	}
}

func TestRunTap_PrintMode(t *testing.T) {
	cli.SetTapSendTimeout(t, postWindow)
	tests := []struct {
		name, input, want string
	}{
		{name: "everything", input: statusInput(t), want: "Opus 5.5 · ctx 42% · 7d 98%\n"},
		{
			name:  "no rate limits",
			input: `{"session_id":"s1","model":{"display_name":"Opus 5.5"},"context_window":{"used_percentage":42}}`,
			want:  "Opus 5.5 · ctx 42%\n",
		},
		{
			name:  "a window without usage",
			input: `{"session_id":"s1","model":{"display_name":"Opus 5.5"},"rate_limits":{"seven_day":{"used_percentage":null}}}`,
			want:  "Opus 5.5\n",
		},
		{name: "only usage", input: `{"session_id":"s1","rate_limits":{"seven_day":{"used_percentage":12.4}}}`, want: "7d 12%\n"},
		{name: "nothing known", input: `{"session_id":"s1"}`, want: "\n"},
		{name: "not JSON", input: "status " + canary, want: "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			srv := f.serve()

			code, stdout := f.tap(tt.input, cli.TapMode{Print: true})

			if code != 0 || stdout != tt.want {
				t.Errorf("tap --print = %d, %q; want 0, %q", code, stdout, tt.want)
			}
			wantPosts := 1
			if tt.name == "not JSON" {
				wantPosts = 0
			}
			if posts := srv.received(); len(posts) != wantPosts {
				t.Errorf("server received %d posts, want %d", len(posts), wantPosts)
			}
		})
	}
}

// runPrefix is what runCommand's command prints before its stdin.
const runPrefix = `say "hi" it's `

// runCommand returns a status line command for this platform that prints
// runPrefix, which holds both kinds of quote, then its stdin, then stderr on
// its standard error, and exits with code.
func runCommand(stderr string, code int) string {
	if runtime.GOOS == "windows" {
		return `$in = [Console]::In.ReadToEnd(); [Console]::Out.Write('say "hi" it''s ' + $in); ` +
			`[Console]::Error.Write('` + stderr + `'); exit ` + strconv.Itoa(code)
	}
	return `printf '%s' "say \"hi\" it's "; cat; printf '%s' '` + stderr + `' >&2; exit ` + strconv.Itoa(code)
}

func TestRunTap_RunPipesStdinThroughTheCommand(t *testing.T) {
	cli.SetTapSendTimeout(t, postWindow)
	f := newFixture(t)
	srv := f.serve()
	command := runCommand("warned", 3)
	input := statusInput(t)
	var stdout, stderr bytes.Buffer

	code := cli.RunTap(t.Context(), strings.NewReader(input), &stdout, f.env(), f.paths, f.cfg,
		cli.TapMode{Run: command, Stderr: &stderr})

	if code != 3 {
		t.Errorf("exit code = %d, want the command's 3", code)
	}
	if want := runPrefix + input; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "warned" {
		t.Errorf("stderr = %q, want the command's %q", stderr.String(), "warned")
	}
	if posts := srv.received(); len(posts) != 1 {
		t.Errorf("server received %d posts, want 1", len(posts))
	}
}

func tapThroughCLI(t *testing.T, input string, args ...string) (code int, stdout, stderr string, srv *ingestServer) {
	t.Helper()
	cli.SetTapSendTimeout(t, postWindow)
	f := newFixture(t)
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	f.paths.TokenFile = filepath.Join(home, "token")
	srv = f.serve()
	writeConfig(t, filepath.Join(home, "config.json"), f.cfg.Port)
	var out, errOut bytes.Buffer
	code = cli.Run(context.Background(), append([]string{"tap"}, args...), cli.IO{
		In: strings.NewReader(input), Out: &out, Err: &errOut,
	})
	return code, out.String(), errOut.String(), srv
}

func TestTap_RunAndRunB64TakeQuotesThroughTheCommandLine(t *testing.T) {
	command := runCommand("", 0)
	input := statusInput(t)
	for name, args := range map[string][]string{
		"--run":     {"--run", command},
		"--run=":    {"--run=" + command},
		"--run-b64": {"--run-b64", base64.RawURLEncoding.EncodeToString([]byte(command))},
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr, srv := tapThroughCLI(t, input, args...)

			if code != 0 || stdout != runPrefix+input || stderr != "" {
				t.Errorf("tap %s = %d, %q, %q; want 0, %q and no stderr", name, code, stdout, stderr, runPrefix+input)
			}
			if posts := srv.received(); len(posts) != 1 {
				t.Errorf("server received %d posts, want 1", len(posts))
			}
		})
	}
}

// Windows PowerShell 5.1 splits an argument holding unescaped double quotes
// into several; --run joins them back with single spaces.
func TestTap_RunJoinsSplitArguments(t *testing.T) {
	parts := []string{"printf", "'%s|%s'", "joined", "parts"}
	want := "joined|parts"
	if runtime.GOOS == "windows" {
		parts = []string{"[Console]::Out.Write('joined'", "+", "'|'", "+", "'parts')"}
	}

	code, stdout, stderr, _ := tapThroughCLI(t, statusInput(t), append([]string{"--run"}, parts...)...)

	if code != 0 || stdout != want || stderr != "" {
		t.Errorf("tap --run %v = %d, %q, %q; want 0, %q and no stderr", parts, code, stdout, stderr, want)
	}
}

func TestTap_InvalidRunB64EchoesAndForwards(t *testing.T) {
	input := statusInput(t)
	for _, value := range []string{"not base64!", "cHJpbnRm=", "", base64.RawURLEncoding.EncodeToString([]byte{0xff, 0xfe})} {
		t.Run(value, func(t *testing.T) {
			code, stdout, _, srv := tapThroughCLI(t, input, "--run-b64", value)

			if code != 0 || stdout != input {
				t.Errorf("tap --run-b64 %q = %d, %q; want 0 and the input echoed", value, code, stdout)
			}
			if posts := srv.received(); len(posts) != 1 {
				t.Errorf("server received %d posts, want 1", len(posts))
			}
		})
	}
}

func TestTap_PrintThroughTheCommandLine(t *testing.T) {
	code, stdout, _, _ := tapThroughCLI(t, statusInput(t), "--print")

	if code != 0 || stdout != "Opus 5.5 · ctx 42% · 7d 98%\n" {
		t.Errorf("tap --print = %d, %q; want 0 and the summary", code, stdout)
	}
}

// failingReader fails the test that reads it.
type failingReader struct{ t *testing.T }

func (r failingReader) Read([]byte) (int, error) {
	r.t.Error("read stdin, want it left alone")
	return 0, io.EOF
}

// Options the tap cannot read, as from an install by a newer version, must
// never blank the status line: the tap passes its input through and forwards
// it, as it does with no options, and then says on one line what was wrong.
func TestTap_BadOptionsStillPassTheInputThrough(t *testing.T) {
	input := statusInput(t)
	for _, args := range [][]string{
		{"--bogus"},
		{"stray"},
		{"--print", "extra"},
		{"--run"},
		{"--run", " "},
		{"--run-b64"},
		{"--run-b64", "a", "b"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, stdout, stderr, srv := tapThroughCLI(t, input, args...)

			if code != 0 || stdout != input {
				t.Errorf("tap %q = %d, stdout %q; want 0 and the input unchanged", args, code, stdout)
			}
			if !strings.HasPrefix(stderr, "loadline tap: ") || strings.Count(stderr, "\n") != 1 || !strings.HasSuffix(stderr, "\n") {
				t.Errorf("stderr %q, want one line saying what was wrong", stderr)
			}
			if posts := srv.received(); len(posts) != 1 {
				t.Errorf("server received %d posts, want 1", len(posts))
			}
		})
	}
}

func TestTap_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := cli.Run(context.Background(), []string{"tap", "--help"}, cli.IO{
		In: failingReader{t}, Out: &stdout, Err: &stderr,
	})

	if code != 0 || !strings.Contains(stdout.String(), "--run-b64") || stderr.Len() != 0 {
		t.Errorf("tap --help = %d, %q, %q; want 0 and the usage on stdout", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "PowerShell 5.1") {
		t.Errorf("tap usage %q does not explain the PowerShell 5.1 limitation of --run", stdout.String())
	}
}

func TestRun_HelpListsHookAndTap(t *testing.T) {
	got := run(t, "help")

	for _, command := range []string{"hook", "tap"} {
		if !strings.Contains(got.stdout, "  "+command+" ") {
			t.Errorf("help %q does not list %s", got.stdout, command)
		}
	}
}

// BenchmarkRunTap measures one tap's own cost against a server that answers
// at once.
func BenchmarkRunTap(b *testing.B) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "{}")
	}))
	b.Cleanup(ts.Close)
	dir := b.TempDir()
	paths := config.Paths{DataDir: dir, TokenFile: filepath.Join(dir, "token"), SpoolDir: filepath.Join(dir, "spool")}
	if _, err := config.EnsureToken(paths.TokenFile); err != nil {
		b.Fatalf("EnsureToken: %v", err)
	}
	cfg := config.Defaults()
	cfg.Port = portOf(b, ts.Listener.Addr())
	home := filepath.Join(dir, "home")
	env := cli.Env{
		Getenv: func(string) string { return "" },
		Home:   home,
		Now:    time.Now,
		Exe:    testExe,
		Spawn: func(string, ...string) error {
			b.Error("the tap started the server, so a forward failed")
			return nil
		},
	}
	input := []byte(marshal(b, map[string]any{
		"session_id": "s1",
		"model":      map[string]any{"id": "claude-opus-5-5", "display_name": "Opus 5.5"},
		"rate_limits": map[string]any{
			"seven_day": map[string]any{"used_percentage": 97.6, "resets_at": 1800000000},
		},
	}))
	b.ReportAllocs()

	for b.Loop() {
		if code := cli.RunTap(b.Context(), bytes.NewReader(input), io.Discard, env, paths, cfg, cli.TapMode{}); code != 0 {
			b.Fatalf("exit code = %d", code)
		}
	}
}
