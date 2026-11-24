package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/sessionenv"
	"github.com/dsj7419/loadline/internal/tracker"
)

const (
	// upgradeSession is the session the stand-in claude lists as live.
	upgradeSession = "e2e00000-0000-4000-8000-000000000001"
	oldVersion     = "0.1.1-e2e-old"
	newVersion     = "0.1.1-e2e-new"

	// hooksBefore subagents start before the new install does, hooksDuring
	// more from the moment no server answers, once install has asked the old
	// one to stop, and hooksAfter once install has returned. All of them
	// stay below the 32 running subagents a session counts.
	hooksBefore = 6
	hooksDuring = 18
	hooksAfter  = 6

	// buildBound bounds each build, which compiles everything the first
	// time on a machine. commandBound bounds every other command: an install
	// that hands a server over waits for the old one to stop and the new one
	// to answer.
	buildBound   = 10 * time.Minute
	commandBound = 2 * time.Minute

	modulePath = "github.com/dsj7419/loadline"
)

// oneShot asks the servers in the sandbox, keeping no connection open after
// an answer: the servers come and go, and a connection kept would outlive
// the test.
var oneShot = &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: hangBound}

// An upgrade while a server runs and sessions keep sending hooks: the new
// install stops the old server, which writes all it applied before it
// lets go of the history, starts the new binary's server, which takes the
// history up from there and drains what the hooks spooled in between, and
// loses no hook. Real binaries, built for the test, run in a sandbox that
// keeps them from the machine's own Loadline and Claude Code.
func TestUpgrade_InstallHandsARunningServerOverToTheNewBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds three binaries and runs them")
	}
	s := newSandbox(t)
	oldExe := filepath.Join(s.tree, "build", "old", "loadline"+exeSuffix())
	newExe := filepath.Join(s.tree, "build", "new", "loadline"+exeSuffix())

	// 1. The stand-in claude, and the binary twice, each trusting the tree
	// as a folder nobody else can move.
	s.check(t, s.claude, oldExe, newExe)
	build(t, s.claude, "./testdata/fakeclaude", "")
	for exe, version := range map[string]string{oldExe: oldVersion, newExe: newVersion} {
		build(t, exe, modulePath+"/cmd/loadline",
			fmt.Sprintf("-X %s/internal/cli.Version=%s -X '%s/internal/cli.trustFrom=%s'", modulePath, version, modulePath, s.tree))
	}

	// 2. The tree: the config, and the session the stand-in lists as live.
	s.writeConfig(t)
	writeAgents(t, s)
	s.check(t)

	// 3. The old binary installs itself, and its server runs from bin, as a
	// hook would start it.
	s.check(t, oldExe, s.configDir)
	s.run(t, oldExe, "install", "--yes", "--config-dir", s.configDir)
	settingsFile := filepath.Join(s.configDir, "settings.json")
	settings := readBytes(t, settingsFile)
	s.check(t, s.installed())
	old := s.start(t, s.installed(), "serve", "--no-open")
	t.Cleanup(func() { s.stopServer(t) })
	eventually(t, "the old server to answer", func() bool { return s.version() == oldVersion })

	// 4. Subagents start, each one's hook run from bin as Claude Code runs
	// it: before the install, while it runs and once it has.
	s.check(t, s.installed())
	hooks := s.sendHooks(t)
	hooks.await(t, hooksBefore)

	// 5. The new binary installs itself.
	s.check(t, newExe, s.configDir)
	installed := s.run(t, newExe, "install", "--yes", "--config-dir", s.configDir)
	hooks.installDone()
	if strings.Contains(installed, "Stopped the old server; run loadline open from a normal shell to start the new one.") {
		// Run elevated, as on some CI machines, install starts no server;
		// the test then does as the user would from a normal shell.
		t.Log("install ran elevated: it stopped the old server, and the test starts the new one")
		s.check(t, s.installed())
		s.start(t, s.installed(), "serve", "--no-open")
	} else {
		t.Log("install ran from a normal shell: it stopped the old server and started the new one")
		if want := "Restarted the Loadline server on " + newVersion + "; reload an open dashboard."; !strings.Contains(installed, want) {
			t.Errorf("the new install says:\n%s\nwant %q", installed, want)
		}
	}
	sent := hooks.wait(t)

	// 6. Each hook counted once, in a server of the new version.
	s.check(t)
	if code := old.exitCode(t); code != 0 {
		t.Errorf("the old server exited %d, want 0", code)
	}
	eventually(t, "the new server to answer", func() bool { return s.version() == newVersion })
	if runtime.GOOS == "windows" {
		if aside, _ := filepath.Glob(s.installed() + ".old*"); len(aside) != 0 {
			t.Errorf("the binaries the upgrade moved aside are still there: %q", aside)
		}
	}
	if after := readBytes(t, settingsFile); !bytes.Equal(after, settings) {
		t.Errorf("the second install changed the settings:\n%s\nwas:\n%s", after, settings)
	}
	var counted int
	eventually(t, fmt.Sprintf("the session to count the %d subagents that started", sent), func() bool {
		counted = s.subagents(t)
		return counted == sent
	})
	if counted != hooksBefore+hooksDuring+hooksAfter {
		t.Errorf("%d subagents counted, want all %d sent", counted, hooksBefore+hooksDuring+hooksAfter)
	}
	// The hooks held until no server answered were spooled, and drained.
	if drained := s.drained(t); drained < 1 {
		t.Errorf("the servers drained %d hook events spooled while none answered, want at least one", drained)
	} else {
		t.Logf("the servers drained %d hook events spooled while none answered", drained)
	}
}

