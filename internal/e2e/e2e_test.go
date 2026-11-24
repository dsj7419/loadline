// Package e2e_test runs the whole server in-process, fed the way Claude Code
// feeds it: hooks, status lines and OpenTelemetry posted to its routes, and
// `claude agents` and `claude auth status` answered by a stand-in claude.
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/clocktest"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/hermetictest"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/tracker"
)

// The tests work below a private temporary folder of their own. The
// system's may let other accounts in, as it does on some machines, and the
// server refuses a data directory below such a folder.
//
// The run is sealed: claude on PATH is a stand-in that fails the run, and
// the home directory, Claude Code's config dir and Loadline's files are
// temporary, so that a server that loses its fake claude reaches neither the
// user's claude nor the user's account.
func TestMain(m *testing.M) {
	hermetictest.StandIn()
	root, err := os.MkdirTemp("", "loadline-e2e-")
	if err == nil {
		err = fsperm.Private(root)
	}
	for _, name := range []string{"TMP", "TEMP", "TMPDIR"} {
		if err == nil {
			err = os.Setenv(name, root)
		}
	}
	unsealedEnv = os.Environ()
	var sealed *hermetictest.Sealed
	if err == nil {
		sealed, err = hermetictest.Seal()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(root)
		os.Exit(1)
	}
	tempRoot = root
	goleak.VerifyTestMain(m, goleak.Cleanup(func(code int) {
		code = sealed.Finish(code)
		_ = os.RemoveAll(root)
		os.Exit(code)
	}))
}

// tempRoot is the private temporary folder TestMain makes, which the
// servers the tests run trust, with every folder above it.
var tempRoot string

// unsealedEnv is the test binary's environment as it was before TestMain
// sealed it, with the user's own home.
var unsealedEnv []string

const (
	// s1 is the session of the model call in the OpenTelemetry fixture.
	s1 domain.SessionID = "00000000-0000-4000-8000-000000000001"
	s2 domain.SessionID = "bbbbbbbb-0000-4000-8000-000000000002"

	otlpFixture = "../otlp/testdata/api_request.json"
)

// fixtureCallAt is when the fixture's model call was made.
var fixtureCallAt = time.Unix(0, 1795299794980000000)

// claude answers `claude agents --json --all` and `claude auth status` with
// what the test sets for each config dir.
type claude struct {
	mu     sync.Mutex
	agents map[domain.ConfigDir]string
	polls  int
}

func (c *claude) Run(_ context.Context, dir domain.ConfigDir, args ...string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch strings.Join(args, " ") {
	case "agents --json --all":
		c.polls++
		if out, ok := c.agents[dir]; ok {
			return []byte(out), nil
		}
		return []byte("[]"), nil
	case "auth status --json":
		return []byte(`{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"dev@example.com","subscriptionType":"max"}`), nil
	default:
		return nil, fmt.Errorf("unexpected claude %q", args)
	}
}

func (c *claude) list(dir domain.ConfigDir, out string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.agents[dir] = out
}

func (c *claude) pollCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.polls
}

// hangBound is how long any wait in these tests lasts before the run is
// taken to have hung. It decides nothing: each wait ends as soon as what it
// waits for holds, and a loaded machine with the race detector on can take
// seconds to start the server, open the history and restore it.
const hangBound = 30 * time.Second

// eventually polls cond until it holds, failing the test once hangBound has
// passed without it.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(hangBound)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatalf("split address: %v", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return n
}

// machine is a running server and the means to feed and read it.
type machine struct {
	t      *testing.T
	port   int
	token  []byte
	http   *http.Client
	client forward.Client
	// stop shuts the server down and waits for it.
	stop func()
}

// serve runs the server in-process until it is stopped or the test ends.
// It first writes the config the server reads, replacing any there: one
// that names the server's port and the stand-in claude besides the options,
// so that a server that loses either still serves on the test's port and
// runs no real claude.
func serve(t *testing.T, o cli.ServeOptions) *machine {
	t.Helper()
	hermetictest.WriteConfig(t, o.Paths.ConfigFile, `{}`, o.Port)
	ready := make(chan struct{})
	o.Ready = func(string) { close(ready) }
	o.TrustFrom = tempRoot
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() { done <- cli.Serve(ctx, o, cli.IO{In: strings.NewReader(""), Out: io.Discard, Err: &stderr}) }()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case code := <-done:
				if code != 0 {
					t.Errorf("Serve = %d, want 0", code)
				}
			case <-time.After(hangBound):
				t.Errorf("Serve did not stop within %v", hangBound)
			}
		})
	}
	t.Cleanup(stop)
	select {
	case <-ready:
	case code := <-done:
		t.Fatalf("Serve returned %d before listening: %s", code, stderr.String())
	case <-time.After(hangBound):
		t.Fatalf("Serve did not listen within %v", hangBound)
	}

	token, err := config.EnsureToken(o.Paths.TokenFile)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(httpClient.CloseIdleConnections)
	return &machine{
		t: t, port: o.Port, token: token, http: httpClient,
		client: forward.Client{BaseURL: forward.LoopbackURL(o.Port), Token: token, HTTP: httpClient},
		stop:   stop,
	}
}

