package doctor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/clocktest"
	"github.com/dsj7419/loadline/internal/doctor"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/install"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/server"
	"github.com/dsj7419/loadline/internal/tracker"
)

const (
	token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	notFound      = "not found; install Claude Code, or set claudePath in the Loadline config"
	wrapperDetail = "the PowerShell wrapper starts a PowerShell process on every status refresh, which is slower " +
		"than the Git Bash pipe form; installing Git for Windows and running loadline install again avoids it"
)

var t0 = time.Date(2026, time.November, 21, 18, 0, 0, 0, time.UTC)

// fakeState is the live state behind a real server: it shows the sessions a
// test gives it and ignores the events the server applies.
type fakeState struct {
	mu       sync.Mutex
	sessions []tracker.SessionView
}

func (s *fakeState) Apply(domain.Event)          {}
func (s *fakeState) RecordAPI(domain.APIRequest) {}

func (s *fakeState) Snapshot() tracker.View {
	s.mu.Lock()
	defer s.mu.Unlock()
	return tracker.View{GeneratedAt: t0, Sessions: slices.Clone(s.sessions)}
}

func (s *fakeState) Subscribe() (<-chan tracker.View, func()) {
	return make(chan tracker.View), func() {}
}

// setSessions gives the state one session in each of states.
func (s *fakeState) setSessions(states ...domain.State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = nil
	for i, state := range states {
		s.sessions = append(s.sessions, tracker.SessionView{ID: domain.SessionID(fmt.Sprintf("s%d", i)), State: state})
	}
}

// world is an installation for the doctor to check: Loadline's token and
// binary, a Claude Code settings file wired to them, a Loadline server on a
// free loopback port with a clock of its own, and a claude of a supported
// version. The doctor reads the server's clock.
type world struct {
	t         *testing.T
	settings  string
	exe       string
	tokenFile string
	dataDir   string
	port      int
	clock     *clocktest.Clock
	state     *fakeState
	server    *server.Server
	listener  *httptest.Server
	http      *http.Client
	deps      doctor.Deps

	// healthDelay holds back the server's answers to /healthz.
	healthDelay atomic.Int64

	// exposure and exposureErr are what the doctor learns of who else can
	// get into the data directory, and exposed the paths it asked about.
	exposure    string
	exposureErr error
	exposed     []string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	dir := t.TempDir()
	w := &world{
		t:         t,
		settings:  filepath.Join(dir, "claude", "settings.json"),
		exe:       filepath.ToSlash(filepath.Join(dir, "bin", "loadline.exe")),
		tokenFile: filepath.Join(dir, "token"),
		dataDir:   dir,
		clock:     clocktest.New(t0),
		state:     &fakeState{},
		http:      &http.Client{Transport: &http.Transport{Proxy: nil}},
	}
	t.Cleanup(w.http.CloseIdleConnections)
	w.write(w.tokenFile, token)
	w.write(filepath.FromSlash(w.exe), "the installed binary")

	w.listener = httptest.NewUnstartedServer(nil)
	addr, ok := w.listener.Listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %v is not TCP", w.listener.Listener.Addr())
	}
	w.port = addr.Port
	w.server = server.New(server.Deps{State: w.state, Token: []byte(token), Port: w.port, Clock: w.clock.Now})
	handler := w.server.Handler()
	w.listener.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Duration(w.healthDelay.Load())):
			}
		}
		handler.ServeHTTP(rw, r)
	})
	w.listener.Start()
	t.Cleanup(w.listener.Close)

	w.install(w.settings, `{}`, install.ShellBash)
	w.deps = doctor.Deps{
		SettingsFiles: []string{w.settings},
		Exe:           w.exe,
		Port:          w.port,
		Shell:         func([]byte) install.Shell { return install.ShellBash },
		Client:        forward.Client{BaseURL: forward.LoopbackURL(w.port), HTTP: w.http},
		ClaudeVersion: func(context.Context) (string, error) { return "2.1.330 (Claude Code)\n", nil },
		TokenFile:     w.tokenFile,
		DataDir:       w.dataDir,
		Exposure: func(path string) (string, error) {
			w.exposed = append(w.exposed, path)
			return w.exposure, w.exposureErr
		},
		Now: w.clock.Now,
	}
	return w
}

