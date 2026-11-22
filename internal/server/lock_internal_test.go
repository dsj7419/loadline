package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/tracker"
)

type nopSink struct{}

func (nopSink) Apply(domain.Event)          {}
func (nopSink) RecordAPI(domain.APIRequest) {}
func (nopSink) Snapshot() tracker.View      { return tracker.View{} }
func (nopSink) Subscribe() (<-chan tracker.View, func()) {
	return make(chan tracker.View), func() {}
}

// locked reports 1 when mu is held, and 0 otherwise.
func locked(mu *sync.Mutex) int {
	if mu.TryLock() {
		mu.Unlock()
		return 0
	}
	return 1
}

// The rate limit and the recent event IDs must read the time while they hold
// their locks: requests that race for a lock then see the clock in the order
// they are served, and none looks like the clock stepping back.
func TestIngest_ReadsTheClockInsideTheLimiterAndDedupLocks(t *testing.T) {
	var srv *Server
	var underLimiter, underDedup int
	clock := func() time.Time {
		if srv != nil {
			underLimiter += locked(&srv.limiter.mu)
			underDedup += locked(&srv.recent.mu)
		}
		return time.Date(2026, time.November, 21, 18, 0, 0, 0, time.UTC)
	}
	token := []byte(strings.Repeat("ab", 32))
	srv = New(Deps{State: nopSink{}, Token: token, Port: 8289, Clock: clock})
	body, err := json.Marshal(protocol.Envelope{
		V:         protocol.SchemaVersion,
		ID:        "0123456789abcdef",
		Kind:      protocol.KindStatus,
		At:        time.Date(2026, time.November, 21, 17, 59, 0, 0, time.UTC),
		ConfigDir: filepath.Join(os.TempDir(), ".claude"),
		Payload:   json.RawMessage(`{"session_id":"s1"}`),
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest/status", bytes.NewReader(body))
	r.Host = "127.0.0.1:8289"
	r.Header.Set(tokenHeader, string(token))
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %q", rec.Code, rec.Body.String())
	}
	if underLimiter == 0 {
		t.Error("the rate limit read the clock without holding its lock")
	}
	if underDedup == 0 {
		t.Error("the recent event IDs read the clock without holding their lock")
	}
}
