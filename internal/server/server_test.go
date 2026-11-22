package server_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/server"
	"github.com/dsj7419/loadline/internal/tracker"
)

const (
	testPort    = 8289
	testHost    = "127.0.0.1:8289"
	testVersion = "1.2.3"
	tokenHeader = "X-Loadline-Token" //nolint:gosec // G101: the name of the header, not a credential
	uiCookie    = "loadline_ui"

	contentSecurityPolicy = "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; " +
		"script-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

	// canary appears in request bodies that must be rejected. A response
	// that contains it echoes request content.
	canary = "canary-7d3f"
)

var (
	testToken = []byte("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")

	// t0 is when every test's clock starts.
	t0 = time.Date(2026, time.November, 21, 18, 0, 0, 0, time.UTC)
	// eventTime is when the test envelopes say their events happened.
	eventTime = time.Date(2026, time.November, 21, 17, 59, 58, 0, time.UTC)

	configDir = filepath.Join(os.TempDir(), "loadline-server-test", ".claude")

	testFiles = fstest.MapFS{
		"index.html":      {Data: []byte("<!doctype html><title>Loadline</title>")},
		"app.js":          {Data: []byte("console.log('loadline')")},
		"assets/app.css":  {Data: []byte("body{margin:0}")},
		"docs/index.html": {Data: []byte("<p>docs</p>")},
		// odd holds a directory named index.html, which is no index page.
		"odd/index.html/readme.txt": {Data: []byte("not a page")},
	}
)

// uiCookieValue is the cookie value the server must issue and accept for
// token.
func uiCookieValue(token []byte) string {
	mac := hmac.New(sha256.New, token)
	mac.Write([]byte("ui"))
	return hex.EncodeToString(mac.Sum(nil))
}

// The test doubles.

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

// fakeSink records what the server feeds it and serves the view a test sets.
type fakeSink struct {
	mu           sync.Mutex
	events       []domain.Event
	requests     []domain.APIRequest
	view         tracker.View
	calls        []string
	subscribers  []chan tracker.View
	unsubscribed int
}

func (f *fakeSink) Apply(e domain.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeSink) RecordAPI(r domain.APIRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
}

func (f *fakeSink) Snapshot() tracker.View {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "snapshot")
	return f.view
}

func (f *fakeSink) Subscribe() (<-chan tracker.View, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "subscribe")
	ch := make(chan tracker.View, 1)
	f.subscribers = append(f.subscribers, ch)
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.unsubscribed++
		})
	}
}

func (f *fakeSink) setView(v tracker.View) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.view = v
}

// publish hands v to every subscriber, as the tracker does.
func (f *fakeSink) publish(v tracker.View) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.subscribers {
		ch <- v
	}
}

// closeSubscriptions closes every subscriber's channel, as the tracker's
// Close does.
func (f *fakeSink) closeSubscriptions() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.subscribers {
		close(ch)
	}
	f.subscribers = nil
}

func (f *fakeSink) recorded() (events []domain.Event, requests []domain.APIRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Event(nil), f.events...), append([]domain.APIRequest(nil), f.requests...)
}

func (f *fakeSink) callOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeSink) unsubscribes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unsubscribed
}

// harness is a server wired to test doubles.
type harness struct {
	srv     *server.Server
	sink    *fakeSink
	clock   *fakeClock
	handler http.Handler

	mu    sync.Mutex
	kicks int
	dirs  []domain.ConfigDir
}

func newHarness(t *testing.T, opts ...func(*server.Deps)) *harness {
	t.Helper()
	h := &harness{sink: &fakeSink{}, clock: &fakeClock{now: t0}}
	deps := server.Deps{
		State:   h.sink,
		Kick:    h.kick,
		OnDir:   h.onDir,
		Token:   testToken,
		Port:    testPort,
		Static:  testFiles,
		Version: testVersion,
		Clock:   h.clock.Now,
	}
	for _, opt := range opts {
		opt(&deps)
	}
	h.srv = server.New(deps)
	h.handler = h.srv.Handler()
	return h
}

func (h *harness) kick() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.kicks++
}

func (h *harness) onDir(dir domain.ConfigDir) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dirs = append(h.dirs, dir)
}

func (h *harness) observed() (kicks int, dirs []domain.ConfigDir) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.kicks, append([]domain.ConfigDir(nil), h.dirs...)
}

func (h *harness) do(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, r)
	return rec
}

// assertNothingApplied fails when the sink or the callbacks saw anything.
func (h *harness) assertNothingApplied(t *testing.T) {
	t.Helper()
	events, requests := h.sink.recorded()
	kicks, dirs := h.observed()
	if len(events) != 0 || len(requests) != 0 || kicks != 0 || len(dirs) != 0 {
		t.Errorf("applied %d events and %d requests, kicked %d times, noted dirs %v; want nothing",
			len(events), len(requests), kicks, dirs)
	}
}

