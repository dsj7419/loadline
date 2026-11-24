package server_test

import (
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"testing"

	"github.com/dsj7419/loadline/internal/server"
)

// stops counts the stop requests a server accepted.
type stops struct{ n atomic.Int32 }

func (s *stops) deps(d *server.Deps) { d.Stop = func() { s.n.Add(1) } }

func stopRequest() *http.Request { return tokenRequest(http.MethodPost, "/api/stop", nil) }

// A stop request ends the server, so it takes the token, which only the
// user's own processes hold. Never the cookie: a browser sends it to every
// port on 127.0.0.1, while a page of another origin cannot send the token's
// header without a preflight the server never grants.
func TestStop_RouteTable(t *testing.T) {
	refused := []struct {
		name    string
		request func() *http.Request
		status  int
	}{
		{"the cookie alone", func() *http.Request {
			r := request(http.MethodPost, "/api/stop", nil)
			addCookie(r, uiCookie, uiCookieValue(testToken))
			return r
		}, http.StatusUnauthorized},
		{"no credential", func() *http.Request { return request(http.MethodPost, "/api/stop", nil) }, http.StatusUnauthorized},
		{"the cookie's value as the token", func() *http.Request {
			r := request(http.MethodPost, "/api/stop", nil)
			r.Header.Set(tokenHeader, uiCookieValue(testToken))
			return r
		}, http.StatusUnauthorized},
		{"a foreign Origin", func() *http.Request {
			r := stopRequest()
			r.Header.Set("Origin", "http://evil.example")
			return r
		}, http.StatusForbidden},
		{"a wrong Host", func() *http.Request {
			r := stopRequest()
			r.Host = "evil.example:8289"
			return r
		}, http.StatusMisdirectedRequest},
		{"GET", func() *http.Request { return tokenRequest(http.MethodGet, "/api/stop", nil) }, http.StatusMethodNotAllowed},
		{"HEAD", func() *http.Request { return tokenRequest(http.MethodHead, "/api/stop", nil) }, http.StatusMethodNotAllowed},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			var s stops
			h := newHarness(t, s.deps)

			rec := h.do(tt.request())

			assertStatus(t, rec, tt.status)
			if n := s.n.Load(); n != 0 {
				t.Errorf("Stop called %d times, want none", n)
			}
		})
	}

	accepted := map[string][]func(*server.Deps){
		"the token":            nil,
		"in demo mode":         {demoDeps},
		"while ingest is held": {holdDeps},
	}
	for name, opts := range accepted {
		t.Run(name, func(t *testing.T) {
			var s stops
			h := newHarness(t, append(opts, s.deps)...)

			rec := h.do(stopRequest())

			assertStatus(t, rec, http.StatusOK)
			assertJSONBody(t, rec, fmt.Sprintf(`{"stopping":true,"pid":%d}`, os.Getpid()))
			if n := s.n.Load(); n != 1 {
				t.Errorf("Stop called %d times, want once", n)
			}
		})
	}

	// The route takes POST alone, and says so to every other method.
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		t.Run(method+" is told the route takes POST", func(t *testing.T) {
			var s stops
			h := newHarness(t, s.deps)

			rec := h.do(tokenRequest(method, "/api/stop", nil))

			assertJSONError(t, rec, http.StatusMethodNotAllowed)
			if got := rec.Header().Get("Allow"); got != http.MethodPost {
				t.Errorf("Allow = %q, want POST", got)
			}
			if n := s.n.Load(); n != 0 {
				t.Errorf("Stop called %d times, want none", n)
			}
		})
	}

	// A server that cannot stop on request answers as one from before the
	// route did: 405, which tells the client to say how to end it instead.
	t.Run("no Stop", func(t *testing.T) {
		h := newHarness(t)

		rec := h.do(stopRequest())

		assertJSONError(t, rec, http.StatusMethodNotAllowed)
	})
}
