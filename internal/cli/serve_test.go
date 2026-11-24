package cli_test

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/accounts"
	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/clocktest"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/launch"
	"github.com/dsj7419/loadline/internal/poll"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/serverlock"
	"github.com/dsj7419/loadline/internal/tracker"
)

// fakeClaude stands in for the claude binary: it answers `agents` and
// `auth status` for each config dir with canned output, and records every
// call.
type fakeClaude struct {
	mu     sync.Mutex
	agents map[domain.ConfigDir]string
	auth   map[domain.ConfigDir]string
	// fail, when set, is the error every call returns.
	fail error
	// onAgents, when set, runs at the start of each agents call.
	onAgents func(domain.ConfigDir)
	calls    []claudeCall
	called   chan struct{}
}

type claudeCall struct {
	dir     domain.ConfigDir
	command string
}

func newFakeClaude() *fakeClaude {
	return &fakeClaude{
		agents: make(map[domain.ConfigDir]string),
		auth:   make(map[domain.ConfigDir]string),
		called: make(chan struct{}, 10000),
	}
}

func (c *fakeClaude) Run(_ context.Context, dir domain.ConfigDir, args ...string) ([]byte, error) {
	command := strings.Join(args, " ")
	c.mu.Lock()
	c.calls = append(c.calls, claudeCall{dir: dir, command: command})
	fail, onAgents := c.fail, c.onAgents
	agents, auth := c.agents[dir], c.auth[dir]
	c.mu.Unlock()
	defer func() { c.called <- struct{}{} }()

	if fail != nil {
		return nil, fail
	}
	switch command {
	case "agents --json --all":
		if onAgents != nil {
			onAgents(dir)
		}
		return []byte(cmp.Or(agents, "[]")), nil
	case "auth status --json":
		return []byte(cmp.Or(auth, `{"loggedIn":true,"authMethod":"claude.ai","email":"dev@example.com"}`)), nil
	default:
		return nil, fmt.Errorf("unexpected claude %s", command)
	}
}

func (c *fakeClaude) setAgents(dir domain.ConfigDir, output string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.agents[dir] = output
}

func (c *fakeClaude) history() []claudeCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// dirsCalled returns the dirs command ran for, in order, once each.
func (c *fakeClaude) dirsCalled(command string) []domain.ConfigDir {
	var dirs []domain.ConfigDir
	for _, call := range c.history() {
		if call.command == command && !slices.Contains(dirs, call.dir) {
			dirs = append(dirs, call.dir)
		}
	}
	return dirs
}

const (
	agentsCommand = "agents --json --all"
	authCommand   = "auth status --json"
)

// waitFor polls cond until it holds, failing the test after five seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// syncBuffer is a buffer the server's goroutines may write while the test
// reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// serveFixture is one server's world: its Loadline directories and config,
// the user's home, a fake claude, a free port and a manual clock.
type serveFixture struct {
	t      *testing.T
	home   string
	paths  config.Paths
	port   int
	claude *fakeClaude
	clock  *clocktest.Clock
	http   *http.Client
}

func newServeFixture(t *testing.T) *serveFixture {
	t.Helper()
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	f := &serveFixture{
		t:    t,
		home: filepath.Join(dir, "home"),
		paths: config.Paths{
			DataDir:    data,
			ConfigFile: filepath.Join(dir, "config.json"),
			TokenFile:  filepath.Join(data, "token"),
			SpoolDir:   filepath.Join(data, "spool"),
			DBFile:     filepath.Join(data, "loadline.db"),
			ServerLock: filepath.Join(data, "server.lock"),
			// Where config.DefaultPaths puts it, in the data directory.
			BrowserProfileDir: filepath.Join(data, "browser"),
		},
		port:   freePort(t),
		claude: newFakeClaude(),
		clock:  clocktest.New(t0),
		http:   &http.Client{Transport: &http.Transport{Proxy: nil}},
	}
	t.Cleanup(f.http.CloseIdleConnections)
	// Claude Code has run on this machine, so its default config dir is
	// there.
	f.mkdirs(f.defaultDir())
	return f
}

// mkdirs creates config dirs, as claude leaves them once it has run there.
func (f *serveFixture) mkdirs(dirs ...domain.ConfigDir) {
	f.t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(string(dir), 0o700); err != nil {
			f.t.Fatalf("create %s: %v", dir, err)
		}
	}
}

// callsFor returns how many times claude ran with command for dir.
func (c *fakeClaude) callsFor(dir domain.ConfigDir, command string) int {
	n := 0
	for _, call := range c.history() {
		if call.dir == dir && call.command == command {
			n++
		}
	}
	return n
}

// freePort returns a loopback port that nothing listened on a moment ago.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	return portOf(t, l.Addr())
}

func (f *serveFixture) writeConfig(contents string) {
	f.t.Helper()
	if err := os.WriteFile(f.paths.ConfigFile, []byte(contents), 0o600); err != nil {
		f.t.Fatalf("write config: %v", err)
	}
}

// defaultDir is the config dir Claude Code uses with no CLAUDE_CONFIG_DIR.
func (f *serveFixture) defaultDir() domain.ConfigDir { return config.ResolveConfigDir("", f.home) }

// dir is another account's config dir under the home directory.
func (f *serveFixture) dir(name string) domain.ConfigDir {
	return config.ResolveConfigDir(filepath.Join(f.home, name), f.home)
}

func (f *serveFixture) options() cli.ServeOptions {
	return cli.ServeOptions{
		Paths:  f.paths,
		Home:   f.home,
		Port:   f.port,
		NoOpen: true,
		Open: func(string, launch.Browser) error {
			f.t.Error("the server opened the dashboard, want --no-open to keep it from opening any")
			return nil
		},
		Runner: f.claude,
		Clock:  f.clock.Now,
		Ticker: f.clock,
	}
}

// running is a Serve call in progress.
type running struct {
	cancel context.CancelFunc
	done   chan int
	ready  chan string
	stdout *syncBuffer
	stderr *syncBuffer
	// addr is the address Serve said it listens on.
	addr string
}

// start runs Serve with the fixture's options, as edit changes them, and
// waits until it listens. The test's end stops it and waits for it.
func (f *serveFixture) start(edit ...func(*cli.ServeOptions)) *running {
	f.t.Helper()
	r := f.launch(edit...)
	r.awaitReady(f.t)
	return r
}

// launch runs Serve as start does, and returns at once.
func (f *serveFixture) launch(edit ...func(*cli.ServeOptions)) *running {
	f.t.Helper()
	o := f.options()
	for _, e := range edit {
		e(&o)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, done: make(chan int, 1), ready: make(chan string, 1), stdout: &syncBuffer{}, stderr: &syncBuffer{}}
	o.Ready = func(addr string) { r.ready <- addr }
	go func() { r.done <- cli.Serve(ctx, o, cli.IO{In: strings.NewReader(""), Out: r.stdout, Err: r.stderr}) }()
	f.t.Cleanup(func() { r.stop(f.t) })
	return r
}

// awaitReady waits until Serve listens.
func (r *running) awaitReady(t *testing.T) {
	t.Helper()
	select {
	case r.addr = <-r.ready:
		r.ready <- r.addr
	case code := <-r.done:
		r.done <- code
		t.Fatalf("Serve returned %d before it listened; stderr %q", code, r.stderr.String())
	case <-time.After(serveStartPatience):
		t.Fatalf("Serve did not listen within %v", serveStartPatience)
	}
}

// listening reports whether Serve has said it listens.
func (r *running) listening() bool {
	select {
	case addr := <-r.ready:
		r.ready <- addr
		return true
	default:
		return false
	}
}

// serveStartPatience is how long start waits for Serve to listen. How soon
// the server starts is no test's concern, and a loaded machine with the
// race detector on can take seconds to open the history and restore it.
const serveStartPatience = 30 * time.Second

// stop ends the server's context and returns its exit code.
func (r *running) stop(t *testing.T) int {
	t.Helper()
	r.cancel()
	return r.returned(t)
}

