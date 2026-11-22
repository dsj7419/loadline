package forward_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/tracker"
)

const tokenHeader = "X-Loadline-Token" //nolint:gosec // G101: the name of the header, not a credential

var (
	testToken = []byte("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	t0        = time.Date(2026, time.November, 21, 18, 0, 0, 0, time.UTC)
	configDir = filepath.Join(os.TempDir(), "loadline-forward-test", ".claude")
)

// raceSlowdown is how much longer the race detector may make the hot path
// take.
const raceSlowdown = 5

// assertGaveUpAt fails t unless elapsed, how long call took against a
// server that does not answer, shows that it gave up at deadline: no
// sooner, which no load can bring about, and before a margin past it for a
// loaded machine to wake the caller and let it finish. The margin grows
// with the deadline: four times it, and raceSlowdown times that with the
// race detector, which slows the work around the deadline.
func assertGaveUpAt(t *testing.T, call string, elapsed, deadline time.Duration) {
	t.Helper()
	lateness := 4 * deadline
	if raceEnabled {
		lateness *= raceSlowdown
	}
	if limit := deadline + lateness; elapsed < deadline || elapsed >= limit {
		t.Errorf("%s returned after %v, want it to give up at its %v deadline: no sooner, and before %v", call, elapsed, deadline, limit)
	}
}

// envelopeAt returns a valid hook envelope for session s<i>, stamped at.
func envelopeAt(i int, at time.Time) protocol.Envelope {
	return protocol.Envelope{
		V:         protocol.SchemaVersion,
		ID:        fmt.Sprintf("%016x", i),
		Kind:      protocol.KindHook,
		At:        at,
		ConfigDir: configDir,
		PID:       4242,
		Payload:   json.RawMessage(fmt.Sprintf(`{"session_id":"s%d","hook_event_name":"Stop"}`, i)),
	}
}

// unstartedServer returns an HTTP server on a free loopback port, not yet
// serving, and its port.
func unstartedServer(t *testing.T) (*httptest.Server, int) {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	t.Cleanup(ts.Close)
	_, portText, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse listener port: %v", err)
	}
	return ts, port
}

// startServer serves h on a free loopback port and returns its base URL.
func startServer(t *testing.T, h http.Handler) string {
	t.Helper()
	ts, _ := unstartedServer(t)
	ts.Config.Handler = h
	ts.Start()
	return ts.URL
}

// startHungServer serves a handler that never answers and returns its base
// URL. A request is held until its client gives up or the test ends. The
// handler reads the body first: only then does the server notice a client
// hanging up.
func startHungServer(t *testing.T) string {
	t.Helper()
	release := make(chan struct{})
	url := startServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	// Registered after the server's own cleanup, so it runs first and the
	// server's Close does not wait on a held request.
	t.Cleanup(func() { close(release) })
	return url
}

// closedPortURL returns the base URL of a loopback port nothing listens on.
func closedPortURL(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	url := "http://" + l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return url
}

// recordingSink is the live state the real server feeds in the contract
// tests. It records the events applied.
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
