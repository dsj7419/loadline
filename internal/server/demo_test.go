package server_test

import (
	"net/http"
	"testing"

	"github.com/dsj7419/loadline/internal/server"
)

func demoDeps(d *server.Deps) { d.Demo = true }

// A demo server shows a made-up machine. Were it to take the real hooks'
// events it would show them as made up, and the hooks, finding them taken,
// would not keep them for the real server.
func TestDemo_RefusesEveryIngestRoute(t *testing.T) {
	routes := map[string]func() *http.Request{
		"hook":   func() *http.Request { return hookRequest(hookEnvelope(t, eventID(1))) },
		"status": func() *http.Request { return statusRequest(statusEnvelope(t, eventID(2))) },
		"logs":   func() *http.Request { return logsRequest(logsExport(apiRequestRecord("s1", "m", 1))) },
	}
	for name, route := range routes {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, demoDeps)

			rec := h.do(route())

			assertJSONError(t, rec, http.StatusServiceUnavailable)
			assertJSONBody(t, rec, `{"error":"demo mode"}`)
			h.assertNothingApplied(t)
			if at := h.srv.Feeds()[name]; !at.IsZero() {
				t.Errorf("%s feed noted at %v, want nothing received", name, at)
			}
		})
	}
}

func TestDemo_IngestStillNeedsTheToken(t *testing.T) {
	h := newHarness(t, demoDeps)
	r := hookRequest(hookEnvelope(t, eventID(1)))
	r.Header.Del(tokenHeader)

	assertJSONError(t, h.do(r), http.StatusUnauthorized)
}

func TestDemo_ServesEverythingElse(t *testing.T) {
	h := newHarness(t, demoDeps)

	assertStatus(t, h.do(request(http.MethodGet, "/healthz", nil)), http.StatusOK)
	assertStatus(t, h.do(cookieRequest("/api/snapshot")), http.StatusOK)
	assertStatus(t, h.do(request(http.MethodGet, "/", nil)), http.StatusOK)
	assertStatus(t, h.do(tokenRequest(http.MethodGet, "/api/diag", nil)), http.StatusOK)
	assertStatus(t, h.do(tokenRequest(http.MethodPost, "/api/login-code", nil)), http.StatusOK)
	assertLoggedIn(t, login(h, h.srv.MintLoginCode()))
}