// returned waits for Serve to return, and returns its exit code.
func (r *running) returned(t *testing.T) int {
	t.Helper()
	select {
	case code := <-r.done:
		r.done <- code
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
		return -1
	}
}

// exited reports Serve's exit code once it has returned.
func (r *running) exited() (int, bool) {
	select {
	case code := <-r.done:
		r.done <- code
		return code, true
	default:
		return 0, false
	}
}

func (f *serveFixture) url(path string) string { return forward.LoopbackURL(f.port) + path }

func (f *serveFixture) token() []byte {
	f.t.Helper()
	token, err := config.EnsureToken(f.paths.TokenFile)
	if err != nil {
		f.t.Fatalf("EnsureToken: %v", err)
	}
	return token
}

// get fetches path with the ingest token.
func (f *serveFixture) get(path string) *http.Response {
	f.t.Helper()
	req, err := http.NewRequestWithContext(f.t.Context(), http.MethodGet, f.url(path), http.NoBody)
	if err != nil {
		f.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Loadline-Token", string(f.token()))
	resp, err := f.http.Do(req)
	if err != nil {
		f.t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func (f *serveFixture) snapshot() tracker.View {
	f.t.Helper()
	resp := f.get("/api/snapshot")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("snapshot status %d", resp.StatusCode)
	}
	var v tracker.View
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		f.t.Fatalf("decode snapshot: %v", err)
	}
	return v
}

func (f *serveFixture) envelope(kind protocol.Kind, dir domain.ConfigDir, at time.Time, payload string) protocol.Envelope {
	f.t.Helper()
	id, err := protocol.NewEventID()
	if err != nil {
		f.t.Fatalf("NewEventID: %v", err)
	}
	return protocol.Envelope{
		V: protocol.SchemaVersion, ID: id, Kind: kind, At: at, ConfigDir: string(dir), PID: 4242,
		Payload: json.RawMessage(payload),
	}
}

func (f *serveFixture) send(path string, env protocol.Envelope) {
	f.t.Helper()
	client := forward.Client{BaseURL: forward.LoopbackURL(f.port), Token: f.token(), HTTP: f.http}
	if err := client.Send(f.t.Context(), path, env); err != nil {
		f.t.Fatalf("send %s: %v", path, err)
	}
}

func (f *serveFixture) hook(dir domain.ConfigDir, session, event string) {
	f.t.Helper()
	payload := fmt.Sprintf(`{"session_id":%q,"hook_event_name":%q}`, session, event)
	f.send(forward.HookPath, f.envelope(protocol.KindHook, dir, f.clock.Now(), payload))
}

func (f *serveFixture) statusLine(dir domain.ConfigDir, session string) {
	f.t.Helper()
	payload := fmt.Sprintf(`{"session_id":%q,"model":{"id":"claude-opus-5-5","display_name":"Opus 5.5"}}`, session)
	f.send(forward.StatusPath, f.envelope(protocol.KindStatus, dir, f.clock.Now(), payload))
}

func (f *serveFixture) spool(env protocol.Envelope) {
	f.t.Helper()
	if err := (forward.Spool{Dir: f.paths.SpoolDir}).Put(env); err != nil {
		f.t.Fatalf("spool: %v", err)
	}
}

// waitArmed waits until some code under test has just armed a timer for d:
// one that has all of d still to run.
func (f *serveFixture) waitArmed(d time.Duration) {
	f.t.Helper()
	waitFor(f.t, fmt.Sprintf("a %v timer", d), func() bool { return slices.Contains(f.clock.Pending(), d) })
}

// passDebounce waits until the poller has scheduled the poll a hook asked
// for, and moves the clock to it and no further. The poller then schedules
// its next round on a clock that has stopped, with all of its interval to
// run.
func (f *serveFixture) passDebounce() {
	f.t.Helper()
	f.waitArmed(poll.DefaultDebounce)
	f.clock.Advance(poll.DefaultDebounce)
}

// nextPollIn waits until the poller has scheduled its next round, which it
// does once the round before has ended, and returns how far off that round
// is: the fast interval while a session is live, the slow one while none is.
func (f *serveFixture) nextPollIn() time.Duration {
	f.t.Helper()
	var next time.Duration
	waitFor(f.t, "the poller to schedule its next round", func() bool {
		pending := f.clock.Pending()
		for _, d := range []time.Duration{poll.DefaultFast, poll.DefaultSlow} {
			if slices.Contains(pending, d) {
				next = d
				return true
			}
		}
		return false
	})
	return next
}

// holdPolls makes each `claude agents` call wait until release is called or
// the test ends, so that a round of polls begun meanwhile cannot end, nor
// the poller schedule the next.
func (f *serveFixture) holdPolls() (release func()) {
	held := make(chan struct{})
	release = sync.OnceFunc(func() { close(held) })
	f.t.Cleanup(release)
	f.claude.mu.Lock()
	defer f.claude.mu.Unlock()
	f.claude.onAgents = func(domain.ConfigDir) { <-held }
	return release
}

func (f *serveFixture) logText() string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.paths.DataDir, "loadline.log"))
	if err != nil {
		f.t.Fatalf("read log: %v", err)
	}
	return string(data)
}

func sessionByID(v tracker.View, id domain.SessionID) (tracker.SessionView, bool) {
	for _, s := range v.Sessions {
		if s.ID == id {
			return s, true
		}
	}
	return tracker.SessionView{}, false
}

func accountByDir(v tracker.View, dir domain.ConfigDir) (tracker.AccountView, bool) {
	for _, a := range v.Accounts {
		if a.Dir == dir {
			return a, true
		}
	}
	return tracker.AccountView{}, false
}

