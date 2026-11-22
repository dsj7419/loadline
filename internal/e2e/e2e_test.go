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
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/tracker"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

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

// eventually polls cond until it holds, failing the test after five seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
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
}

// serve runs the server in-process until the test ends.
func serve(t *testing.T, o cli.ServeOptions) *machine {
	t.Helper()
	ready := make(chan struct{})
	o.Ready = func(string) { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() { done <- cli.Serve(ctx, o, cli.IO{In: strings.NewReader(""), Out: io.Discard, Err: &stderr}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("Serve = %d, want 0", code)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve did not stop")
		}
	})
	select {
	case <-ready:
	case code := <-done:
		t.Fatalf("Serve returned %d before listening: %s", code, stderr.String())
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not listen")
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

func needsYouAlerts(v tracker.View, id domain.SessionID) int {
	n := 0
	for _, a := range v.Alerts {
		if a.Kind == domain.AlertNeedsYou && a.Session == id {
			n++
		}
	}
	return n
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

	// 1. The server, polling a claude that lists nothing yet.
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

	// 3. The feeds, stamped a second before the polls that follow them.
	fedAt := t0
	clock.Advance(time.Second)
	m.send(forward.HookPath, protocol.KindHook, dirA, fedAt,
		`{"session_id":"`+string(s1)+`","hook_event_name":"SessionStart","source":"startup","cwd":"/work/demo"}`)
	m.send(forward.StatusPath, protocol.KindStatus, dirA, fedAt,
		`{"session_id":"`+string(s1)+`","model":{"id":"claude-opus-5-5","display_name":"Opus 5.5"},`+
			`"context_window":{"used_percentage":31},"rate_limits":{"seven_day":{"used_percentage":42,"resets_at":1795400000}}}`)
	m.send(forward.StatusPath, protocol.KindStatus, dirB, fedAt,
		`{"session_id":"`+string(s2)+`","model":{"id":"claude-sonnet-5-5","display_name":"Sonnet 5.5"}}`)
	m.postLogs(logs)

	// 4. The hook asked for a poll, which finds s1 waiting.
	eventually(t, "s1 to need the user", func() bool {
		s, ok := session(m.snapshot(), s1)
		return ok && s.State == domain.StateNeedsYou
	})
	v := m.snapshot()
	if len(v.Accounts) != 2 || v.Accounts[0].Dir != dirA || v.Accounts[1].Dir != dirB {
		t.Errorf("accounts %+v, want %s and %s", v.Accounts, dirA, dirB)
	}
	if v.Accounts[0].SevenDay == nil || v.Accounts[0].SevenDay.UsedPct != 42 || v.Accounts[0].Email != "dev@example.com" {
		t.Errorf("account A = %+v, want its status line's week and its probed email", v.Accounts[0])
	}
	first, _ := session(v, s1)
	if first.WaitReason != "permission prompt" || first.Model != "Opus 5.5" || first.ContextPct != 31 {
		t.Errorf("s1 = %+v, want it waiting on a permission prompt, on Opus 5.5 at 31%% context", first)
	}
	if second, ok := session(v, s2); !ok || second.State != domain.StateWorking || second.Dir != dirB {
		t.Errorf("s2 = %+v, want it working under %s", second, dirB)
	}
	if len(v.Models) == 0 || v.Models[0].Requests == 0 || v.Models[0].CostUSD <= 0 || len(first.Models) == 0 {
		t.Errorf("models %+v, s1's %+v; want the fixture's model call counted", v.Models, first.Models)
	}
	if n := needsYouAlerts(v, s1); n != 0 {
		t.Errorf("%d needs-you alerts for s1 at once, want none before 20 s", n)
	}

	// 5. Once s1 has waited past 20 s, it alerts.
	clock.Advance(19 * time.Second)
	if n := needsYouAlerts(m.snapshot(), s1); n != 0 {
		t.Errorf("%d needs-you alerts after 19 s, want none", n)
	}
	clock.Advance(2 * time.Second)
	if n := needsYouAlerts(m.snapshot(), s1); n != 1 {
		t.Errorf("%d needs-you alerts after 21 s, want 1", n)
	}
}