func (m *machine) send(path string, kind protocol.Kind, dir domain.ConfigDir, at time.Time, payload string) {
	m.t.Helper()
	id, err := protocol.NewEventID()
	if err != nil {
		m.t.Fatalf("NewEventID: %v", err)
	}
	env := protocol.Envelope{
		V: protocol.SchemaVersion, ID: id, Kind: kind, At: at, ConfigDir: string(dir), PID: 4242,
		Payload: json.RawMessage(payload),
	}
	if err := m.client.Send(m.t.Context(), path, env); err != nil {
		m.t.Fatalf("send to %s: %v", path, err)
	}
}

func (m *machine) postLogs(body []byte) {
	m.t.Helper()
	req, err := http.NewRequestWithContext(m.t.Context(), http.MethodPost, forward.LoopbackURL(m.port)+"/v1/logs", bytes.NewReader(body))
	if err != nil {
		m.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Loadline-Token", string(m.token))
	resp, err := m.http.Do(req)
	if err != nil {
		m.t.Fatalf("post logs: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		m.t.Fatalf("post logs: status %d", resp.StatusCode)
	}
}

func (m *machine) snapshot() tracker.View {
	m.t.Helper()
	body, err := m.client.Get(m.t.Context(), "/api/snapshot", 32<<20)
	if err != nil {
		m.t.Fatalf("snapshot: %v", err)
	}
	var v tracker.View
	if err := json.Unmarshal(body, &v); err != nil {
		m.t.Fatalf("decode snapshot: %v", err)
	}
	return v
}

func session(v tracker.View, id domain.SessionID) (tracker.SessionView, bool) {
	for _, s := range v.Sessions {
		if s.ID == id {
			return s, true
		}
	}
	return tracker.SessionView{}, false
}

func account(v tracker.View, dir domain.ConfigDir) (tracker.AccountView, bool) {
	for _, a := range v.Accounts {
		if a.Dir == dir {
			return a, true
		}
	}
	return tracker.AccountView{}, false
}

func needsYouAlerts(v tracker.View, id domain.SessionID) int {
	n := 0
	for _, a := range v.Alerts {
		if a.Kind == domain.AlertNeedsYou && a.Session == id {
			n++
		}
	}
	return n
}

// watched describes what the waits of the end-to-end test watch in v: each
// session's state and account, and the email account A shows. A wait that
// times out names only what it waited for; this tells which of the poll of
// dirA, the poll of dirB and the probe of account A never landed.
func watched(v tracker.View, dirA domain.ConfigDir) string {
	describe := func(id domain.SessionID) string {
		s, ok := session(v, id)
		if !ok {
			return "absent"
		}
		return fmt.Sprintf("%s under %s", s.State, s.Dir)
	}
	email := "absent"
	if a, ok := account(v, dirA); ok {
		email = fmt.Sprintf("%q", a.Email)
	}
	return fmt.Sprintf("s1 %s, s2 %s, account A's email %s", describe(s1), describe(s2), email)
}

func TestE2E_HookStatusOTLPAndPollProduceView(t *testing.T) {
	t.Setenv("LOADLINE_HOME", t.TempDir())
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	home := t.TempDir()
	dirA := config.ResolveConfigDir("", home)
	dirB := config.ResolveConfigDir(filepath.Join(home, ".claude-b"), home)
	// Claude Code has run under both accounts, so their config dirs exist.
	for _, dir := range []domain.ConfigDir{dirA, dirB} {
		if err := os.MkdirAll(string(dir), 0o700); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	logs, err := os.ReadFile(otlpFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	t0 := fixtureCallAt.Add(time.Minute)
	clock := clocktest.New(t0)
	fake := &claude{agents: make(map[domain.ConfigDir]string)}

	// 1. The server, polling a claude that lists nothing yet. That first
	// answer may be applied after the feeds below, and then changes nothing:
	// s1 is too new for an absence to end it.
	m := serve(t, cli.ServeOptions{
		Paths: paths, Home: home, Port: freePort(t), NoOpen: true, Runner: fake, Clock: clock.Now,
	})
	eventually(t, "the first poll", func() bool { return fake.pollCount() > 0 })

	// 2. What the session's process reports from now on: s1 waits on a
	// permission prompt and s2 is busy.
	fake.list(dirA, `[{"sessionId":"`+string(s1)+`","kind":"interactive","pid":4242,"status":"waiting",`+
		`"waitingFor":"permission prompt","cwd":"/work/demo","startedAt":1795299700000}]`)
	fake.list(dirB, `[{"sessionId":"`+string(s2)+`","kind":"interactive","pid":4343,"status":"busy",`+
		`"cwd":"/work/other","startedAt":1795299700000}]`)

	// 3. The feeds, stamped a second before the polls that follow them. A
	// round polls the accounts the server knows as it starts, so the hook
	// goes only once the server shows s2 under its account: the round the
	// hook asks for then polls dirB as well as dirA.
	fedAt := t0
	clock.Advance(time.Second)
	// What the waits below last saw goes to the log if the test fails, since
	// a wait that times out says nothing of the state.
	var lastSeen string
	defer func() {
		if t.Failed() && lastSeen != "" {
			t.Logf("the last wait saw %s", lastSeen)
		}
	}()
	m.send(forward.StatusPath, protocol.KindStatus, dirB, fedAt,
		`{"session_id":"`+string(s2)+`","model":{"id":"claude-sonnet-5-5","display_name":"Sonnet 5.5"}}`)
	eventually(t, "s2's status line", func() bool {
		snapshot := m.snapshot()
		lastSeen = watched(snapshot, dirA)
		s, ok := session(snapshot, s2)
		return ok && s.Dir == dirB
	})
	m.send(forward.HookPath, protocol.KindHook, dirA, fedAt,
		`{"session_id":"`+string(s1)+`","hook_event_name":"SessionStart","source":"startup","cwd":"/work/demo"}`)
	m.send(forward.StatusPath, protocol.KindStatus, dirA, fedAt,
		`{"session_id":"`+string(s1)+`","model":{"id":"claude-opus-5-5","display_name":"Opus 5.5"},`+
			`"context_window":{"used_percentage":31},"rate_limits":{"seven_day":{"used_percentage":42,"resets_at":1795400000}}}`)
	m.postLogs(logs)

	// 4. The hook asked for a poll, which finds s1 waiting and s2 busy. The
	// round applies each account's answer as it comes, and the prober asks
	// who account A is on its own schedule, with the model call's email
	// standing in until then, so one snapshot must show all three.
	var v tracker.View
	eventually(t, "s1 to need the user, s2 to work and account A's probed email", func() bool {
		v = m.snapshot()
		lastSeen = watched(v, dirA)
		first, firstOK := session(v, s1)
		second, secondOK := session(v, s2)
		a, aOK := account(v, dirA)
		return firstOK && first.State == domain.StateNeedsYou &&
			secondOK && second.State == domain.StateWorking &&
			aOK && a.Email == "dev@example.com"
	})
	if len(v.Accounts) != 2 || v.Accounts[0].Dir != dirA || v.Accounts[1].Dir != dirB {
		t.Errorf("accounts %+v, want %s and %s", v.Accounts, dirA, dirB)
	}
	if a, _ := account(v, dirA); a.SevenDay == nil || a.SevenDay.UsedPct != 42 {
		t.Errorf("account A = %+v, want its status line's week", a)
	}
	first, _ := session(v, s1)
	if first.WaitReason != "permission prompt" || first.Model != "Opus 5.5" || first.ContextPct != 31 {
		t.Errorf("s1 = %+v, want it waiting on a permission prompt, on Opus 5.5 at 31%% context", first)
	}
	if second, _ := session(v, s2); second.Dir != dirB {
		t.Errorf("s2 = %+v, want it under %s", second, dirB)
	}
	if len(v.Models) == 0 || v.Models[0].Requests == 0 || v.Models[0].CostUSD <= 0 || len(first.Models) == 0 {
		t.Errorf("models %+v, s1's %+v; want the fixture's model call counted", v.Models, first.Models)
	}
	if n := needsYouAlerts(v, s1); n != 0 {
		t.Errorf("%d needs-you alerts for s1 at once, want none before 20 s", n)
	}

	// 5. Once s1 has waited past 20 s, it alerts. A snapshot evaluates the
	// alerts at the clock's time, and a poll that comes meanwhile finds s1
	// still waiting, which keeps when its wait began.
	clock.Advance(19 * time.Second)
	if n := needsYouAlerts(m.snapshot(), s1); n != 0 {
		t.Errorf("%d needs-you alerts after 19 s, want none", n)
	}
	clock.Advance(2 * time.Second)
	if n := needsYouAlerts(m.snapshot(), s1); n != 1 {
		t.Errorf("%d needs-you alerts after 21 s, want 1", n)
	}
}