// Two sessions that start together each start a server; the one that finds
// the port taken by a running loadline must go quietly.
func TestServe_SecondInstanceExitsQuietly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	port := freePort(t)
	// No claude to run: the servers must not reach the machine's real one.
	contents := fmt.Sprintf(`{"port":%d,"claudePath":%q}`, port, filepath.Join(home, "no-claude"))
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstDone := make(chan int, 1)
	var firstErr syncBuffer
	go func() {
		firstDone <- cli.RunServe(ctx, []string{"--no-open"}, cli.IO{In: strings.NewReader(""), Out: io.Discard, Err: &firstErr})
	}()
	client := forward.Client{BaseURL: forward.LoopbackURL(port)}
	waitFor(t, "the first server to answer", func() bool { return client.Healthy(t.Context()) })

	var stdout, stderr bytes.Buffer
	started := time.Now()
	code := cli.RunServe(t.Context(), nil, cli.IO{In: strings.NewReader(""), Out: &stdout, Err: &stderr})
	took := time.Since(started)

	if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("second RunServe = %d, stdout %q, stderr %q; want 0 and no output", code, stdout.String(), stderr.String())
	}
	if took > time.Second {
		t.Errorf("second RunServe took %v, want under 1s", took)
	}
	cancel()
	select {
	case code := <-firstDone:
		if code != 0 || firstErr.String() != "" {
			t.Errorf("first RunServe = %d, stderr %q; want 0 and no output", code, firstErr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first server did not stop")
	}
}

func TestServe_PortHeldByOtherAppFails(t *testing.T) {
	f := newServeFixture(t)
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
	f.port = portOf(t, l.Addr())

	var stdout, stderr bytes.Buffer
	code := cli.Serve(t.Context(), f.options(), cli.IO{In: strings.NewReader(""), Out: &stdout, Err: &stderr})

	want := fmt.Sprintf("loadline: port %d is in use by another program\n", f.port)
	if code != 1 || stderr.String() != want || stdout.Len() != 0 {
		t.Errorf("Serve = %d, stdout %q, stderr %q; want 1 and %q", code, stdout.String(), stderr.String(), want)
	}
	if calls := f.claude.history(); len(calls) != 0 {
		t.Errorf("ran claude %v without a port to serve on", calls)
	}
}

func TestRunServe_RejectsBadFlags(t *testing.T) {
	t.Setenv("LOADLINE_HOME", t.TempDir())
	for name, args := range map[string][]string{
		"privileged port": {"--port", "80"},
		"port too high":   {"--port", "65536"},
		"unknown flag":    {"--frobnicate"},
		"argument":        {"now"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.RunServe(t.Context(), args, cli.IO{In: strings.NewReader(""), Out: &stdout, Err: &stderr})

			if code != 2 || stderr.Len() == 0 || stdout.Len() != 0 {
				t.Errorf("RunServe(%q) = %d, stdout %q, stderr %q; want 2 and a usage error", args, code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestServe_InvalidConfigFails(t *testing.T) {
	f := newServeFixture(t)
	f.writeConfig(`{"accounts":[{"configDir":".claude-b"}]}`)

	var stderr bytes.Buffer
	code := cli.Serve(t.Context(), f.options(), cli.IO{In: strings.NewReader(""), Out: io.Discard, Err: &stderr})

	if code != 1 || !strings.Contains(stderr.String(), "accounts[0].configDir") {
		t.Errorf("Serve = %d, stderr %q; want 1 and the bad setting named", code, stderr.String())
	}
}

func TestServe_DemoServesTheScenarioWithoutClaude(t *testing.T) {
	f := newServeFixture(t)
	f.start(func(o *cli.ServeOptions) { o.Demo = true })

	v := f.snapshot()

	if len(v.Sessions) != 5 || len(v.Accounts) != 3 {
		t.Errorf("demo shows %d sessions and %d accounts, want 5 and 3", len(v.Sessions), len(v.Accounts))
	}
	if len(v.Alerts) == 0 || len(v.Models) == 0 {
		t.Errorf("demo shows %d alerts and %d models, want some of each", len(v.Alerts), len(v.Models))
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.url("/api/snapshot"), http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := f.http.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("snapshot without a cookie or token: status %d, want 401", resp.StatusCode)
	}
	if calls := f.claude.history(); len(calls) != 0 {
		t.Errorf("demo ran claude %v, want no calls", calls)
	}
}

func TestServe_ServesTheEmbeddedDashboard(t *testing.T) {
	f := newServeFixture(t)
	f.start()

	resp := f.get("/")
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !bytes.Contains(bytes.ToLower(body), []byte("<!doctype html")) {
		t.Errorf("GET / = %d %.80q, want the dashboard page", resp.StatusCode, body)
	}
}

// An open event stream ends with the server's shutdown instead of holding
// it for its five seconds.
func TestServe_ShutdownWithAnOpenEventStreamIsPrompt(t *testing.T) {
	f := newServeFixture(t)
	r := f.start()
	resp := f.get("/api/events")
	defer func() { _ = resp.Body.Close() }()
	lines := bufio.NewReader(resp.Body)
	if line, err := lines.ReadString('\n'); err != nil || line != "event: view\n" {
		t.Fatalf("stream began %q, %v; want a view", line, err)
	}

	started := time.Now()
	code := r.stop(t)

	if took := time.Since(started); took > 2*time.Second {
		t.Errorf("shutdown took %v with a stream open, want well under the 5 s timeout", took)
	}
	if code != 0 {
		t.Errorf("Serve = %d, want 0", code)
	}
	if _, err := io.Copy(io.Discard, lines); err != nil {
		t.Errorf("stream ended with %v, want a clean end", err)
	}
}

func TestServe_ExitsOnceIdleForTheConfiguredMinutes(t *testing.T) {
	f := newServeFixture(t)
	f.writeConfig(`{"idleExitMinutes":1}`)
	r := f.start()

	elapsed := advanceUntilExit(t, f.clock, r, 5*time.Minute)

	if elapsed < time.Minute {
		t.Errorf("exited after %v idle, want at least a minute", elapsed)
	}
	if code, _ := r.exited(); code != 0 {
		t.Errorf("Serve = %d, want 0", code)
	}
	if log := f.logText(); !strings.Contains(log, "reason=idle") {
		t.Errorf("log does not say the server stopped for being idle:\n%s", log)
	}
}

func TestServe_IdleExitWaitsWhileASessionIsLive(t *testing.T) {
	f := newServeFixture(t)
	f.writeConfig(`{"idleExitMinutes":1}`)
	r := f.start()
	f.hook(f.defaultDir(), "s1", "SessionStart")
	f.claude.setAgents(f.defaultDir(), `[{"sessionId":"s1","kind":"interactive","pid":4242,"status":"busy","cwd":"/x","startedAt":1795299000000}]`)

	for range 20 {
		if !awaitIdleCheck(t, f.clock, r) {
			t.Fatal("exited with a session live")
		}
		f.clock.Advance(cli.IdleCheckEvery)
	}

	f.hook(f.defaultDir(), "s1", "SessionEnd")
	f.claude.setAgents(f.defaultDir(), `[]`)
	if elapsed := advanceUntilExit(t, f.clock, r, 5*time.Minute); elapsed < time.Minute {
		t.Errorf("exited %v after the session ended, want at least the idle minute", elapsed)
	}
}

func TestServe_IdleExitZeroNeverExits(t *testing.T) {
	f := newServeFixture(t)
	f.writeConfig(`{"idleExitMinutes":0}`)
	r := f.start()

	for range 60 {
		f.clock.Advance(time.Minute)
	}
	time.Sleep(50 * time.Millisecond)

	if _, done := r.exited(); done {
		t.Fatal("exited although idle exit is off")
	}
}

// advanceUntilExit moves the clock from one idle check to the next until
// Serve returns, and returns how far it moved. It fails the test once the
// clock has moved limit without Serve returning.
func advanceUntilExit(t *testing.T, clock *clocktest.Clock, r *running, limit time.Duration) time.Duration {
	t.Helper()
	var elapsed time.Duration
	for awaitIdleCheck(t, clock, r) {
		if elapsed >= limit {
			t.Fatalf("still serving after %v", limit)
		}
		clock.Advance(cli.IdleCheckEvery)
		elapsed += cli.IdleCheckEvery
	}
	return elapsed
}

// awaitIdleCheck waits until the server has scheduled its next check for
// being idle, and reports true, or until Serve has returned, and reports
// false. A move of the clock by the check's interval then runs that check.
func awaitIdleCheck(t *testing.T, clock *clocktest.Clock, r *running) bool {
	t.Helper()
	scheduled := false
	waitFor(t, "the next idle check, or Serve to return", func() bool {
		if _, done := r.exited(); done {
			return true
		}
		scheduled = slices.Contains(clock.Pending(), cli.IdleCheckEvery)
		return scheduled
	})
	return scheduled
}

// After a restart the spool holds what the hooks could not deliver. All of
// it is applied before the first poll, which would otherwise find those
// sessions missing. The spool holds enough events that a drain running
// beside the first poll would still be at it.
func TestServe_DrainsTheSpoolBeforeTheFirstPoll(t *testing.T) {
	f := newServeFixture(t)
	const spooled = 200
	for i := range spooled {
		f.spool(f.envelope(protocol.KindHook, f.defaultDir(), t0.Add(-time.Minute),
			fmt.Sprintf(`{"session_id":"s%03d","hook_event_name":"SessionStart","source":"startup"}`, i)))
	}
	firstPoll := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.claude.onAgents = func(domain.ConfigDir) {
		once.Do(func() {
			close(firstPoll)
			<-release
		})
	}
	f.start()

	select {
	case <-firstPoll:
	case <-time.After(5 * time.Second):
		t.Fatal("no poll")
	}
	drained := len(f.snapshot().Sessions)
	close(release)
	if drained != spooled {
		t.Errorf("the first poll ran with %d of the %d spooled sessions applied", drained, spooled)
	}
}

func TestServe_DrainsTheSpoolEvery10sAndSetsAsideWhatCanNeverApply(t *testing.T) {
	f := newServeFixture(t)
	f.start()
	waitFor(t, "the first poll", func() bool { return len(f.claude.dirsCalled(agentsCommand)) > 0 })
	f.waitArmed(10 * time.Second)

	f.spool(f.envelope(protocol.KindHook, f.defaultDir(), t0, `{"session_id":"s2","hook_event_name":"SessionStart"}`))
	f.spool(f.envelope(protocol.KindHook, f.defaultDir(), t0, `{"hook_event_name":"Stop"}`))
	polls := len(f.claude.history())
	f.clock.Advance(10 * time.Second)

	waitFor(t, "the drained session", func() bool {
		_, ok := sessionByID(f.snapshot(), "s2")
		return ok
	})
	waitFor(t, "the event without a session to be set aside", func() bool {
		bad, _ := os.ReadDir(filepath.Join(f.paths.SpoolDir, "bad"))
		left, _ := os.ReadDir(f.paths.SpoolDir)
		return len(bad) == 1 && len(left) == 1
	})
	// The drain asks for a poll to confirm what it applied.
	f.waitArmed(500 * time.Millisecond)
	f.clock.Advance(500 * time.Millisecond)
	waitFor(t, "the poll the drain asked for", func() bool { return len(f.claude.history()) > polls })
	waitFor(t, "the drain to be logged", func() bool { return strings.Contains(f.logText(), "events=1") })
}

func TestServe_PollsAndProbesEveryKnownDir(t *testing.T) {
	f := newServeFixture(t)
	f.mkdirs(f.dir(".claude-b"), f.dir(".claude-work"))
	f.writeConfig(`{"accounts":[{"configDir":"~/.claude-b","label":"B"}]}`)
	f.start()

	want := []domain.ConfigDir{f.defaultDir(), f.dir(".claude-b")}
	slices.Sort(want)
	waitFor(t, "both dirs polled and probed", func() bool {
		polled, probed := f.claude.dirsCalled(agentsCommand), f.claude.dirsCalled(authCommand)
		slices.Sort(polled)
		slices.Sort(probed)
		return slices.Equal(polled, want) && slices.Equal(probed, want)
	})

	// A dir first named by a status line is probed at once and polled from
	// the next round on.
	f.waitArmed(30 * time.Second)
	f.statusLine(f.dir(".claude-work"), "s9")
	waitFor(t, "the new dir probed", func() bool {
		return slices.Contains(f.claude.dirsCalled(authCommand), f.dir(".claude-work"))
	})
	f.clock.Advance(30 * time.Second)
	waitFor(t, "the new dir polled", func() bool {
		return slices.Contains(f.claude.dirsCalled(agentsCommand), f.dir(".claude-work"))
	})
}

// The server a hook starts keeps none of that session's Claude variables,
// so the accounts it knows come from its config and the home directory
// alone: the same whichever session started it, and the same as a server
// that kept them.
func TestServe_KnowsTheSameAccountsWhicheverSessionStartedIt(t *testing.T) {
	f := newServeFixture(t)
	f.mkdirs(f.dir(".claude-b"))
	t.Setenv("CLAUDE_CONFIG_DIR", string(f.dir(".claude-b")))
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-canary")
	f.start()

	want := []domain.ConfigDir{f.defaultDir()}
	waitFor(t, "the default dir polled and probed", func() bool {
		return slices.Equal(f.claude.dirsCalled(agentsCommand), want) && slices.Equal(f.claude.dirsCalled(authCommand), want)
	})
	// The next round is armed once the first has polled every dir it knows.
	f.waitArmed(30 * time.Second)
	if got := f.claude.callsFor(f.dir(".claude-b"), agentsCommand) + f.claude.callsFor(f.dir(".claude-b"), authCommand); got != 0 {
		t.Errorf("claude ran %d times for the inherited CLAUDE_CONFIG_DIR, want none until an event names it", got)
	}
}

func TestServe_LabelsAndBudgetsComeFromTheConfig(t *testing.T) {
	f := newServeFixture(t)
	f.mkdirs(f.dir(".claude-b"))
	f.writeConfig(`{"accounts":[{"configDir":"~/.claude-b","label":"Work","dailyBudgetUSD":25}]}`)
	f.start()

	waitFor(t, "the configured account", func() bool {
		a, ok := accountByDir(f.snapshot(), f.dir(".claude-b"))
		return ok && a.Label == "Work" && a.BudgetUSD == 25
	})
}

// The plan the config names for an account replaces the tier claude reports
// for it, and no other account's.
func TestServe_PlanComesFromTheConfig(t *testing.T) {
	f := newServeFixture(t)
	b := f.dir(".claude-b")
	f.mkdirs(b)
	const maxTier = `{"loggedIn":true,"authMethod":"claude.ai","email":"dev@example.com","subscriptionType":"max","planTier":"max_20x"}`
	f.claude.auth[b], f.claude.auth[f.defaultDir()] = maxTier, maxTier
	f.writeConfig(`{"accounts":[{"configDir":"~/.claude-b","label":"B","plan":"Ultra"}]}`)
	f.start()

	waitFor(t, "both accounts probed", func() bool {
		view := f.snapshot()
		configured, okB := accountByDir(view, b)
		other, okDefault := accountByDir(view, f.defaultDir())
		return okB && okDefault && configured.Email != "" && other.Email != ""
	})
	view := f.snapshot()
	for dir, want := range map[domain.ConfigDir]string{b: "Ultra", f.defaultDir(): "Max 20x"} {
		if a, _ := accountByDir(view, dir); a.Plan != want {
			t.Errorf("account %s shows the plan %q, want %q", dir, a.Plan, want)
		}
	}
}

// Every account the config names is listed, under its label, from the
// moment the server is ready, and stays listed though nothing is ever seen
// of it: claude cannot be run for one, and the other's dir is missing.
func TestServe_ListsEveryConfiguredAccountFromTheStart(t *testing.T) {
	f := newServeFixture(t)
	work := f.dir(".claude-work")
	f.mkdirs(work)
	f.claude.fail = errors.New("locate claude: executable file not found")
	f.writeConfig(`{"accounts":[{"configDir":"~/.claude-b","label":"B"},{"configDir":"~/.claude-work","label":"Work"}]}`)
	f.start()

	configured := []struct {
		dir   domain.ConfigDir
		label string
	}{{f.dir(".claude-b"), "B"}, {work, "Work"}}
	assertListed := func(when string) {
		t.Helper()
		view := f.snapshot()
		for _, want := range configured {
			if a, ok := accountByDir(view, want.dir); !ok || a.Label != want.label {
				t.Errorf("%s: account %s listed %t with label %q, want it listed as %q", when, want.dir, ok, a.Label, want.label)
			}
		}
	}
	assertListed("once the server is ready")
	waitFor(t, "a failed poll and probe of Work", func() bool {
		return f.claude.callsFor(work, agentsCommand) > 0 && f.claude.callsFor(work, authCommand) > 0
	})
	assertListed("after claude failed for Work")
}

func TestServe_CreditsComeFromTheConfig(t *testing.T) {
	f := newServeFixture(t)
	f.mkdirs(f.dir(".claude-b"))
	f.writeConfig(`{"accounts":[{"configDir":"~/.claude-b","label":"B","credits":[` +
		`{"usd":20,"addedAt":"2026-11-21T12:00:00-08:00"},{"usd":5.5,"addedAt":"2026-11-22T08:00:00-08:00"}]}]}`)
	f.start()

	waitFor(t, "the configured credits", func() bool {
		a, ok := accountByDir(f.snapshot(), f.dir(".claude-b"))
		return ok && a.Credits != nil && a.Credits.BalanceUSD == 25.5 && a.Credits.Source == domain.CreditSourceEstimate
	})
}

// Credits are estimated over the history the server keeps, so a restart does
// not change them.
func TestServe_CreditsCountTheDaysTheHistoryKeeps(t *testing.T) {
	for name, tc := range map[string]struct {
		retention string
		days      int
	}{
		"a week of history":    {retention: `"retentionDays":7,`, days: 7},
		"the default 30 days":  {days: 30},
		"a year, capped at 30": {retention: `"retentionDays":365,`, days: 30},
	} {
		t.Run(name, func(t *testing.T) {
			f := newServeFixture(t)
			f.mkdirs(f.dir(".claude-b"))
			f.writeConfig(`{` + tc.retention + `"accounts":[{"configDir":"~/.claude-b","credits":[{"usd":20,"addedAt":"2026-11-21T12:00:00-08:00"}]}]}`)
			f.start()

			waitFor(t, "the credits", func() bool {
				a, ok := accountByDir(f.snapshot(), f.dir(".claude-b"))
				return ok && a.Credits != nil && a.Credits.Days == tc.days
			})
		})
	}
}

func TestServe_DemoShowsAnAccountOnCredits(t *testing.T) {
	f := newServeFixture(t)
	f.start(func(o *cli.ServeOptions) { o.Demo = true })

	var onCredits []string
	for _, a := range f.snapshot().Accounts {
		if a.Credits != nil && a.Credits.Active {
			onCredits = append(onCredits, a.Label)
		}
	}
	if !slices.Equal(onCredits, []string{"Main"}) {
		t.Errorf("accounts on credits %v, want Main", onCredits)
	}
}

// Main reports the tier claude reports for max_20x; the demo's config names
// its plan Ultra, as an owner's would.
func TestServe_DemoNamesMainsPlanInItsConfig(t *testing.T) {
	f := newServeFixture(t)
	f.start(func(o *cli.ServeOptions) { o.Demo = true })

	plans := make(map[string]string)
	for _, a := range f.snapshot().Accounts {
		plans[a.Label] = a.Plan
	}
	if want := map[string]string{"Main": "Ultra", "B": "Max 20x", "API": ""}; !maps.Equal(plans, want) {
		t.Errorf("demo plans %v, want %v", plans, want)
	}
}

func TestServe_LogsStartupShutdownAndFailuresOncePerMinute(t *testing.T) {
	f := newServeFixture(t)
	f.claude.fail = fmt.Errorf("locate claude: executable file not found")
	r := f.start()
	waitFor(t, "a failed poll", func() bool { return len(f.claude.dirsCalled(agentsCommand)) > 0 })
	f.waitArmed(30 * time.Second)

	// Slow polls every 30 s fail the same way; one line a minute says so.
	for range 3 {
		calls := len(f.claude.history())
		f.clock.Advance(30 * time.Second)
		waitFor(t, "the next poll", func() bool { return len(f.claude.history()) > calls })
		f.waitArmed(30 * time.Second)
	}
	r.stop(t)

	log := f.logText()
	if got := strings.Count(log, "msg=\"poll failed\""); got != 2 {
		t.Errorf("logged %d poll failures over 90 s, want 2:\n%s", got, log)
	}
	for _, want := range []string{"msg=\"loadline started\"", fmt.Sprintf("port=%d", f.port), "version=", "msg=\"loadline stopping\"", "probe failed"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, string(f.token())) {
		t.Error("log holds the ingest token")
	}
}

// A demo server takes none of the real hooks' events: they would show as
// made up, and the hooks, finding them taken, would not keep them for the
// real server. Refused, a hook spools its event as it does when no server
// runs.
func TestServe_DemoRefusesTheHooksSoTheySpool(t *testing.T) {
	f := newServeFixture(t)
	f.start(func(o *cli.ServeOptions) { o.Demo = true })
	client := forward.Client{BaseURL: forward.LoopbackURL(f.port), Token: f.token(), HTTP: f.http}

	err := client.Send(t.Context(), forward.HookPath, f.envelope(protocol.KindHook, f.defaultDir(), t0,
		`{"session_id":"real-1","hook_event_name":"SessionStart"}`))
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("Send to a demo server = %v, want a 503 refusal", err)
	}

	env := cli.Env{
		Getenv: func(string) string { return "" },
		Home:   f.home,
		Now:    f.clock.Now,
		Exe:    testExe,
		Spawn:  (&spawner{}).spawn,
	}
	cfg := config.Defaults()
	cfg.Port = f.port
	in := strings.NewReader(`{"session_id":"real-2","hook_event_name":"UserPromptSubmit"}`)
	if code := cli.RunHook(t.Context(), in, env, f.paths, cfg); code != 0 {
		t.Fatalf("RunHook = %d, want 0", code)
	}
	spooled, err := os.ReadDir(f.paths.SpoolDir)
	if err != nil || len(spooled) != 1 {
		t.Errorf("spool holds %d entries (%v), want the refused hook's event", len(spooled), err)
	}
	v := f.snapshot()
	if len(v.Sessions) != 5 {
		t.Errorf("demo shows %d sessions, want its own 5", len(v.Sessions))
	}
	for _, id := range []domain.SessionID{"real-1", "real-2"} {
		if _, ok := sessionByID(v, id); ok {
			t.Errorf("demo shows the real session %s", id)
		}
	}
}

// The demo never takes the place of a server quietly: on a port a server
// already holds it says so, where a second real server leaves without a word.
func TestServe_DemoOnAHeldPortFails(t *testing.T) {
	f := newServeFixture(t)
	f.start()

	var stdout, stderr bytes.Buffer
	o := f.options()
	o.Demo = true
	code := cli.Serve(t.Context(), o, cli.IO{In: strings.NewReader(""), Out: &stdout, Err: &stderr})

	want := fmt.Sprintf("loadline: port %d is in use (a Loadline server may be running); pass --port to run the demo elsewhere\n", f.port)
	if code != 1 || stderr.String() != want || stdout.Len() != 0 {
		t.Errorf("demo Serve = %d, stdout %q, stderr %q; want 1 and %q", code, stdout.String(), stderr.String(), want)
	}
}

func TestRunServe_DemoAndPortFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	contents := fmt.Sprintf(`{"port":%d,"claudePath":%q}`, freePort(t), filepath.Join(home, "no-claude"))
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- cli.RunServe(ctx, []string{"--demo", "--no-open", "--port", fmt.Sprint(port)},
			cli.IO{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard})
	}()
	client := forward.Client{BaseURL: forward.LoopbackURL(port)}
	waitFor(t, "the demo on the --port port", func() bool { return client.Healthy(t.Context()) })

	token, err := config.ReadToken(filepath.Join(home, "token"))
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}
	client.Token = token
	body, err := client.Get(t.Context(), "/api/snapshot", 32<<20)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var v tracker.View
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(v.Sessions) != 5 {
		t.Errorf("--demo served %d sessions, want the demo's 5", len(v.Sessions))
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("RunServe = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the demo did not stop")
	}
}

// The startup drain runs before the first poll, which already covers what it
// applied: it asks for no poll of its own.
func TestServe_StartupDrainAsksForNoExtraPoll(t *testing.T) {
	f := newServeFixture(t)
	f.spool(f.envelope(protocol.KindHook, f.defaultDir(), t0.Add(-time.Minute),
		`{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`))
	f.start()
	waitFor(t, "the first poll", func() bool { return len(f.claude.dirsCalled(agentsCommand)) > 0 })
	// The poll does not list s1, which ended a minute ago, so the poller
	// waits its idle interval.
	f.waitArmed(30 * time.Second)

	// A kick would arm its debounce at once beside the poller's own timer.
	time.Sleep(50 * time.Millisecond)
	if pending := f.clock.Pending(); slices.Contains(pending, 500*time.Millisecond) {
		t.Errorf("timers %v hold a debounced poll after the startup drain, want none", pending)
	}
}

// claude, pointed at a config dir that is not there, creates it, and
// Loadline only observes. So claude never runs for a dir that is missing: a
// default dir Claude Code never made, a mistyped account in the config, a
// dir an event named that has since been deleted, or a path that is a file.
// Each is logged once.
func TestServe_NeverRunsClaudeForAMissingConfigDir(t *testing.T) {
	f := newServeFixture(t)
	if err := os.Remove(string(f.defaultDir())); err != nil {
		t.Fatal(err)
	}
	existing := f.dir(".claude-b")
	f.mkdirs(existing)
	typo := f.dir(".cluade-work")
	gone := f.dir(filepath.Join("throwaway", ".claude"))
	file := f.dir(".claude-file")
	if err := os.WriteFile(string(file), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.writeConfig(`{"accounts":[{"configDir":"~/.claude-b","label":"B"},{"configDir":"~/.cluade-work","label":"Work"},` +
		`{"configDir":"~/.claude-file","label":"File"}]}`)
	r := f.start()

	waitFor(t, "the existing account polled and probed", func() bool {
		return f.claude.callsFor(existing, agentsCommand) > 0 && f.claude.callsFor(existing, authCommand) > 0
	})
	// No probe of a missing dir is tried again: the next is the existing
	// account's, six hours on.
	f.waitArmed(accounts.DefaultEvery)
	f.waitArmed(30 * time.Second)
	f.hook(gone, "s-gone", "SessionEnd")
	// The hook asks for a poll of its own, before the two on schedule.
	f.passDebounce()
	f.waitArmed(30 * time.Second)
	for range 2 {
		before := f.claude.callsFor(existing, agentsCommand)
		f.clock.Advance(30 * time.Second)
		waitFor(t, "the next round of polls", func() bool { return f.claude.callsFor(existing, agentsCommand) > before })
		f.waitArmed(30 * time.Second)
	}
	r.stop(t)

	log := f.logText()
	for _, missing := range []domain.ConfigDir{f.defaultDir(), typo, gone, file} {
		if info, err := os.Stat(string(missing)); !errors.Is(err, fs.ErrNotExist) && (err != nil || info.IsDir()) {
			t.Fatalf("%s: %v; the test needs it missing or a file", missing, err)
		}
		for _, call := range f.claude.history() {
			if call.dir == missing {
				t.Errorf("claude %s ran for %s, which does not exist", call.command, missing)
				break
			}
		}
		if got := strings.Count(log, fmt.Sprintf("subject=%s ", missing)); got != 1 {
			t.Errorf("the log names %s %d times, want once:\n%s", missing, got, log)
		}
	}
}

// A dir deleted while none of its sessions is live is no longer polled or
// probed, even once it is back, until an event names it again.
func TestServe_ADeletedDirIsDroppedUntilAnEventNamesIt(t *testing.T) {
	f := newServeFixture(t)
	b := f.dir(".claude-b")
	f.mkdirs(b)
	// The clock runs past the next probe, which an idle exit would cut short.
	f.writeConfig(`{"accounts":[{"configDir":"~/.claude-b","label":"B"}],"idleExitMinutes":0}`)
	f.start()
	waitFor(t, "the account polled and probed", func() bool {
		return f.claude.callsFor(b, agentsCommand) > 0 && f.claude.callsFor(b, authCommand) > 0
	})
	f.waitArmed(30 * time.Second)

	if err := os.RemoveAll(string(b)); err != nil {
		t.Fatal(err)
	}
	polls, probes := f.claude.callsFor(b, agentsCommand), f.claude.callsFor(b, authCommand)
	nextRound := func() {
		t.Helper()
		before := f.claude.callsFor(f.defaultDir(), agentsCommand)
		f.clock.Advance(30 * time.Second)
		waitFor(t, "the next round of polls", func() bool { return f.claude.callsFor(f.defaultDir(), agentsCommand) > before })
		f.waitArmed(30 * time.Second)
	}
	nextRound()
	f.mkdirs(b)
	nextRound()
	// Past the six hours after which both accounts were due to be probed
	// again: only the one still known is. The round of polls the jump
	// starts is held until the clock has stopped, so that the poller
	// schedules the next from there.
	defaultProbes := f.claude.callsFor(f.defaultDir(), authCommand)
	release := f.holdPolls()
	f.clock.Advance(accounts.DefaultEvery)
	waitFor(t, "the default dir probed again", func() bool { return f.claude.callsFor(f.defaultDir(), authCommand) > defaultProbes })
	release()
	f.waitArmed(30 * time.Second)
	time.Sleep(50 * time.Millisecond)
	if got := f.claude.callsFor(b, agentsCommand); got != polls {
		t.Errorf("the dropped dir was polled %d more times, want none until an event names it", got-polls)
	}
	if got := f.claude.callsFor(b, authCommand); got != probes {
		t.Errorf("the dropped dir was probed %d more times, want none until an event names it", got-probes)
	}

	f.statusLine(b, "s1")
	waitFor(t, "the named dir probed at once", func() bool { return f.claude.callsFor(b, authCommand) > probes })
	f.clock.Advance(30 * time.Second)
	waitFor(t, "the named dir polled again", func() bool { return f.claude.callsFor(b, agentsCommand) > polls })
}

// A dir deleted while a session in it is live lists nothing, as claude
// would say of it, without claude being run: the session ends once it has
// been missing and silent for the grace, and then the dir is dropped.
func TestServe_ADeletedDirWithALiveSessionListsNothing(t *testing.T) {
	f := newServeFixture(t)
	b := f.dir(".claude-b")
	f.mkdirs(b)
	f.writeConfig(`{"accounts":[{"configDir":"~/.claude-b"}]}`)
	f.claude.setAgents(b, `[{"sessionId":"s1","kind":"interactive","pid":4242,"status":"busy","cwd":"/x","startedAt":1795299000000}]`)
	f.start()
	f.hook(b, "s1", "UserPromptSubmit")
	waitFor(t, "s1 working", func() bool {
		s, ok := sessionByID(f.snapshot(), "s1")
		return ok && s.State == domain.StateWorking
	})
	// The first round of polls is over, so its poll of b ran while b was
	// there.
	f.waitArmed(poll.DefaultFast)

	if err := os.RemoveAll(string(b)); err != nil {
		t.Fatal(err)
	}
	polls := f.claude.callsFor(b, agentsCommand)
	// The poll the hook asked for, then a minute and a half of rounds: the
	// clock moves to each once the poller has scheduled it.
	end := f.clock.Now().Add(90 * time.Second)
	f.passDebounce()
	for f.clock.Now().Before(end) {
		f.clock.Advance(f.nextPollIn())
	}
	f.nextPollIn() // the last round has ended

	if s, _ := sessionByID(f.snapshot(), "s1"); s.State != domain.StateEnded || !s.Lost {
		t.Errorf("s1 is %v (lost %t) with its dir gone and no status line for over a minute, want ended and lost", s.State, s.Lost)
	}
	if got := f.claude.callsFor(b, agentsCommand); got != polls {
		t.Errorf("claude ran %d more times for the deleted dir, want none", got-polls)
	}
}

// The tracker moves its times back when the system clock is set back, and
// the server's log says so.
func TestServe_LogsTheClockBeingSetBack(t *testing.T) {
	f := newServeFixture(t)
	var mu sync.Mutex
	var offset time.Duration
	f.start(func(o *cli.ServeOptions) {
		o.Clock = func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return f.clock.Now().Add(offset)
		}
	})
	f.snapshot()

	mu.Lock()
	offset = -time.Hour
	mu.Unlock()
	f.snapshot()

	if log := f.logText(); !strings.Contains(log, "system clock was set back") {
		t.Errorf("the log does not mention the clock being set back:\n%s", log)
	}
}

// The server says where it listens: the loopback address, on its port.
func TestServe_ListensOnTheLoopbackAddress(t *testing.T) {
	f := newServeFixture(t)

	r := f.start()

	if want := fmt.Sprintf("127.0.0.1:%d", f.port); r.addr != want {
		t.Errorf("Serve listens on %q, want %q", r.addr, want)
	}
}

// A header block over 64 KiB is refused before any handler sees it.
func TestServe_RefusesAnOversizedHeaderBlock(t *testing.T) {
	f := newServeFixture(t)
	f.start()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.url("/healthz"), http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Padding", strings.Repeat("a", 80<<10))

	resp, err := f.http.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("status %d for an 80 KiB header, want 431", resp.StatusCode)
	}
}

// A login code signs a browser in, so the log must never hold one, whether
// it was handed out or redeemed.
func TestServe_LogNeverHoldsALoginCode(t *testing.T) {
	f := newServeFixture(t)
	r := f.start()
	var codes []string
	for range 2 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.url("/api/login-code"), http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Loadline-Token", string(f.token()))
		resp, err := f.http.Do(req)
		if err != nil {
			t.Fatalf("POST /api/login-code: %v", err)
		}
		var body struct {
			Code string `json:"code"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if err != nil || body.Code == "" {
			t.Fatalf("login code: %v, %+v", err, body)
		}
		codes = append(codes, body.Code)
	}
	redeem, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.url("/login?code="+codes[0]), http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := f.http.Do(redeem); err == nil {
		_ = resp.Body.Close()
	}
	r.stop(t)

	log := f.logText()
	for _, code := range codes {
		if strings.Contains(log, code) {
			t.Errorf("the log holds a login code it handed out:\n%s", log)
		}
	}
	// A code is 32 hex digits; so is half the token. No such run belongs in
	// the log.
	if secret := regexp.MustCompile(`[0-9a-f]{32}`).FindString(log); secret != "" {
		t.Errorf("the log holds %s, which looks like a login code or the token:\n%s", secret, log)
	}
}

const (
	// removedMessage and unremovedMessage are what the server logs of the
	// binaries earlier upgrades moved aside.
	removedMessage   = "removed a binary an earlier upgrade replaced"
	unremovedMessage = "could not remove the binaries earlier upgrades replaced"
)

// plantAside puts the installed binary in the fixture's data directory, and
// two that earlier upgrades moved aside, and returns their paths.
func (f *serveFixture) plantAside() (installed string, aside []string) {
	f.t.Helper()
	installed = filepath.FromSlash(installedExe(f.paths.DataDir))
	aside = []string{installed + ".old", installed + ".old-1"}
	if err := os.MkdirAll(filepath.Dir(installed), 0o700); err != nil {
		f.t.Fatal(err)
	}
	for _, path := range append([]string{installed}, aside...) {
		if err := os.WriteFile(path, []byte("a loadline binary"), 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
	return installed, aside
}

// loggedPath reports whether log holds a line with msg that ends with the
// attribute path=path, written as slog writes it.
func loggedPath(log, msg, path string) bool {
	for line := range strings.SplitSeq(log, "\n") {
		if strings.Contains(line, `msg="`+msg+`"`) &&
			(strings.HasSuffix(line, " path="+path) || strings.HasSuffix(line, " path="+strconv.Quote(path))) {
			return true
		}
	}
	return false
}

// An upgrade on Windows moves the running binary aside. The next server to
// start, once nothing runs that binary, removes it and logs so.
func TestServe_RemovesBinariesAnUpgradeMovedAside(t *testing.T) {
	f := newServeFixture(t)
	installed, aside := f.plantAside()

	f.start().stop(t)

	log := f.logText()
	for _, path := range aside {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still there (%v)", path, err)
		}
		if !loggedPath(log, removedMessage, path) {
			t.Errorf("log does not say it removed %s:\n%s", path, log)
		}
	}
	if _, err := os.Lstat(installed); err != nil {
		t.Errorf("the server removed the installed binary: %v", err)
	}
	if strings.Contains(log, "level=WARN") {
		t.Errorf("log warns:\n%s", log)
	}
}

// Only a server that serves this machine tidies its data directory. A demo
// serves a made-up one, and a server that finds the port taken by another
// leaves at once, to that one.
func TestServe_RemovesNothingInADemoOrAfterLosingThePortRace(t *testing.T) {
	check := func(t *testing.T, aside []string) {
		t.Helper()
		for _, path := range aside {
			if _, err := os.Lstat(path); err != nil {
				t.Errorf("%s was removed: %v", path, err)
			}
		}
	}

	t.Run("demo", func(t *testing.T) {
		f := newServeFixture(t)
		_, aside := f.plantAside()

		f.start(func(o *cli.ServeOptions) { o.Demo = true }).stop(t)

		check(t, aside)
	})

	t.Run("lost the port race", func(t *testing.T) {
		f := newServeFixture(t)
		f.start()
		_, aside := f.plantAside()

		var stdout, stderr bytes.Buffer
		code := cli.Serve(t.Context(), f.options(), cli.IO{In: strings.NewReader(""), Out: &stdout, Err: &stderr})

		if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Errorf("second Serve = %d, stdout %q, stderr %q; want 0 and no output", code, stdout.String(), stderr.String())
		}
		check(t, aside)
	})
}

// Housekeeping never keeps the server from starting: what it cannot do is
// a warning in the log.
func TestServe_WarnsOfWhatItCannotRemoveAndServesAnyway(t *testing.T) {
	f := newServeFixture(t)
	_, aside := f.plantAside()
	failure := errors.New("the disk is failing")
	cli.FailAsideHousekeeping(t, failure)

	r := f.start()
	f.snapshot()
	if code := r.stop(t); code != 0 {
		t.Errorf("Serve = %d, want 0", code)
	}

	log := f.logText()
	warned := false
	for line := range strings.SplitSeq(log, "\n") {
		warned = warned || strings.Contains(line, "level=WARN") && strings.Contains(line, `msg="`+unremovedMessage+`"`) &&
			strings.Contains(line, failure.Error())
	}
	if !warned {
		t.Errorf("log does not warn of the failure:\n%s", log)
	}
	for _, path := range aside {
		if !loggedPath(log, removedMessage, path) {
			t.Errorf("log does not say it removed %s, as it did:\n%s", path, log)
		}
	}
}

// post posts to path with the ingest token and no body.
func (f *serveFixture) post(path string) *http.Response {
	f.t.Helper()
	req, err := http.NewRequestWithContext(f.t.Context(), http.MethodPost, f.url(path), http.NoBody)
	if err != nil {
		f.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Loadline-Token", string(f.token()))
	resp, err := f.http.Do(req)
	if err != nil {
		f.t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// subagentStart sends the hook of a subagent agent starting in session.
func (f *serveFixture) subagentStart(dir domain.ConfigDir, session, agent string) {
	f.t.Helper()
	payload := fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SubagentStart","agent_id":%q,"agent_type":"general-purpose"}`, session, agent)
	f.send(forward.HookPath, f.envelope(protocol.KindHook, dir, f.clock.Now(), payload))
}

// subagentsOf returns how many subagents the server counts in session.
func (f *serveFixture) subagentsOf(session domain.SessionID) int {
	f.t.Helper()
	s, ok := sessionByID(f.snapshot(), session)
	if !ok {
		f.t.Fatalf("the server shows no session %s", session)
	}
	return s.Subagents
}

// assertHistoryClosed fails unless the history database can be opened with
// SQLite's exclusive locking, which no other connection may have open: a
// connection in write-ahead logging mode holds a shared lock on the file for
// as long as it is open.
func assertHistoryClosed(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)&_pragma=locking_mode(EXCLUSIVE)")
	if err != nil {
		t.Fatalf("open the history: %v", err)
	}
	defer func() { _ = db.Close() }()
	var objects int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema").Scan(&objects); err != nil {
		t.Errorf("the history cannot be opened exclusively, so something still has it open: %v", err)
	}
}