func (w *world) write(path, contents string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) read(path string) []byte {
	w.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		w.t.Fatal(err)
	}
	return data
}

// install writes settings to path as loadline install leaves them for shell.
func (w *world) install(path, settings string, shell install.Shell) {
	w.t.Helper()
	w.installWith(path, settings, install.Options{Exe: w.exe, Port: w.port, Token: token, Shell: shell})
}

func (w *world) installWith(path, settings string, o install.Options) {
	w.t.Helper()
	out, _, _, err := install.Plan([]byte(settings), o)
	if err != nil {
		w.t.Fatalf("Plan: %v", err)
	}
	w.write(path, string(out))
}

func (w *world) run() []doctor.Check {
	w.t.Helper()
	return doctor.Run(w.t.Context(), w.deps)
}

// The hear methods deliver one event of a feed to the server, which notes
// it at its clock's time.

func (w *world) hearHook() {
	w.t.Helper()
	w.send(forward.HookPath, protocol.KindHook, `{"session_id":"s0","hook_event_name":"Stop"}`)
}

func (w *world) hearStatus() {
	w.t.Helper()
	w.send(forward.StatusPath, protocol.KindStatus, `{"session_id":"s0"}`)
}

func (w *world) hearOTLP() {
	w.t.Helper()
	req, err := http.NewRequestWithContext(w.t.Context(), http.MethodPost,
		forward.LoopbackURL(w.port)+"/v1/logs", strings.NewReader(`{}`))
	if err != nil {
		w.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Loadline-Token", token)
	resp, err := w.http.Do(req)
	if err != nil {
		w.t.Fatalf("post logs: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		w.t.Fatalf("post logs: %s", resp.Status)
	}
}

func (w *world) hearPoll() { w.server.NotePoll(w.clock.Now()) }

func (w *world) hearAll() {
	w.t.Helper()
	w.hearHook()
	w.hearStatus()
	w.hearOTLP()
	w.hearPoll()
}

func (w *world) send(path string, kind protocol.Kind, payload string) {
	w.t.Helper()
	id, err := protocol.NewEventID()
	if err != nil {
		w.t.Fatal(err)
	}
	env := protocol.Envelope{
		V: protocol.SchemaVersion, ID: id, Kind: kind, At: w.clock.Now(),
		ConfigDir: filepath.Dir(w.settings), Payload: json.RawMessage(payload),
	}
	client := forward.Client{BaseURL: forward.LoopbackURL(w.port), Token: []byte(token), HTTP: w.http}
	if err := client.Send(w.t.Context(), path, env); err != nil {
		w.t.Fatalf("send %s: %v", path, err)
	}
}

func assertChecks(t *testing.T, got, want []doctor.Check) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("checks:\n%swant:\n%s", list(got), list(want))
	}
}

func list(checks []doctor.Check) string {
	var b strings.Builder
	for _, c := range checks {
		fmt.Fprintf(&b, "  %v %s: %s\n", c.Status, c.Name, c.Detail)
	}
	return b.String()
}

// named returns the checks with one of names, in order.
func named(checks []doctor.Check, names ...string) []doctor.Check {
	return slices.DeleteFunc(slices.Clone(checks), func(c doctor.Check) bool { return !slices.Contains(names, c.Name) })
}

// fixed is a detail that does not depend on the world.
func fixed(detail string) func(*world) string { return func(*world) string { return detail } }

func feedChecks(checks []doctor.Check) []doctor.Check {
	return named(checks, "hook feed", "status line feed", "OpenTelemetry feed", "poll feed", "feeds")
}

