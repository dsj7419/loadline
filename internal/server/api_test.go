package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/tracker"
)

// testView is a view with something in it, so that serving it is told apart
// from serving the zero view.
func testView(generatedAt time.Time) tracker.View {
	return tracker.View{
		GeneratedAt: generatedAt,
		Accounts:    []tracker.AccountView{{Dir: "/home/dev/.claude", Label: "main", Models: []tracker.ModelTotal{}, ModelsWeek: []tracker.ModelTotal{}}},
		Sessions:    []tracker.SessionView{},
		Models:      []tracker.ModelTotal{},
		ModelsWeek:  []tracker.ModelTotal{},
		Alerts:      []tracker.AlertView{},
	}
}

func marshalView(t *testing.T, v tracker.View) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	return string(b)
}

func TestUI_RequiresCookie(t *testing.T) {
	credentials := []struct {
		name string
		set  func(r *http.Request)
	}{
		{name: "nothing", set: func(*http.Request) {}},
		{name: "empty cookie", set: func(r *http.Request) { addCookie(r, uiCookie, "") }},
		{name: "wrong cookie", set: func(r *http.Request) {
			addCookie(r, uiCookie, strings.Repeat("0", 64))
		}},
		{name: "cookie for another token", set: func(r *http.Request) {
			addCookie(r, uiCookie, uiCookieValue([]byte(strings.Repeat("9", 64))))
		}},
		{name: "cookie under another name", set: func(r *http.Request) {
			addCookie(r, "loadline", uiCookieValue(testToken))
		}},
		{name: "the token as the cookie", set: func(r *http.Request) {
			addCookie(r, uiCookie, string(testToken))
		}},
		{name: "wrong token", set: func(r *http.Request) { r.Header.Set(tokenHeader, strings.Repeat("f", 64)) }},
		{name: "the cookie as the token", set: func(r *http.Request) { r.Header.Set(tokenHeader, uiCookieValue(testToken)) }},
	}
	for _, route := range []string{"/api/snapshot", "/api/events"} {
		for _, cred := range credentials {
			t.Run(route+"/"+cred.name, func(t *testing.T) {
				h := newHarness(t)
				r := withDeadline(t, request(http.MethodGet, route, nil))
				cred.set(r)

				assertJSONError(t, h.do(r), http.StatusUnauthorized)
				if calls := h.sink.callOrder(); len(calls) != 0 {
					t.Errorf("the state was read (%v) for an unauthenticated request", calls)
				}
			})
		}
	}
}

func TestSnapshot_ServesTheCurrentView(t *testing.T) {
	view := testView(t0)
	for _, tt := range []struct {
		name string
		req  *http.Request
	}{
		{name: "cookie", req: cookieRequest("/api/snapshot")},
		{name: "token", req: tokenRequest(http.MethodGet, "/api/snapshot", nil)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.sink.setView(view)

			rec := h.do(tt.req)

			assertStatus(t, rec, http.StatusOK)
			assertJSONBody(t, rec, marshalView(t, view))
		})
	}
}

// unencodableView is a view that json.Marshal refuses: its session is in a
// state with no name.
func unencodableView() tracker.View {
	v := testView(t0)
	v.Sessions = []tracker.SessionView{{ID: "s1", State: domain.State(200)}}
	return v
}

func TestSnapshot_UnencodableViewIsAServerError(t *testing.T) {
	h := newHarness(t)
	h.sink.setView(unencodableView())

	assertJSONError(t, h.do(tokenRequest(http.MethodGet, "/api/snapshot", nil)), http.StatusInternalServerError)
}

func TestDiag_ReportsFeeds(t *testing.T) {
	h := newHarness(t)
	diag := func() string {
		t.Helper()
		rec := h.do(tokenRequest(http.MethodGet, "/api/diag", nil))
		assertStatus(t, rec, http.StatusOK)
		return strings.TrimSpace(rec.Body.String())
	}

	want := `{"app":"loadline","version":"1.2.3","clients":0,` +
		`"feeds":{"hook":null,"status":null,"otlp":null,"poll":null}}`
	if got := diag(); got != want {
		t.Fatalf("diag before any feed = %s, want %s", got, want)
	}
	before := h.srv.Feeds()
	for _, feed := range []string{"hook", "status", "otlp", "poll"} {
		if at, ok := before[feed]; !ok || !at.IsZero() {
			t.Errorf("Feeds()[%q] = %v, %t before any receipt, want the zero time", feed, at, ok)
		}
	}

	// Rejected requests are no sign of a working feed.
	assertJSONError(t, h.do(hookRequest(nil)), http.StatusBadRequest)
	assertJSONError(t, h.do(statusRequest(nil)), http.StatusBadRequest)
	assertJSONError(t, h.do(logsRequest(nil)), http.StatusBadRequest)
	unauthorized := hookRequest(hookEnvelope(t, eventID(1)))
	unauthorized.Header.Del(tokenHeader)
	assertJSONError(t, h.do(unauthorized), http.StatusUnauthorized)
	if got := diag(); got != want {
		t.Fatalf("diag after rejected requests = %s, want %s", got, want)
	}

	h.clock.Advance(time.Second)
	assertStatus(t, h.do(hookRequest(hookEnvelope(t, eventID(1)))), http.StatusOK)
	h.clock.Advance(time.Second)
	assertStatus(t, h.do(statusRequest(statusEnvelope(t, eventID(2)))), http.StatusOK)
	h.clock.Advance(time.Second)
	assertStatus(t, h.do(logsRequest([]byte(`{"resourceLogs":[]}`))), http.StatusOK)
	h.srv.NotePoll(time.Date(2026, time.November, 21, 10, 0, 4, 500_000_000, time.FixedZone("test", -8*3600)))

	want = `{"app":"loadline","version":"1.2.3","clients":0,"feeds":{` +
		`"hook":"2026-11-21T18:00:01Z","status":"2026-11-21T18:00:02Z",` +
		`"otlp":"2026-11-21T18:00:03Z","poll":"2026-11-21T18:00:04.5Z"}}`
	if got := diag(); got != want {
		t.Errorf("diag = %s, want %s", got, want)
	}
	feeds := h.srv.Feeds()
	wantFeeds := map[string]time.Time{
		"hook":   t0.Add(time.Second),
		"status": t0.Add(2 * time.Second),
		"otlp":   t0.Add(3 * time.Second),
		"poll":   t0.Add(4500 * time.Millisecond),
	}
	if len(feeds) != len(wantFeeds) {
		t.Errorf("Feeds() = %v, want %v", feeds, wantFeeds)
	}
	for feed, at := range wantFeeds {
		if !feeds[feed].Equal(at) {
			t.Errorf("Feeds()[%q] = %v, want %v", feed, feeds[feed], at)
		}
	}
}
