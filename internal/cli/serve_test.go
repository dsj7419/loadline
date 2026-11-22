package cli_test

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	"github.com/dsj7419/loadline/internal/protocol"
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
	stdout *syncBuffer
	stderr *syncBuffer
	// addr is the address Serve said it listens on.
	addr string
}

// start runs Serve with the fixture's options, as edit changes them, and
// waits until it listens. The test's end stops it and waits for it.
func (f *serveFixture) start(edit ...func(*cli.ServeOptions)) *running {
	f.t.Helper()
	o := f.options()
	for _, e := range edit {
		e(&o)
	}
	ready := make(chan string, 1)
	o.Ready = func(addr string) { ready <- addr }
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, done: make(chan int, 1), stdout: &syncBuffer{}, stderr: &syncBuffer{}}
	go func() { r.done <- cli.Serve(ctx, o, cli.IO{In: strings.NewReader(""), Out: r.stdout, Err: r.stderr}) }()
	f.t.Cleanup(func() { r.stop(f.t) })

	select {
	case r.addr = <-ready:
	case code := <-r.done:
		r.done <- code
		f.t.Fatalf("Serve returned %d before it listened; stderr %q", code, r.stderr.String())
	case <-time.After(serveStartPatience):
		f.t.Fatalf("Serve did not listen within %v", serveStartPatience)
	}
	return r
}

// serveStartPatience is how long start waits for Serve to listen. How soon
// the server starts is no test's concern, and a loaded machine with the
// race detector on can take seconds to open the history and restore it.
const serveStartPatience = 30 * time.Second

// stop ends the server's context and returns its exit code.
func (r *running) stop(t *testing.T) int {
	t.Helper()
	r.cancel()
	select {
	case code := <-r.done:
		r.done <- code
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after its context ended")
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

	elapsed := advanceUntilExit(t, f.clock, r, 10*time.Second, 5*time.Minute)

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

	for range 30 {
		f.clock.Advance(10 * time.Second)
		if _, done := r.exited(); done {
			t.Fatal("exited with a session live")
		}
	}

	f.hook(f.defaultDir(), "s1", "SessionEnd")
	f.claude.setAgents(f.defaultDir(), `[]`)
	advanceUntilExit(t, f.clock, r, 10*time.Second, 5*time.Minute)
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

// advanceUntilExit moves the clock step by step until Serve returns, and
// returns how far it moved. It fails the test once the clock has moved
// limit without Serve returning.
func advanceUntilExit(t *testing.T, clock *clocktest.Clock, r *running, step, limit time.Duration) time.Duration {
	t.Helper()
	var elapsed time.Duration
	for elapsed < limit {
		clock.Advance(step)
		elapsed += step
		deadline := time.After(20 * time.Millisecond)
	wait:
		for {
			select {
			case code := <-r.done:
				r.done <- code
				return elapsed
			case <-deadline:
				break wait
			}
		}
	}
	t.Fatalf("still serving after %v", limit)
	return elapsed
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
	// again: only the one still known is.
	defaultProbes := f.claude.callsFor(f.defaultDir(), authCommand)
	f.clock.Advance(accounts.DefaultEvery)
	waitFor(t, "the default dir probed again", func() bool { return f.claude.callsFor(f.defaultDir(), authCommand) > defaultProbes })
	time.Sleep(50 * time.Millisecond)
	if got := f.claude.callsFor(b, agentsCommand); got != polls {
		t.Errorf("the dropped dir was polled %d more times, want none until an event names it", got-polls)
	}
	if got := f.claude.callsFor(b, authCommand); got != probes {
		t.Errorf("the dropped dir was probed %d more times, want none until an event names it", got-probes)
	}

	f.statusLine(b, "s1")
	waitFor(t, "the named dir probed at once", func() bool { return f.claude.callsFor(b, authCommand) > probes })
	waitFor(t, "the next poll to be scheduled", func() bool {
		pending := f.clock.Pending()
		return len(pending) > 0 && pending[0] <= 30*time.Second
	})
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

	if err := os.RemoveAll(string(b)); err != nil {
		t.Fatal(err)
	}
	polls := f.claude.callsFor(b, agentsCommand)
	for range 3 {
		before := f.claude.callsFor(f.defaultDir(), agentsCommand)
		f.clock.Advance(30 * time.Second)
		waitFor(t, "the next round of polls", func() bool { return f.claude.callsFor(f.defaultDir(), agentsCommand) > before })
	}

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