// Requests.

func request(method, target string, body []byte) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), method, target, bytes.NewReader(body))
	r.Host = testHost
	return r
}

func tokenRequest(method, target string, body []byte) *http.Request {
	r := request(method, target, body)
	r.Header.Set(tokenHeader, string(testToken))
	return r
}

// withDeadline bounds r, so that an event stream admitted by mistake ends
// with a failure instead of hanging the test.
func withDeadline(t *testing.T, r *http.Request) *http.Request {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	t.Cleanup(cancel)
	return r.WithContext(ctx)
}

// addCookie sends a cookie with r, as a browser holding it would.
func addCookie(r *http.Request, name, value string) {
	r.Header.Add("Cookie", name+"="+value)
}

func cookieRequest(target string) *http.Request {
	r := request(http.MethodGet, target, nil)
	addCookie(r, uiCookie, uiCookieValue(testToken))
	return r
}

func hookRequest(body []byte) *http.Request {
	return tokenRequest(http.MethodPost, "/ingest/hook", body)
}

func statusRequest(body []byte) *http.Request {
	return tokenRequest(http.MethodPost, "/ingest/status", body)
}

func logsRequest(body []byte) *http.Request {
	r := tokenRequest(http.MethodPost, "/v1/logs", body)
	r.Header.Set("Content-Type", "application/json")
	return r
}

// Envelopes.

const (
	hookPayload = `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup",` +
		`"prompt":"` + canary + `"}`
	statusPayload = `{"session_id":"s1","model":{"id":"claude-opus-5-5","display_name":"Opus"},` +
		`"rate_limits":{"seven_day":{"used_percentage":42,"resets_at":1795400000}}}`
)

// eventID returns the i-th distinct event ID.
func eventID(i int) string { return fmt.Sprintf("%016x", i) }

func envelope(t *testing.T, kind protocol.Kind, id, payload string) []byte {
	t.Helper()
	b, err := json.Marshal(protocol.Envelope{
		V:         protocol.SchemaVersion,
		ID:        id,
		Kind:      kind,
		At:        eventTime,
		ConfigDir: configDir,
		PID:       4242,
		Payload:   json.RawMessage(payload),
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return b
}

func hookEnvelope(t *testing.T, id string) []byte {
	t.Helper()
	return envelope(t, protocol.KindHook, id, hookPayload)
}

func statusEnvelope(t *testing.T, id string) []byte {
	t.Helper()
	return envelope(t, protocol.KindStatus, id, statusPayload)
}

// envelopeWith returns a valid hook envelope after edit changed its JSON
// fields. A field edit deletes is absent.
func envelopeWith(t *testing.T, kind protocol.Kind, payload string, edit func(map[string]json.RawMessage)) []byte {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope(t, kind, eventID(1), payload), &fields); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	edit(fields)
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return b
}

// padTo pads a JSON body with trailing spaces to exactly n bytes.
func padTo(t *testing.T, body []byte, n int) []byte {
	t.Helper()
	if len(body) > n {
		t.Fatalf("body is %d bytes, longer than %d", len(body), n)
	}
	padded := bytes.Repeat([]byte(" "), n)
	copy(padded, body)
	return padded
}

// Responses.

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body %q", rec.Code, want, rec.Body.String())
	}
}

// assertJSONError checks that rec is a short JSON error that echoes nothing
// of the request.
func assertJSONError(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	assertStatus(t, rec, want)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Error string `json:"error"`
	}
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Error == "" {
		t.Errorf("body = %q, want {\"error\":\"<reason>\"}", rec.Body.String())
	}
	for _, secret := range []string{canary, string(testToken), uiCookieValue(testToken)} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("body %q echoes request content", rec.Body.String())
		}
	}
}