// A stop request takes the path of any other stop: everything shuts down
// gracefully, and the history is written and closed, before Serve returns.
func TestServe_StopsWhenAsked(t *testing.T) {
	f := newServeFixture(t)
	r := f.start()
	f.hook(f.defaultDir(), "s1", "SessionStart")

	resp := f.post("/api/stop")
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if want := fmt.Sprintf(`{"stopping":true,"pid":%d}`, os.Getpid()); err != nil || resp.StatusCode != http.StatusOK || string(body) != want {
		t.Errorf("POST /api/stop = %d %q (%v), want 200 %s", resp.StatusCode, body, err, want)
	}
	if code := r.returned(t); code != 0 {
		t.Errorf("Serve = %d, want 0", code)
	}
	if log := f.logText(); !strings.Contains(log, `msg="loadline stopping" reason="asked to stop"`) || !strings.Contains(log, `msg="loadline stopped"`) {
		t.Errorf("the log does not say the server stopped because it was asked to:\n%s", log)
	}
	assertHistoryClosed(t, f.paths.DBFile)
}

// holdHistoryClose returns a BeforeHistoryClose that says it has been
// reached, by closing reached, and holds the close until release is closed.
func holdHistoryClose(reached, release chan struct{}) func(*cli.ServeOptions) {
	return func(o *cli.ServeOptions) {
		o.BeforeHistoryClose = func() {
			close(reached)
			<-release
		}
	}
}

