package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/server"
	"github.com/dsj7419/loadline/internal/tracker"
)

// The hook and tap start the server as "<executable> serve". In these tests
// the executable is the test binary, which must then exit at once instead
// of running the tests again. Otherwise every server a test starts must be
// gone, goroutines and all, by the end of the run.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		os.Exit(0)
	}
	goleak.VerifyTestMain(m)
}

// canary is planted in every content field of the test payloads. It must
// never leave the process.
const canary = "CANARY-c0ffee"

var t0 = time.Date(2026, time.November, 21, 18, 0, 0, 0, time.UTC)

// raceSlowdown is how much longer the race detector may make the hot path
// take.
const raceSlowdown = 5

// hotPathBound is d, a wall-clock bound on the hot path, as strict as the
// plan sets it without the race detector and raceSlowdown times longer with
// it, so that a race-enabled run checks the bound without failing on the
// detector's cost.
func hotPathBound(d time.Duration) time.Duration {
	if raceEnabled {
		return raceSlowdown * d
	}
	return d
}

// postWindow replaces the tap's 100 ms and the hook's 300 ms bound on their
// post in tests that count what reaches the server but are not about that
// bound: a loaded machine, or a command such as PowerShell starting, must
// not make the post miss its window.
const postWindow = 30 * time.Second

// probeWindow replaces the hook's 150 ms bound on its check that a server
// answers, in tests that need the check to find a running server but are
// not about that bound.
const probeWindow = 30 * time.Second

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// spawner records the processes the command under test starts.
type spawner struct {
	mu    sync.Mutex
	calls []string
}

func (s *spawner) spawn(exe string, args ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, strings.Join(append([]string{exe}, args...), " "))
	return nil
}

func (s *spawner) started() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

const testExe = "loadline-under-test"

// fixture is one hook or tap invocation's world: its Loadline directories,
// configuration, environment, clock and spawns.
type fixture struct {
	t      *testing.T
	paths  config.Paths
	cfg    config.Config
	clock  *fakeClock
	spawns *spawner
	home   string
	vars   map[string]string
	// healthTimeout is the Env's bound on the hook's check that a server
	// answers; zero leaves the hook's own.
	healthTimeout time.Duration
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	return &fixture{
		t: t,
		paths: config.Paths{
			DataDir:    filepath.Join(dir, "data"),
			ConfigFile: filepath.Join(dir, "config", "config.json"),
			TokenFile:  filepath.Join(dir, "data", "token"),
			SpoolDir:   filepath.Join(dir, "data", "spool"),
			DBFile:     filepath.Join(dir, "data", "loadline.db"),
		},
		cfg:    config.Defaults(),
		clock:  &fakeClock{now: t0},
		spawns: &spawner{},
		home:   home,
		vars: map[string]string{
			"CLAUDE_PID":        "4242",
			"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude-b"),
		},
	}
}

func (f *fixture) env() cli.Env {
	return cli.Env{
		Getenv:        func(name string) string { return f.vars[name] },
		Home:          f.home,
		Now:           f.clock.Now,
		Exe:           testExe,
		Spawn:         f.spawns.spawn,
		HealthTimeout: f.healthTimeout,
	}
}

// wantConfigDir is the config dir every envelope of the fixture names.
func (f *fixture) wantConfigDir() string {
	return string(config.ResolveConfigDir(f.vars["CLAUDE_CONFIG_DIR"], f.home))
}

func (f *fixture) token() []byte {
	f.t.Helper()
	token, err := config.EnsureToken(f.paths.TokenFile)
	if err != nil {
		f.t.Fatalf("EnsureToken: %v", err)
	}
	return token
}

// serverDown points the fixture at a loopback port nothing listens on.
func (f *fixture) serverDown() {
	f.t.Helper()
	l, err := (&net.ListenConfig{}).Listen(f.t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		f.t.Fatalf("listen: %v", err)
	}
	f.cfg.Port = portOf(f.t, l.Addr())
	if err := l.Close(); err != nil {
		f.t.Fatalf("close listener: %v", err)
	}
}

func portOf(tb testing.TB, addr net.Addr) int {
	tb.Helper()
	_, portText, err := net.SplitHostPort(addr.String())
	if err != nil {
		tb.Fatalf("split address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		tb.Fatalf("parse port: %v", err)
	}
	return port
}

// spooled returns the envelopes in the fixture's spool, leaving them there.
func (f *fixture) spooled() []protocol.Envelope {
	f.t.Helper()
	entries, err := os.ReadDir(f.paths.SpoolDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		f.t.Fatalf("read spool: %v", err)
	}
	var envelopes []protocol.Envelope
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.paths.SpoolDir, entry.Name()))
		if err != nil {
			f.t.Fatalf("read spool file: %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			f.t.Fatalf("decode spool file %s: %v", entry.Name(), err)
		}
		envelopes = append(envelopes, env)
	}
	return envelopes
}