func assertJSONBody(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

// Host and Origin.

func TestHost_RejectsForeignHost(t *testing.T) {
	hosts := []string{
		"evil.example:8289",
		"evil.example",
		"127.0.0.1:9999",
		"localhost:9999",
		"127.0.0.1",
		"localhost",
		"127.0.0.1.evil.example:8289",
		"[::1]:8289",
		"0.0.0.0:8289",
		"",
	}
	for _, host := range hosts {
		t.Run(host, func(t *testing.T) {
			h := newHarness(t)
			requests := []*http.Request{
				request(http.MethodGet, "/healthz", nil),
				hookRequest(hookEnvelope(t, eventID(1))),
				statusRequest(statusEnvelope(t, eventID(2))),
				logsRequest([]byte(`{"resourceLogs":[]}`)),
				tokenRequest(http.MethodGet, "/api/snapshot", nil),
				cookieRequest("/api/events"),
				tokenRequest(http.MethodGet, "/api/diag", nil),
				tokenRequest(http.MethodPost, "/api/login-code", nil),
				request(http.MethodGet, "/login?code="+h.srv.MintLoginCode(), nil),
				request(http.MethodGet, "/", nil),
			}
			for _, r := range requests {
				r.Host = host
				rec := h.do(r)
				assertJSONError(t, rec, http.StatusMisdirectedRequest)
				if rec.Header().Get("Set-Cookie") != "" {
					t.Errorf("%s %s set a cookie for host %q", r.Method, r.URL, host)
				}
			}
			h.assertNothingApplied(t)
		})
	}
}

func TestHost_AllowsLocalhostAndLoopback(t *testing.T) {
	for _, host := range []string{"127.0.0.1:8289", "localhost:8289"} {
		t.Run(host, func(t *testing.T) {
			h := newHarness(t)

			health := request(http.MethodGet, "/healthz", nil)
			health.Host = host
			assertStatus(t, h.do(health), http.StatusOK)

			hook := hookRequest(hookEnvelope(t, eventID(1)))
			hook.Host = host
			assertStatus(t, h.do(hook), http.StatusOK)

			page := request(http.MethodGet, "/", nil)
			page.Host = host
			assertStatus(t, h.do(page), http.StatusOK)
		})
	}
}

func TestOrigin_RejectsForeignOriginOnPost(t *testing.T) {
	foreign := []string{
		"http://evil.example",
		"http://evil.example:8289",
		"https://127.0.0.1:8289",
		"http://127.0.0.1:9999",
		"http://127.0.0.1:8289/",
		"http://127.0.0.1",
		"null",
		"",
	}
	for _, origin := range foreign {
		t.Run("foreign "+origin, func(t *testing.T) {
			h := newHarness(t)
			for _, r := range []*http.Request{
				hookRequest(hookEnvelope(t, eventID(1))),
				statusRequest(statusEnvelope(t, eventID(2))),
				logsRequest([]byte(`{"resourceLogs":[]}`)),
				tokenRequest(http.MethodPost, "/api/login-code", nil),
			} {
				r.Header.Set("Origin", origin)
				assertJSONError(t, h.do(r), http.StatusForbidden)
			}
			h.assertNothingApplied(t)
		})
	}

	allowed := []struct {
		name   string
		origin []string
	}{
		{name: "same origin by address", origin: []string{"http://127.0.0.1:8289"}},
		{name: "same origin by name", origin: []string{"http://localhost:8289"}},
		{name: "absent"},
	}
	for _, tt := range allowed {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			r := hookRequest(hookEnvelope(t, eventID(1)))
			r.Header["Origin"] = tt.origin
			assertStatus(t, h.do(r), http.StatusOK)

			code := tokenRequest(http.MethodPost, "/api/login-code", nil)
			code.Header["Origin"] = tt.origin
			assertStatus(t, h.do(code), http.StatusOK)
		})
	}

	t.Run("a second, foreign Origin header", func(t *testing.T) {
		h := newHarness(t)
		r := hookRequest(hookEnvelope(t, eventID(1)))
		r.Header["Origin"] = []string{"http://127.0.0.1:8289", "http://evil.example"}
		assertJSONError(t, h.do(r), http.StatusForbidden)
		h.assertNothingApplied(t)
	})

	t.Run("reads are not checked", func(t *testing.T) {
		h := newHarness(t)
		for _, r := range []*http.Request{
			request(http.MethodGet, "/healthz", nil),
			tokenRequest(http.MethodGet, "/api/snapshot", nil),
			request(http.MethodHead, "/", nil),
		} {
			r.Header.Set("Origin", "http://evil.example")
			assertStatus(t, h.do(r), http.StatusOK)
		}
	})
}

func TestAuth_EmptyTokenAdmitsNobody(t *testing.T) {
	h := newHarness(t, func(d *server.Deps) { d.Token = nil })
	routes := []struct {
		method, target string
		body           []byte
	}{
		{method: http.MethodPost, target: "/ingest/hook", body: hookEnvelope(t, eventID(1))},
		{method: http.MethodPost, target: "/api/login-code"},
		{method: http.MethodGet, target: "/api/diag"},
		{method: http.MethodGet, target: "/api/snapshot"},
		{method: http.MethodGet, target: "/api/events"},
	}
	credentials := map[string]func(r *http.Request){
		"nothing":                    func(*http.Request) {},
		"an empty token":             func(r *http.Request) { r.Header.Set(tokenHeader, "") },
		"an empty cookie":            func(r *http.Request) { addCookie(r, uiCookie, "") },
		"the cookie of an empty key": func(r *http.Request) { addCookie(r, uiCookie, uiCookieValue(nil)) },
	}
	for _, route := range routes {
		for name, set := range credentials {
			r := withDeadline(t, request(route.method, route.target, route.body))
			set(r)
			if rec := h.do(r); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with %s: status %d, want 401", route.method, route.target, name, rec.Code)
			}
		}
	}

	for _, setCookie := range login(h, h.srv.MintLoginCode()).Header().Values("Set-Cookie") {
		cookie, err := http.ParseSetCookie(setCookie)
		if err != nil {
			t.Fatalf("parse Set-Cookie: %v", err)
		}
		r := request(http.MethodGet, "/api/snapshot", nil)
		addCookie(r, cookie.Name, cookie.Value)
		if rec := h.do(r); rec.Code != http.StatusUnauthorized {
			t.Errorf("the cookie a login set opened the snapshot (status %d) without a token", rec.Code)
		}
	}
	h.assertNothingApplied(t)
}