// drained is how many spooled events the servers' log says they drained.
func (s *sandbox) drained(t *testing.T) int {
	t.Helper()
	total := 0
	for line := range strings.Lines(string(readBytes(t, filepath.Join(s.loadlineHome, "loadline.log")))) {
		if !strings.Contains(line, `msg="spool drained"`) {
			continue
		}
		var events int
		if _, after, ok := strings.Cut(line, " events="); ok {
			if _, err := fmt.Sscanf(after, "%d", &events); err == nil {
				total += events
			}
		}
	}
	return total
}

// build builds the package pkg into out, with ldflags, for this system and
// without cgo, as a release build is. The build tool runs with the test's
// own environment, less what would tie it to a session: it reads nothing of
// Loadline's or Claude Code's, and writes only out.
func build(t *testing.T, out, pkg, ldflags string) {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("find the go tool: %v", err)
	}
	args := []string{"build", "-o", out}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	ctx, cancel := context.WithTimeout(t.Context(), buildBound)
	defer cancel()
	cmd := exec.CommandContext(ctx, goTool, append(args, pkg)...) //nolint:gosec // G204: the go tool, building this module
	cmd.Env = append(sessionenv.Strip(os.Environ()), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, output)
	}
}

// writeAgents has the stand-in claude list the session as live.
func writeAgents(t *testing.T, s *sandbox) {
	t.Helper()
	agents, err := json.Marshal([]map[string]any{{
		"sessionId": upgradeSession, "kind": "interactive", "pid": 4242, "status": "busy",
		"cwd": filepath.Join(s.tree, "work"), "startedAt": time.Now().UnixMilli(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(s.claude), "agents.json"), agents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// run runs exe with args in the sandbox and returns what it printed. It
// fails the test unless exe exits 0.
func (s *sandbox) run(t *testing.T, exe string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), commandBound)
	defer cancel()
	cmd, err := s.command(ctx, exe, args...)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %q: %v\nstdout:\n%s\nstderr:\n%s", filepath.Base(exe), args, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// process is a server the test started, which it ends itself, by its own
// handle, should the test end with it still running.
type process struct {
	cmd    *exec.Cmd
	exited chan struct{}
	err    error
}

// start starts exe with args in the sandbox and leaves it running.
func (s *sandbox) start(t *testing.T, exe string, args ...string) *process {
	t.Helper()
	cmd, err := s.command(context.Background(), exe, args...)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s %q: %v", filepath.Base(exe), args, err)
	}
	p := &process{cmd: cmd, exited: make(chan struct{})}
	// Waited for at once, so that on Unix the process is reaped as it
	// exits, and its ID no longer answers signal 0.
	go func() {
		p.err = cmd.Wait()
		close(p.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-p.exited:
		default:
			_ = cmd.Process.Kill()
			<-p.exited
		}
	})
	return p
}

// exitCode waits for the process to exit, and returns its exit code.
func (p *process) exitCode(t *testing.T) int {
	t.Helper()
	select {
	case <-p.exited:
	case <-time.After(hangBound):
		t.Fatalf("the process did not exit within %v", hangBound)
	}
	var exit *exec.ExitError
	switch {
	case p.err == nil:
		return 0
	case errors.As(p.err, &exit):
		return exit.ExitCode()
	default:
		t.Fatalf("wait for the process: %v", p.err)
		return -1
	}
}

// stopServer asks whatever Loadline server runs in the sandbox to stop,
// through the installed binary, as the user would.
func (s *sandbox) stopServer(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandBound)
	defer cancel()
	cmd, err := s.command(ctx, s.installed(), "stop")
	if err != nil {
		t.Error(err)
		return
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("loadline stop: %v\n%s", err, output)
	}
}

