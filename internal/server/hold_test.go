package server_test

import (
	"net/http"
	"testing"

	"github.com/dsj7419/loadline/internal/server"
)

func holdDeps(d *server.Deps) { d.HoldIngest = true }

// A server that holds ingest takes no event until its state is in place: an
// event applied before the history is restored would make its session look
// new, and the restored copy would be dropped. The hooks keep what is
// refused, and a drain applies it once ingest opens.
func TestHoldIngest_RefusesEveryIngestRouteUntilOpened(t *testing.T) {
	routes := map[string]func() *http.Request{
		"hook":   func() *http.Request { return hookRequest(hookEnvelope(t, eventID(1))) },
		"status": func() *http.Request { return statusRequest(statusEnvelope(t, eventID(2))) },
		"logs":   func() *http.Request { return logsRequest(logsExport(apiRequestRecord("s1", "m", 1))) },
	}
	for name, route := range routes {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, holdDeps)

			rec := h.do(route())
			assertJSONError(t, rec, http.StatusServiceUnavailable)
			assertJSONBody(t, rec, `{"error":"starting"}`)
			h.assertNothingApplied(t)

			h.srv.OpenIngest()
			assertStatus(t, h.do(route()), http.StatusOK)
			if events, requests := h.sink.recorded(); len(events)+len(requests) != 1 {
				t.Errorf("applied %d events and %d requests once ingest opened, want the one", len(events), len(requests))
			}
		})
	}
}

// Another loadline that loses the race for the port asks /healthz whether
// this one serves, and must hear yes while ingest is held.
func TestHoldIngest_ServesHealthAndTheUI(t *testing.T) {
	h := newHarness(t, holdDeps)

	assertStatus(t, h.do(request(http.MethodGet, "/healthz", nil)), http.StatusOK)
	assertStatus(t, h.do(cookieRequest("/api/snapshot")), http.StatusOK)
	assertStatus(t, h.do(request(http.MethodGet, "/", nil)), http.StatusOK)
}

func TestHoldIngest_StillNeedsTheToken(t *testing.T) {
	h := newHarness(t, holdDeps)
	r := hookRequest(hookEnvelope(t, eventID(1)))
	r.Header.Del(tokenHeader)

	assertJSONError(t, h.do(r), http.StatusUnauthorized)
}