// Security headers.

func TestSecurityHeaders_OnEveryResponse(t *testing.T) {
	h := newHarness(t)
	foreignHost := request(http.MethodGet, "/healthz", nil)
	foreignHost.Host = "evil.example:8289"
	foreignOrigin := hookRequest(hookEnvelope(t, eventID(1)))
	foreignOrigin.Header.Set("Origin", "http://evil.example")

	requests := []*http.Request{
		request(http.MethodGet, "/healthz", nil),
		foreignHost,
		foreignOrigin,
		request(http.MethodPost, "/ingest/hook", hookEnvelope(t, eventID(2))),
		hookRequest(hookEnvelope(t, eventID(3))),
		logsRequest([]byte(`{"resourceLogs":[]}`)),
		tokenRequest(http.MethodGet, "/api/snapshot", nil),
		tokenRequest(http.MethodGet, "/api/diag", nil),
		request(http.MethodGet, "/login?code=nope", nil),
		request(http.MethodGet, "/", nil),
		request(http.MethodGet, "/app.js", nil),
		request(http.MethodGet, "/missing.js", nil),
		request(http.MethodPut, "/api/snapshot", nil),
	}
	for _, r := range requests {
		rec := h.do(r)
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s %s (%d): X-Content-Type-Options = %q, want nosniff", r.Method, r.URL, rec.Code, got)
		}
		if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s %s (%d): Referrer-Policy = %q, want no-referrer", r.Method, r.URL, rec.Code, got)
		}
	}
}

func TestSecurityHeaders_HTMLHasContentSecurityPolicy(t *testing.T) {
	h := newHarness(t)
	requests := []*http.Request{
		request(http.MethodGet, "/", nil),
		request(http.MethodGet, "/docs/", nil),
		request(http.MethodGet, "/login?code=nope", nil),
		request(http.MethodGet, "/login?code="+h.srv.MintLoginCode(), nil),
	}
	for _, r := range requests {
		rec := h.do(r)
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s %s (%d): Content-Type = %q, want an HTML response", r.Method, r.URL, rec.Code, ct)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
			t.Errorf("%s %s (%d): Content-Security-Policy = %q, want %q", r.Method, r.URL, rec.Code, got, contentSecurityPolicy)
		}
	}
}

func TestSecurityHeaders_APIResponsesAreNotStored(t *testing.T) {
	h := newHarness(t)
	requests := []*http.Request{
		tokenRequest(http.MethodGet, "/api/snapshot", nil),
		cookieRequest("/api/snapshot"),
		request(http.MethodGet, "/api/snapshot", nil),
		tokenRequest(http.MethodGet, "/api/diag", nil),
		tokenRequest(http.MethodPost, "/api/login-code", nil),
		request(http.MethodGet, "/api/events", nil),
		request(http.MethodGet, "/api/unknown", nil),
		request(http.MethodGet, "/login?code=nope", nil),
		request(http.MethodGet, "/login?code="+h.srv.MintLoginCode(), nil),
		hookRequest(hookEnvelope(t, eventID(1))),
		request(http.MethodPost, "/ingest/hook", hookEnvelope(t, eventID(2))),
		statusRequest(statusEnvelope(t, eventID(3))),
		request(http.MethodGet, "/ingest/hook", nil),
		logsRequest([]byte(`{"resourceLogs":[]}`)),
		logsRequest([]byte(canary)),
	}
	for _, r := range requests {
		rec := h.do(r)
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s %s (%d): Cache-Control = %q, want no-store", r.Method, r.URL, rec.Code, got)
		}
	}
}

func TestHealthz_NoAuth(t *testing.T) {
	h := newHarness(t)

	rec := h.do(request(http.MethodGet, "/healthz", nil))

	assertStatus(t, rec, http.StatusOK)
	assertJSONBody(t, rec, `{"app":"loadline","version":"1.2.3"}`)
}