// version is the version the server in the sandbox answers with, or "" when
// none answers.
func (s *sandbox) version() string {
	ctx, cancel := context.WithTimeout(context.Background(), hangBound)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, forward.LoopbackURL(s.port)+"/healthz", http.NoBody)
	if err != nil {
		return ""
	}
	resp, err := oneShot.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	var health struct {
		App     string `json:"app"`
		Version string `json:"version"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil || resp.StatusCode != http.StatusOK || json.Unmarshal(body, &health) != nil || health.App != "loadline" {
		return ""
	}
	return health.Version
}

// subagents is how many subagents the server counts in the session.
func (s *sandbox) subagents(t *testing.T) int {
	t.Helper()
	token := bytes.TrimSpace(readBytes(t, filepath.Join(s.loadlineHome, "token")))
	client := forward.Client{BaseURL: forward.LoopbackURL(s.port), Token: token, HTTP: oneShot}
	body, err := client.Get(t.Context(), "/api/snapshot", 32<<20)
	if err != nil {
		return -1
	}
	var view tracker.View
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode the snapshot: %v", err)
	}
	for _, session := range view.Sessions {
		if session.ID == upgradeSession {
			return session.Subagents
		}
	}
	return 0
}

// hookSender runs the hooks of subagents starting in the session, one after
// another, from the installed binary.
type hookSender struct {
	s *sandbox
	// sent counts the hooks that ran.
	sent atomic.Int32
	// installed is closed once the new install has returned, which the last
	// hooksAfter hooks wait for.
	installed     chan struct{}
	installedOnce sync.Once
	done          chan struct{}
	err           error
}

func (s *sandbox) sendHooks(t *testing.T) *hookSender {
	t.Helper()
	h := &hookSender{s: s, installed: make(chan struct{}), done: make(chan struct{})}
	go h.run()
	t.Cleanup(func() {
		h.installDone()
		<-h.done
	})
	return h
}

func (h *hookSender) run() {
	defer close(h.done)
	for i := range hooksBefore + hooksDuring + hooksAfter {
		switch i {
		case hooksBefore:
			// Held until no server answers, so that the first lands while
			// none does.
			if err := h.awaitSilence(); err != nil {
				h.err = err
				return
			}
		case hooksBefore + hooksDuring:
			<-h.installed
		}
		if err := h.send(fmt.Sprintf("agent-%03d", i)); err != nil {
			h.err = err
			return
		}
		h.sent.Add(1)
	}
}

// awaitSilence waits until no server in the sandbox answers, as from the
// moment the old server stops listening until the new one serves.
func (h *hookSender) awaitSilence() error {
	for deadline := time.Now().Add(commandBound); h.s.version() != ""; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			return fmt.Errorf("a server still answered %v after the hooks before the install", commandBound)
		}
	}
	return nil
}

// send runs the hook of the subagent agent starting. While install swaps
// the binary, Windows may find nothing at its path for a moment, so a hook
// that cannot start is started again, as Claude Code's next one would be.
func (h *hookSender) send(agent string) error {
	payload := fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SubagentStart","agent_id":%q,"agent_type":"general-purpose","cwd":%q}`,
		upgradeSession, agent, filepath.Join(h.s.tree, "work"))
	deadline := time.Now().Add(hangBound)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), commandBound)
		cmd, err := h.s.command(ctx, h.s.installed(), "hook")
		if err != nil {
			cancel()
			return err
		}
		cmd.Stdin = strings.NewReader(payload)
		output, err := cmd.CombinedOutput()
		cancel()
		var exit *exec.ExitError
		switch {
		case err == nil && len(output) == 0:
			return nil
		case err == nil:
			return fmt.Errorf("the hook for %s printed %q, want nothing", agent, output)
		case errors.As(err, &exit):
			return fmt.Errorf("the hook for %s exited %d, want 0: %s", agent, exit.ExitCode(), output)
		case time.Now().After(deadline):
			return fmt.Errorf("the hook for %s did not start within %v: %w", agent, hangBound, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// await waits until n hooks have run.
func (h *hookSender) await(t *testing.T, n int) {
	t.Helper()
	eventually(t, fmt.Sprintf("%d hooks to run", n), func() bool {
		select {
		case <-h.done:
			if h.err != nil {
				t.Fatalf("hooks: %v", h.err)
			}
		default:
		}
		return int(h.sent.Load()) >= n
	})
}

// installDone lets the hooks that wait for the new install run.
func (h *hookSender) installDone() { h.installedOnce.Do(func() { close(h.installed) }) }

// wait waits until every hook has run, and returns how many did.
func (h *hookSender) wait(t *testing.T) int {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(commandBound):
		t.Fatalf("the hooks did not finish within %v", commandBound)
	}
	if h.err != nil {
		t.Fatalf("hooks: %v", h.err)
	}
	return int(h.sent.Load())
}
