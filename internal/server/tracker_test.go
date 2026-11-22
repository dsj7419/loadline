package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/server"
	"github.com/dsj7419/loadline/internal/tracker"
)

// The tests here run the server in front of a real tracker, to prove what
// no single package can: that an event the routes take never leaves the
// state unable to be served.

var trackedAlerts = domain.AlertConfig{WeeklyPct: 90, ContextPct: 85, NeedsYouAfter: 20 * time.Second}

// trackedServer is a server in front of a real tracker, both reading clock.
func trackedServer(t *testing.T, clock func() time.Time) *server.Server {
	t.Helper()
	tr := tracker.New(clock, tracker.Config{Alerts: trackedAlerts}, nil)
	t.Cleanup(tr.Close)
	return server.New(server.Deps{State: tr, Token: testToken, Port: testPort, Clock: clock})
}

func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

// rawEnvelope is an envelope whose "at" is the text given, so that a time no
// time.Time marshals can still be sent.
func rawEnvelope(t *testing.T, id string, kind protocol.Kind, at, payload string) string {
	t.Helper()
	dir, err := json.Marshal(configDir)
	if err != nil {
		t.Fatalf("marshal config dir: %v", err)
	}
	return fmt.Sprintf(`{"v":1,"id":%q,"kind":%q,"at":%q,"configDir":%s,"payload":%s}`, id, kind, at, dir, payload)
}

func post(h http.Handler, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, tokenRequest(http.MethodPost, path, []byte(body)))
	return rec
}

func getSnapshot(h http.Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, tokenRequest(http.MethodGet, "/api/snapshot", nil))
	return rec
}

// Each of these times parses as RFC 3339 but lies outside the years a
// time.Time marshals once in UTC, as every view time is.
func TestTracked_EventTimeOutsideTheCalendarIsRefusedAndTheStateStillServes(t *testing.T) {
	cases := []struct{ name, route, at, payload string }{
		{"hook, year 10000 in UTC", "/ingest/hook", "9999-12-31T23:59:59-01:00", `{"session_id":"s1","hook_event_name":"SessionStart"}`},
		{"hook, year -1 in UTC", "/ingest/hook", "0000-01-01T00:00:00+01:00", `{"session_id":"s1","hook_event_name":"SessionStart"}`},
		{"status, year 10000 in UTC", "/ingest/status", "9999-12-31T23:59:59-01:00", `{"session_id":"s1"}`},
		{"status, year -1 in UTC", "/ingest/status", "0000-01-01T00:00:00+01:00", `{"session_id":"s1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := trackedServer(t, fixedClock(t0))
			h := srv.Handler()
			kind := protocol.KindHook
			if strings.HasSuffix(tc.route, "status") {
				kind = protocol.KindStatus
			}

			rec := post(h, tc.route, rawEnvelope(t, eventID(1), kind, tc.at, tc.payload))

			assertJSONError(t, rec, http.StatusBadRequest)
			assertJSONBody(t, rec, `{"error":"invalid envelope"}`)
			if snap := getSnapshot(h); snap.Code != http.StatusOK {
				t.Errorf("after the refused event, GET /api/snapshot = %d %s, want 200", snap.Code, bytes.TrimSpace(snap.Body.Bytes()))
			}
		})
	}
}

// A spooled event with such a time is set aside by the drain, which holds it
// to the envelope's checks, and the state still serves.
func TestTracked_SpooledEventTimeOutsideTheCalendarIsSetAside(t *testing.T) {
	srv := trackedServer(t, fixedClock(t0))
	spool := forward.Spool{Dir: filepath.Join(t.TempDir(), "spool")}
	if err := os.MkdirAll(spool.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := rawEnvelope(t, eventID(2), protocol.KindHook, "9999-12-31T23:59:59-01:00", `{"session_id":"s1","hook_event_name":"SessionStart"}`)
	if err := os.WriteFile(filepath.Join(spool.Dir, "1795300000000000000-0000abcd.json"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}

	drained, err := spool.Drain(func(env protocol.Envelope) error {
		if err := srv.ApplySpooledHook(env); err != nil {
			return fmt.Errorf("%w: %w", forward.ErrUndeliverable, err)
		}
		return nil
	})

	if drained != 0 || err != nil {
		t.Errorf("Drain = %d, %v; want nothing applied and no error", drained, err)
	}
	if bad, _ := os.ReadDir(filepath.Join(spool.Dir, "bad")); len(bad) != 1 {
		t.Errorf("bad/ holds %d files, want the one with the impossible time", len(bad))
	}
	if snap := getSnapshot(srv.Handler()); snap.Code != http.StatusOK {
		t.Errorf("after the drain, GET /api/snapshot = %d %s, want 200", snap.Code, bytes.TrimSpace(snap.Body.Bytes()))
	}
}

// ApplySpooledHook holds an envelope to the route's checks even when nothing
// checked it before.
func TestTracked_ApplySpooledHookRefusesAnEventTimeOutsideTheCalendar(t *testing.T) {
	srv := trackedServer(t, fixedClock(t0))
	var env protocol.Envelope
	raw := rawEnvelope(t, eventID(3), protocol.KindHook, "9999-12-31T23:59:59-01:00", `{"session_id":"s1","hook_event_name":"SessionStart"}`)
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if err := srv.ApplySpooledHook(env); err == nil {
		t.Error("ApplySpooledHook = nil, want the event refused")
	}
	if snap := getSnapshot(srv.Handler()); snap.Code != http.StatusOK {
		t.Errorf("GET /api/snapshot = %d %s, want 200", snap.Code, bytes.TrimSpace(snap.Body.Bytes()))
	}
}