// post is one ingest request a test server received.
type post struct {
	path     string
	raw      []byte
	envelope protocol.Envelope
}

// ingestServer is the real server on a free loopback port, fed into a
// recording sink, that also keeps the raw ingest requests it receives. It
// can hold or refuse those requests instead of serving them.
type ingestServer struct {
	sink *recordingSink
	// answer, when set, is the status every ingest request gets.
	answer int
	// hold, when set, is how long each ingest request waits for an answer
	// that never comes.
	hold time.Duration
	// slowHealth, when set, is how long every other request, such as the
	// health check, waits before it is answered.
	slowHealth time.Duration

	mu    sync.Mutex
	posts []post
}

// serve starts an ingest server for the fixture, configured by opts, and
// points the fixture at it.
func (f *fixture) serve(opts ...func(*ingestServer)) *ingestServer {
	f.t.Helper()
	s := &ingestServer{sink: &recordingSink{}}
	for _, opt := range opts {
		opt(s)
	}
	ts := httptest.NewUnstartedServer(nil)
	f.t.Cleanup(ts.Close)
	port := portOf(f.t, ts.Listener.Addr())
	loadline := server.New(server.Deps{State: s.sink, Token: f.token(), Port: port}).Handler()
	release := make(chan struct{})
	// wait holds r for d, and reports false when its client or the test
	// gave up first.
	wait := func(r *http.Request, d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-r.Context().Done():
		case <-release:
		}
		return false
	}
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/ingest/") {
			if s.slowHealth == 0 || wait(r, s.slowHealth) {
				loadline.ServeHTTP(w, r)
			}
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.record(r.URL.Path, raw)
		switch {
		case s.hold > 0:
			wait(r, s.hold)
		case s.answer != 0:
			w.WriteHeader(s.answer)
		default:
			r.Body = io.NopCloser(bytes.NewReader(raw))
			loadline.ServeHTTP(w, r)
		}
	})
	ts.Start()
	// Runs before the server's Close, which waits for held requests.
	f.t.Cleanup(func() { close(release) })
	f.cfg.Port = port
	return s
}

func (s *ingestServer) record(path string, raw []byte) {
	var env protocol.Envelope
	_ = json.Unmarshal(raw, &env) // a body that is no envelope is kept raw
	s.mu.Lock()
	defer s.mu.Unlock()
	s.posts = append(s.posts, post{path: path, raw: raw, envelope: env})
}

func (s *ingestServer) received() []post {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]post(nil), s.posts...)
}

// recordingSink is the live state behind the test server.
type recordingSink struct {
	mu     sync.Mutex
	events []domain.Event
}

func (s *recordingSink) Apply(e domain.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *recordingSink) RecordAPI(domain.APIRequest) {}

func (s *recordingSink) Snapshot() tracker.View { return tracker.View{} }

func (s *recordingSink) Subscribe() (<-chan tracker.View, func()) {
	return make(chan tracker.View), func() {}
}

func (s *recordingSink) applied() []domain.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Event(nil), s.events...)
}

// payloadFields decodes an envelope's payload into its top-level fields.
func payloadFields(t *testing.T, env protocol.Envelope) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &fields); err != nil {
		t.Fatalf("decode payload %s: %v", env.Payload, err)
	}
	return fields
}

// assertEnvelope checks what every envelope the fixture sends must carry.
func (f *fixture) assertEnvelope(t *testing.T, env protocol.Envelope, kind protocol.Kind) {
	t.Helper()
	if err := env.Validate(); err != nil {
		t.Errorf("envelope is invalid: %v", err)
	}
	if env.Kind != kind || env.V != protocol.SchemaVersion {
		t.Errorf("envelope kind %q version %d, want %q version %d", env.Kind, env.V, kind, protocol.SchemaVersion)
	}
	if !env.At.Equal(f.clock.Now()) {
		t.Errorf("envelope time = %v, want the clock's %v", env.At, f.clock.Now())
	}
	if env.ConfigDir != f.wantConfigDir() {
		t.Errorf("envelope config dir = %q, want %q", env.ConfigDir, f.wantConfigDir())
	}
	if env.PID != 4242 {
		t.Errorf("envelope pid = %d, want CLAUDE_PID 4242", env.PID)
	}
}

// assertNoContent fails when raw carries anything planted as content.
func assertNoContent(t *testing.T, raw []byte) {
	t.Helper()
	if bytes.Contains(raw, []byte(canary)) {
		t.Errorf("forwarded body carries content: %.300s", raw)
	}
}

func marshal(tb testing.TB, v any) string {
	tb.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		tb.Fatalf("marshal: %v", err)
	}
	return string(b)
}