// settingsChecks returns the checks of the settings files, which come
// between the token's and the binary's.
func settingsChecks(t *testing.T, checks []doctor.Check) []doctor.Check {
	t.Helper()
	token := slices.IndexFunc(checks, func(c doctor.Check) bool { return c.Name == "token" })
	binary := slices.IndexFunc(checks, func(c doctor.Check) bool { return c.Name == "binary" })
	if token < 0 || binary < token {
		t.Fatalf("checks:\n%swant the token's, then the settings', then the binary's", list(checks))
	}
	return checks[token+1 : binary]
}

func TestDoctor_AllGreen(t *testing.T) {
	w := newWorld(t)
	w.state.setSessions(domain.StateWorking, domain.StateEnded)
	w.hearAll()
	w.clock.Advance(time.Minute)

	got := w.run()

	assertChecks(t, got, []doctor.Check{
		{Name: "claude", Status: doctor.OK, Detail: "2.1.330"},
		{Name: "data directory", Status: doctor.OK, Detail: w.dataDir},
		{Name: "token", Status: doctor.OK, Detail: w.tokenFile},
		{Name: "settings " + w.settings, Status: doctor.OK, Detail: "wired"},
		{Name: "binary", Status: doctor.OK, Detail: filepath.FromSlash(w.exe)},
		{Name: "server", Status: doctor.OK, Detail: "running at " + forward.LoopbackURL(w.port)},
		{Name: "hook feed", Status: doctor.OK, Detail: "last seen 1m0s ago"},
		{Name: "status line feed", Status: doctor.OK, Detail: "last seen 1m0s ago"},
		{Name: "OpenTelemetry feed", Status: doctor.OK, Detail: "last seen 1m0s ago"},
		{Name: "poll feed", Status: doctor.OK, Detail: "last seen 1m0s ago"},
	})
}

func TestDoctor_NotWired(t *testing.T) {
	const oldExe = "D:/old/loadline.exe"
	tests := []struct {
		name    string
		prepare func(w *world, path string)
		detail  func(w *world) string
	}{
		{
			name:    "no settings file",
			prepare: func(*world, string) {},
			detail:  fixed("not wired: the file does not exist; run loadline install"),
		},
		{
			name:    "never installed",
			prepare: func(w *world, path string) { w.write(path, `{"model": "opus"}`) },
			detail: fixed("not wired: install would change hooks.SessionStart, hooks.SessionEnd, hooks.UserPromptSubmit " +
				"and 18 more; run loadline install"),
		},
		{
			name:    "never installed, next to the user's own telemetry",
			prepare: func(w *world, path string) { w.write(path, `{"env": {"OTEL_LOGS_EXPORTER": "console"}}`) },
			detail: fixed("not wired: install would change hooks.SessionStart, hooks.SessionEnd, hooks.UserPromptSubmit " +
				"and 11 more; run loadline install; also, env already sets OTEL_LOGS_EXPORTER, so Loadline left " +
				"OpenTelemetry alone and the per-model view will be unavailable"),
		},
		{
			name: "installed for another port",
			prepare: func(w *world, path string) {
				w.installWith(path, `{}`, install.Options{Exe: w.exe, Port: w.port - 1, Token: token, Shell: install.ShellBash})
			},
			detail: fixed("not wired: install would change env.OTEL_EXPORTER_OTLP_LOGS_ENDPOINT; run loadline install"),
		},
		{
			name: "installed for another binary",
			prepare: func(w *world, path string) {
				w.installWith(path, `{}`, install.Options{Exe: oldExe, Port: w.port, Token: token, Shell: install.ShellBash})
			},
			detail: func(w *world) string {
				return "not wired: its hooks run " + oldExe + " instead of " + w.exe + "; install would change " +
					"hooks.SessionStart, hooks.SessionEnd, hooks.UserPromptSubmit and 11 more; run loadline install"
			},
		},
		{
			name:    "not JSON",
			prepare: func(w *world, path string) { w.write(path, `{"hooks": `) },
			detail:  fixed("unusable settings file: it is not valid JSON"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			other := filepath.Join(t.TempDir(), "settings.json")
			tt.prepare(w, other)
			w.deps.SettingsFiles = []string{w.settings, other}

			got := w.run()

			assertChecks(t, settingsChecks(t, got), []doctor.Check{
				{Name: "settings " + w.settings, Status: doctor.OK, Detail: "wired"},
				{Name: "settings " + other, Status: doctor.Fail, Detail: tt.detail(w)},
			})
		})
	}
}