// onClock runs a server on a clock of its own.
func onClock(clock *clocktest.Clock) func(*cli.ServeOptions) {
	return func(o *cli.ServeOptions) {
		o.Clock = clock.Now
		o.Ticker = clock
	}
}

// awaitClosed waits until ch is closed.
func awaitClosed(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(serveStartPatience):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// A server stops answering well before it exits: it closes its listener
// first, and writes and closes the history last. A server that starts in
// between, as one a hook starts does, takes the port at once, but must not
// read the history until the old one has closed it, or it misses what the
// old one wrote last.
func TestServe_ANewServerWaitsForTheOldOneToCloseItsHistory(t *testing.T) {
	// begin runs a first server whose history close is held, has it apply
	// a subagent's start as the last event before it is asked to stop, and
	// asks it. It returns once the first server's listener has closed and
	// its history is about to close, with two binaries an upgrade moved
	// aside planted, and a second server started on the same paths, on a
	// clock of its own, and waiting.
	begin := func(t *testing.T) (f *serveFixture, first, second *running, clock *clocktest.Clock, release chan struct{}, aside []string) {
		t.Helper()
		f = newServeFixture(t)
		reached, release := make(chan struct{}), make(chan struct{})
		t.Cleanup(func() {
			select {
			case <-release:
			default:
				close(release)
			}
		})
		first = f.start(holdHistoryClose(reached, release))
		f.hook(f.defaultDir(), "s1", "SessionStart")
		f.subagentStart(f.defaultDir(), "s1", "agent-1")
		if n := f.subagentsOf("s1"); n != 1 {
			t.Fatalf("the first server counts %d subagents, want 1", n)
		}

		first.cancel()
		awaitClosed(t, "the first server's history close", reached)
		_, aside = f.plantAside()
		clock = clocktest.New(t0)
		second = f.launch(onClock(clock))
		clock.WaitForTimers(t, 2)
		return f, first, second, clock, release, aside
	}

	t.Run("the old one closes it within the wait", func(t *testing.T) {
		f, first, second, clock, release, _ := begin(t)
		for range 3 {
			clock.Advance(clock.Pending()[0])
			clock.WaitForTimers(t, 2)
			if second.listening() {
				t.Fatal("the second server listens while the first still holds the history")
			}
		}

		close(release)
		if code := first.returned(t); code != 0 {
			t.Errorf("the first Serve = %d, want 0", code)
		}
		clock.Advance(clock.Pending()[0])
		second.awaitReady(t)

		log := f.logText()
		stopped := strings.Index(log, `msg="loadline stopped"`)
		restarted := strings.LastIndex(log, `msg="loadline started"`)
		restored := strings.LastIndex(log, `msg="history restored"`)
		if stopped < 0 || restarted < stopped || restored < restarted {
			t.Errorf("the log does not show the second server starting, and restoring, only after the first closed the history:\n%s", log)
		}
		if n := f.subagentsOf("s1"); n != 1 {
			t.Errorf("the second server counts %d subagents in s1, want the 1 the first applied last", n)
		}
	})

	t.Run("the old one holds it past the wait", func(t *testing.T) {
		f, first, second, clock, release, aside := begin(t)

		clock.Advance(cli.LockWait)

		if code := second.returned(t); code != 1 || second.stderr.String() != "loadline: another server still holds the data directory\n" {
			t.Errorf("the second Serve = %d, stderr %q; want 1 and the data directory named as held", code, second.stderr.String())
		}
		close(release)
		if code := first.returned(t); code != 0 {
			t.Errorf("the first Serve = %d, want 0", code)
		}
		log := f.logText()
		if n := strings.Count(log, `msg="loadline started"`); n != 1 {
			t.Errorf("the log shows %d servers starting, want the first alone:\n%s", n, log)
		}
		if n := strings.Count(log, `msg="history restored"`); n != 1 {
			t.Errorf("the log shows the history restored %d times, want by the first alone:\n%s", n, log)
		}
		if !strings.Contains(log, `msg="loadline could not start" err="another server still holds the data directory"`) {
			t.Errorf("the log does not say why the second server did not start:\n%s", log)
		}
		// Tidying the data directory is for a server that starts, as it is
		// for one that wins the port.
		for _, path := range aside {
			if _, err := os.Lstat(path); err != nil {
				t.Errorf("%s was removed by a server that did not start: %v", path, err)
			}
		}
	})

	// A server stopped as it waits, as by Ctrl+C, leaves once the lock is
	// its own, opening nothing.
	t.Run("the new one is stopped while it waits", func(t *testing.T) {
		f, first, second, clock, release, _ := begin(t)

		second.cancel()
		close(release)
		if code := first.returned(t); code != 0 {
			t.Errorf("the first Serve = %d, want 0", code)
		}
		clock.Advance(clock.Pending()[0])

		if code := second.returned(t); code != 0 || second.stderr.String() != "" {
			t.Errorf("the second Serve = %d, stderr %q; want 0 and nothing", code, second.stderr.String())
		}
		if n := strings.Count(f.logText(), `msg="loadline started"`); n != 1 {
			t.Errorf("the log shows %d servers starting, want the first alone:\n%s", n, f.logText())
		}
	})

	// Stopped, it has nothing to report when the lock stays held past the
	// wait: it was not going to start.
	t.Run("the new one is stopped while the old one holds on", func(t *testing.T) {
		f, first, second, clock, release, _ := begin(t)

		second.cancel()
		clock.Advance(cli.LockWait)

		if code := second.returned(t); code != 0 || second.stderr.String() != "" {
			t.Errorf("the second Serve = %d, stderr %q; want 0 and nothing", code, second.stderr.String())
		}
		close(release)
		if code := first.returned(t); code != 0 {
			t.Errorf("the first Serve = %d, want 0", code)
		}
		if log := f.logText(); strings.Contains(log, "could not start") {
			t.Errorf("the log says the stopped server could not start:\n%s", log)
		}
	})
}

// A spare server, as a hook starts one, that finds the port taken by a
// server still starting or stopping, which answers nothing yet or any more,
// leaves quietly, as it does when that server answers: that server holds the
// history's lock.
func TestServe_LeavesQuietlyWhileAnotherServerStartsOrStops(t *testing.T) {
	f := newServeFixture(t)
	lock, err := serverlock.Acquire(f.paths.ServerLock, 0, nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	silent, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(f.port)))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = silent.Close() })

	var stdout, stderr bytes.Buffer
	code := cli.Serve(t.Context(), f.options(), cli.IO{In: strings.NewReader(""), Out: &stdout, Err: &stderr})

	if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("Serve = %d, stdout %q, stderr %q; want 0 and no output", code, stdout.String(), stderr.String())
	}
}

// A data directory on a file system that takes no lock, as some network
// shares do, served before servers took one there: the server still starts,
// and says in its log that servers are not kept from the history there.
func TestServe_RunsWithoutTheLockWhereTheFileSystemTakesNone(t *testing.T) {
	f := newServeFixture(t)
	cli.LocksUnsupported(t)

	r := f.start()
	f.hook(f.defaultDir(), "s1", "SessionStart")
	if code := r.stop(t); code != 0 {
		t.Errorf("Serve = %d, want 0", code)
	}

	log := f.logText()
	warned := false
	for line := range strings.SplitSeq(log, "\n") {
		warned = warned || strings.Contains(line, "level=WARN") && strings.Contains(line, "takes no lock")
	}
	if !warned || !strings.Contains(log, `msg="history restored"`) {
		t.Errorf("the log does not warn of the missing lock, or the server kept no history:\n%s", log)
	}
}
