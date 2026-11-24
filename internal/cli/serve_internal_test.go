package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/clocktest"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/protocol"
)

var idleT0 = time.Date(2026, time.November, 21, 18, 0, 0, 0, time.UTC)

// idleWatch runs waitIdle on a manual clock.
type idleWatch struct {
	clock  *clocktest.Clock
	live   atomic.Bool
	cancel context.CancelFunc
	done   chan bool
}

// startIdleWatch runs waitIdle until it returns or the test ends.
func startIdleWatch(t *testing.T, idleAfter time.Duration, live bool) *idleWatch {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	w := &idleWatch{clock: clocktest.New(idleT0), cancel: cancel, done: make(chan bool, 1)}
	w.live.Store(live)
	go func() { w.done <- waitIdle(ctx, w.live.Load, idleAfter, w.clock.Now, w.clock.AfterFunc) }()
	t.Cleanup(cancel)
	return w
}

// check moves the clock to the next check once the watch has armed it.
func (w *idleWatch) check(t *testing.T) {
	t.Helper()
	w.clock.WaitForTimers(t, 1)
	w.clock.Advance(idleCheckEvery)
}

func (w *idleWatch) assertWaiting(t *testing.T) {
	t.Helper()
	w.clock.WaitForTimers(t, 1)
	select {
	case idle := <-w.done:
		t.Fatalf("waitIdle returned %v after %v", idle, w.clock.Now().Sub(idleT0))
	default:
	}
}

func (w *idleWatch) assertIdle(t *testing.T, after time.Duration) {
	t.Helper()
	select {
	case idle := <-w.done:
		if !idle {
			t.Fatal("waitIdle returned false, want idle")
		}
		if got := w.clock.Now().Sub(idleT0); got != after {
			t.Errorf("idle after %v, want %v", got, after)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitIdle did not return")
	}
}

func TestWaitIdle_ReturnsOnceNoSessionWasLiveForTheWholeSpan(t *testing.T) {
	w := startIdleWatch(t, time.Minute, false)

	for range 3 {
		w.check(t)
		w.assertWaiting(t)
	}
	w.check(t)

	w.assertIdle(t, time.Minute)
}

func TestWaitIdle_ALiveSessionStartsTheSpanAgain(t *testing.T) {
	w := startIdleWatch(t, time.Minute, false)
	w.check(t)
	w.check(t)
	w.assertWaiting(t)
	w.live.Store(true)
	w.check(t) // 45 s: live
	w.assertWaiting(t)
	w.live.Store(false)

	// Idle is first seen at 60 s, so the span ends at 120 s.
	for range 4 {
		w.check(t)
		w.assertWaiting(t)
	}
	w.check(t)

	w.assertIdle(t, 2*time.Minute)
}

func TestWaitIdle_StopsWithItsContext(t *testing.T) {
	w := startIdleWatch(t, time.Minute, true)
	w.clock.WaitForTimers(t, 1)

	w.cancel()

	select {
	case idle := <-w.done:
		if idle {
			t.Error("waitIdle returned true after its context ended")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitIdle did not return after its context ended")
	}
	if pending := w.clock.Pending(); len(pending) != 0 {
		t.Errorf("timers %v left pending", pending)
	}
}

// noClaude is a Runner for tests that must never run claude.
type noClaude struct{ t *testing.T }

func (c noClaude) Run(context.Context, domain.ConfigDir, ...string) ([]byte, error) {
	c.t.Error("ran claude")
	return nil, errors.New("no claude here")
}

// A drain that shutdown interrupts is not a failure of the events it did
// not reach: they stay spooled for the next server, none of them set aside.
func TestDrain_ShutdownLeavesTheRestSpooled(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{DataDir: dir, SpoolDir: filepath.Join(dir, "spool")}
	spool := forward.Spool{Dir: paths.SpoolDir}
	for i := range 3 {
		env := protocol.Envelope{
			V: protocol.SchemaVersion, ID: fmt.Sprintf("%016x", i+1), Kind: protocol.KindHook, At: idleT0,
			ConfigDir: dir, Payload: json.RawMessage(fmt.Sprintf(`{"session_id":"s%d","hook_event_name":"Stop"}`, i)),
		}
		if err := spool.Put(env); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	clock := clocktest.New(idleT0)
	s := newInstance(t.Context(), ServeOptions{Paths: paths, Home: dir, Runner: noClaude{t}, Clock: clock.Now, Ticker: clock},
		config.Defaults(), 8289, []byte(strings.Repeat("ab", 32)), slog.New(slog.DiscardHandler))
	defer s.tracker.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	s.drain(ctx)

	left, err := os.ReadDir(paths.SpoolDir)
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	if len(left) != 3 {
		t.Errorf("spool holds %d entries, want the three events left as they were", len(left))
	}
	if got := s.tracker.Snapshot().Sessions; len(got) != 0 {
		t.Errorf("applied %d sessions after shutdown began, want none", len(got))
	}
}

// The server can be started by an account-B hook, and so inherit B's
// CLAUDE_CONFIG_DIR. Claude Code's default dir still comes from the home
// directory alone, so that polling it unsets the variable.
func TestClaudeRunner_DefaultDirComesFromTheHomeDirAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude-b"))

	runner := claudeRunner(config.Config{ClaudePath: filepath.Join(home, "bin", "claude")}, home)

	if want := config.ResolveConfigDir("", home); runner.DefaultDir != want {
		t.Errorf("DefaultDir = %q, want %q", runner.DefaultDir, want)
	}
	if want := filepath.Join(home, "bin", "claude"); runner.Path != want {
		t.Errorf("Path = %q, want the configured %q", runner.Path, want)
	}
}

// A stop request repeated while the server stops is answered as the first
// was, and asks nothing more of it.
func TestInstance_AnswersAStopRequestRepeatedWhileItStops(t *testing.T) {
	const port = 40001 // named in the Host header only; nothing listens
	token := []byte(strings.Repeat("ab", 32))
	dir := t.TempDir()
	o := ServeOptions{Paths: config.Paths{DataDir: dir, SpoolDir: filepath.Join(dir, "spool")}, Demo: true, NoOpen: true}
	s := newInstance(t.Context(), o, config.Defaults(), port, token, slog.New(slog.DiscardHandler))
	t.Cleanup(s.tracker.Close)

	for i := range 2 {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/stop", http.NoBody)
		req.Host = fmt.Sprintf("127.0.0.1:%d", port)
		req.Header.Set("X-Loadline-Token", string(token))
		rec := httptest.NewRecorder()
		s.srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("stop request %d = %d %q, want 200", i+1, rec.Code, rec.Body.String())
		}
	}
	select {
	case <-s.stopAsked:
	default:
		t.Error("the server was not asked to stop")
	}
}