func TestDoctor_SkippedTelemetryWarns(t *testing.T) {
	w := newWorld(t)
	w.install(w.settings, `{"env": {"OTEL_LOGS_EXPORTER": "console"}}`, install.ShellBash)

	got := w.run()

	assertChecks(t, settingsChecks(t, got), []doctor.Check{{
		Name:   "settings " + w.settings,
		Status: doctor.Warn,
		Detail: "wired, but env already sets OTEL_LOGS_EXPORTER, so Loadline left OpenTelemetry alone " +
			"and the per-model view will be unavailable",
	}})
}

func TestDoctor_PowerShellWrapperWarns(t *testing.T) {
	tests := []struct {
		name     string
		settings string
		want     []doctor.Check
	}{
		{
			name:     "around the user's own status line",
			settings: `{"statusLine": {"type": "command", "command": "my-status"}}`,
			want:     []doctor.Check{{Status: doctor.Warn, Detail: wrapperDetail}},
		},
		{
			name:     "printing its own status line",
			settings: `{}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			w.install(w.settings, tt.settings, install.ShellPowerShell)
			w.deps.Shell = func([]byte) install.Shell { return install.ShellPowerShell }

			got := w.run()

			want := []doctor.Check{{Name: "settings " + w.settings, Status: doctor.OK, Detail: "wired"}}
			for _, c := range tt.want {
				c.Name = "status line " + w.settings
				want = append(want, c)
			}
			assertChecks(t, settingsChecks(t, got), want)
		})
	}
}

// Each file's own env can choose the shell, so each file must be judged by
// the shell its own bytes give.
func TestDoctor_DetectsTheShellOfEachFile(t *testing.T) {
	w := newWorld(t)
	own := `"statusLine": {"type": "command", "command": "my-status"}`
	powerShell := filepath.Join(t.TempDir(), "settings.json")
	w.install(powerShell, `{"model": "on-powershell", `+own+`}`, install.ShellPowerShell)
	w.install(w.settings, `{`+own+`}`, install.ShellBash)
	w.deps.SettingsFiles = []string{powerShell, w.settings}
	var mu sync.Mutex
	var seen [][]byte
	w.deps.Shell = func(settings []byte) install.Shell {
		mu.Lock()
		seen = append(seen, slices.Clone(settings))
		mu.Unlock()
		if strings.Contains(string(settings), `"on-powershell"`) {
			return install.ShellPowerShell
		}
		return install.ShellBash
	}

	got := w.run()

	assertChecks(t, settingsChecks(t, got), []doctor.Check{
		{Name: "settings " + powerShell, Status: doctor.OK, Detail: "wired"},
		{Name: "status line " + powerShell, Status: doctor.Warn, Detail: wrapperDetail},
		{Name: "settings " + w.settings, Status: doctor.OK, Detail: "wired"},
	})
	mu.Lock()
	defer mu.Unlock()
	if want := [][]byte{w.read(powerShell), w.read(w.settings)}; !slices.EqualFunc(seen, want, bytes.Equal) {
		t.Errorf("Shell saw %q, want each file's contents once, in order: %q", seen, want)
	}
}

func TestDoctor_OldClaudeFails(t *testing.T) {
	tests := []struct {
		name   string
		output string
		err    error
		want   string
	}{
		{name: "an older patch", output: "2.1.299 (Claude Code)\n", want: "2.1.299 is older than 2.1.300; update Claude Code"},
		{name: "an older minor", output: "2.0.999 (Claude Code)\n", want: "2.0.999 is older than 2.1.300; update Claude Code"},
		{name: "an older major", output: "1.99.999\n", want: "1.99.999 is older than 2.1.300; update Claude Code"},
		{
			name:   "a pre-release of the oldest supported",
			output: "2.1.300-rc.1 (Claude Code)\n",
			want:   "2.1.300-rc.1 is older than 2.1.300; update Claude Code",
		},
		{
			name: "not on PATH",
			err:  fmt.Errorf("locate claude: %w", &exec.Error{Name: "claude", Err: exec.ErrNotFound}),
			want: notFound,
		},
		{
			name: "missing at its configured path",
			err:  fmt.Errorf("claude --version: %w", &fs.PathError{Op: "fork/exec", Path: "/opt/claude", Err: fs.ErrNotExist}),
			want: notFound,
		},
		{name: "fails to run", err: errors.New("claude --version: exit status 3"), want: "failed: claude --version: exit status 3"},
		{name: "prints no version", output: "Claude Code\r\n", want: `claude --version printed "Claude Code", which holds no version`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			w.deps.ClaudeVersion = func(context.Context) (string, error) { return tt.output, tt.err }

			got := w.run()

			assertChecks(t, named(got, "claude"), []doctor.Check{{Name: "claude", Status: doctor.Fail, Detail: tt.want}})
		})
	}
}

func TestDoctor_SupportedClaudePasses(t *testing.T) {
	tests := []struct{ output, want string }{
		{output: "2.1.300 (Claude Code)\n", want: "2.1.300"},
		{output: "2.1.1000 (Claude Code)\n", want: "2.1.1000"},
		{output: "2.2.0\n", want: "2.2.0"},
		{output: "10.0.0\n", want: "10.0.0"},
		{output: "  2.1.330-beta.1 (Claude Code)\r\n", want: "2.1.330-beta.1"},
		{output: "2.1.301-rc.1\n", want: "2.1.301-rc.1"},
	}
	for _, tt := range tests {
		t.Run(tt.output, func(t *testing.T) {
			w := newWorld(t)
			w.deps.ClaudeVersion = func(context.Context) (string, error) { return tt.output, nil }

			got := w.run()

			assertChecks(t, named(got, "claude"), []doctor.Check{{Name: "claude", Status: doctor.OK, Detail: tt.want}})
		})
	}
}

func TestDoctor_TokenFileMustHoldAToken(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(w *world)
		want    func(w *world) string
	}{
		{
			name:    "missing",
			prepare: func(w *world) { w.deps.TokenFile = filepath.Join(filepath.Dir(w.tokenFile), "no-token") },
			want:    func(w *world) string { return w.deps.TokenFile + " does not exist; run loadline install" },
		},
		{
			name:    "malformed",
			prepare: func(w *world) { w.write(w.tokenFile, "not a token") },
			want: func(w *world) string {
				return "token file " + w.tokenFile + " does not hold 64 hex characters; delete it to generate a new token"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			tt.prepare(w)

			got := w.run()

			// Without the token, the settings cannot be shown to hold it and
			// the server will not answer the feed checks.
			assertChecks(t, named(got, "token", "settings "+w.settings, "feeds"), []doctor.Check{
				{Name: "token", Status: doctor.Fail, Detail: tt.want(w)},
				{
					Name:   "settings " + w.settings,
					Status: doctor.Fail,
					Detail: "not wired: install would change env.OTEL_EXPORTER_OTLP_LOGS_HEADERS; run loadline install",
				},
				{Name: "feeds", Status: doctor.Fail, Detail: "cannot be checked without the token file"},
			})
		})
	}
}

// A server started before the token file was replaced holds the old token,
// and only a restart makes it read the new one.
func TestDoctor_ServerWithAnotherTokenFails(t *testing.T) {
	w := newWorld(t)
	w.write(w.tokenFile, strings.Repeat("ab", 32))

	got := w.run()

	assertChecks(t, feedChecks(got), []doctor.Check{{
		Name:   "feeds",
		Status: doctor.Fail,
		Detail: "cannot be checked: the server holds a different token than " + w.tokenFile + "; restart it",
	}})
}

func TestDoctor_MissingBinaryFails(t *testing.T) {
	w := newWorld(t)
	if err := os.Remove(filepath.FromSlash(w.exe)); err != nil {
		t.Fatal(err)
	}

	got := w.run()

	assertChecks(t, named(got, "binary"), []doctor.Check{{
		Name:   "binary",
		Status: doctor.Fail,
		Detail: filepath.FromSlash(w.exe) + " does not exist; run loadline install",
	}})
}

func TestDoctor_FeedStaleWarns(t *testing.T) {
	w := newWorld(t)
	w.state.setSessions(domain.StateIdle, domain.StateWorking)
	w.hearHook()
	w.clock.Advance(time.Minute)
	w.hearPoll()
	w.clock.Advance(9 * time.Minute)
	w.hearStatus()
	w.clock.Advance(time.Minute)

	got := w.run()

	assertChecks(t, feedChecks(got), []doctor.Check{
		{Name: "hook feed", Status: doctor.Warn, Detail: "last seen 11m0s ago, while sessions are active"},
		{Name: "status line feed", Status: doctor.OK, Detail: "last seen 1m0s ago"},
		{Name: "OpenTelemetry feed", Status: doctor.Warn, Detail: "not seen since the server started, while sessions are active"},
		{Name: "poll feed", Status: doctor.OK, Detail: "last seen 10m0s ago"},
	})
}

func TestDoctor_StaleFeedsAreFineWithoutActiveSessions(t *testing.T) {
	w := newWorld(t)
	w.state.setSessions(domain.StateIdle, domain.StateNeedsYou, domain.StateError, domain.StateParked, domain.StateEnded)
	w.hearHook()
	w.clock.Advance(11 * time.Minute)
	w.hearPoll()
	w.clock.Advance(time.Minute)

	got := w.run()

	assertChecks(t, feedChecks(got), []doctor.Check{
		{Name: "hook feed", Status: doctor.OK, Detail: "no active sessions"},
		{Name: "status line feed", Status: doctor.OK, Detail: "no active sessions"},
		{Name: "OpenTelemetry feed", Status: doctor.OK, Detail: "no active sessions"},
		{Name: "poll feed", Status: doctor.OK, Detail: "last seen 1m0s ago"},
	})
}

// Only a session that is starting, working or compacting sends events, so
// only such a session makes a quiet feed a warning. One that waits for the
// user may stay quiet for as long as the user is away.
func TestDoctor_OnlyActiveSessionsMakeAQuietFeedStale(t *testing.T) {
	stale := doctor.Check{Name: "hook feed", Status: doctor.Warn, Detail: "last seen 11m0s ago, while sessions are active"}
	quiet := doctor.Check{Name: "hook feed", Status: doctor.OK, Detail: "no active sessions"}
	for state, want := range map[domain.State]doctor.Check{
		domain.StateStarting:   stale,
		domain.StateWorking:    stale,
		domain.StateCompacting: stale,
		domain.StateIdle:       quiet,
		domain.StateNeedsYou:   quiet,
		domain.StateError:      quiet,
		domain.StateParked:     quiet,
		domain.StateEnded:      quiet,
	} {
		t.Run(state.String(), func(t *testing.T) {
			w := newWorld(t)
			w.state.setSessions(state)
			w.hearHook()
			w.clock.Advance(11 * time.Minute)

			got := w.run()

			assertChecks(t, named(got, "hook feed"), []doctor.Check{want})
		})
	}
}

// The doctor is off the hot path, so it gives a busy server longer to answer
// than a hook would.
func TestDoctor_WaitsForASlowServer(t *testing.T) {
	w := newWorld(t)
	w.healthDelay.Store(int64(500 * time.Millisecond))

	got := w.run()

	assertChecks(t, named(got, "server"), []doctor.Check{
		{Name: "server", Status: doctor.OK, Detail: "running at " + forward.LoopbackURL(w.port)},
	})
}

func TestDoctor_ServerDownFailsButOtherChecksRun(t *testing.T) {
	w := newWorld(t)
	w.listener.Close()

	got := w.run()

	url := forward.LoopbackURL(w.port)
	assertChecks(t, got, []doctor.Check{
		{Name: "claude", Status: doctor.OK, Detail: "2.1.330"},
		{Name: "data directory", Status: doctor.OK, Detail: w.dataDir},
		{Name: "token", Status: doctor.OK, Detail: w.tokenFile},
		{Name: "settings " + w.settings, Status: doctor.OK, Detail: "wired"},
		{Name: "binary", Status: doctor.OK, Detail: filepath.FromSlash(w.exe)},
		{
			Name:   "server",
			Status: doctor.Fail,
			Detail: "no Loadline server answers at " + url + "; the next Claude Code session starts it, or run loadline serve",
		},
		{Name: "feeds", Status: doctor.Fail, Detail: "cannot be checked while the server is down"},
	})
}

// Another program on the port is no Loadline server, so it must never see the
// token.
func TestDoctor_NeverSendsTheTokenToAnotherProgram(t *testing.T) {
	w := newWorld(t)
	var mu sync.Mutex
	var sawToken bool
	other := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sawToken = sawToken || r.Header.Get("X-Loadline-Token") != ""
		mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"app":"something-else","feeds":{},"sessions":[]}`))
	}))
	t.Cleanup(other.Close)
	w.deps.Client.BaseURL = other.URL

	got := w.run()

	mu.Lock()
	defer mu.Unlock()
	if sawToken {
		t.Error("the doctor sent the token to a server that is not Loadline")
	}
	if feeds := feedChecks(got); len(feeds) != 1 || feeds[0].Status != doctor.Fail {
		t.Errorf("feed checks:\n%swant one failure", list(feeds))
	}
}

func TestLevel_String(t *testing.T) {
	for level, want := range map[doctor.Level]string{doctor.OK: "ok", doctor.Warn: "warn", doctor.Fail: "fail", 7: "Level(7)"} {
		if got := level.String(); got != want {
			t.Errorf("Level(%d).String() = %q, want %q", uint8(level), got, want)
		}
	}
}

// The data directory holds the token and the binary every hook runs, so
// anyone else it admits can read the one and replace the other.
func TestDoctor_DataDirThatAdmitsOthersFails(t *testing.T) {
	w := newWorld(t)
	w.exposure = `its access list also admits BUILTIN\Users`

	got := w.run()

	assertChecks(t, named(got, "data directory"), []doctor.Check{{
		Name:   "data directory",
		Status: doctor.Fail,
		Detail: w.dataDir + ` is not private: its access list also admits BUILTIN\Users; run loadline install`,
	}})
	if want := []string{w.dataDir}; !slices.Equal(w.exposed, want) {
		t.Errorf("asked who else can get into %q, want %q", w.exposed, want)
	}
}

func TestDoctor_DataDirThatCannotBeReadFails(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want func(w *world) string
	}{
		{
			name: "missing",
			err:  fmt.Errorf("read the access list: %w", fs.ErrNotExist),
			want: func(w *world) string { return w.dataDir + " does not exist; run loadline install" },
		},
		{
			name: "unreadable",
			err:  errors.New("read the access list: access is denied"),
			want: fixed("read the access list: access is denied"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			w.exposureErr = tt.err

			got := w.run()

			assertChecks(t, named(got, "data directory"), []doctor.Check{
				{Name: "data directory", Status: doctor.Fail, Detail: tt.want(w)},
			})
		})
	}
}

// install writes the token into the file a symlinked settings.json points
// to, which may sit in a dotfiles repository that its owner publishes.
func TestDoctor_SymlinkedSettingsWarn(t *testing.T) {
	w := newWorld(t)
	target := filepath.Join(t.TempDir(), "dotfiles", "settings.json")
	w.install(target, `{}`, install.ShellBash)
	link := filepath.Join(t.TempDir(), "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	w.deps.SettingsFiles = []string{link}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}

	got := w.run()

	assertChecks(t, settingsChecks(t, got), []doctor.Check{
		{Name: "settings " + link, Status: doctor.OK, Detail: "wired"},
		{
			Name:   "token in " + link,
			Status: doctor.Warn,
			Detail: "the file is a symlink to " + resolved + ", so install writes the ingest token there; " +
				"keep that file out of any repository you share",
		},
	})
}
