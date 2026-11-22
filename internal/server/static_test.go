package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/server"
)

func TestStatic_ServesTheUI(t *testing.T) {
	tests := []struct {
		target   string
		wantBody string
		wantType string
	}{
		{target: "/", wantBody: "<!doctype html><title>Loadline</title>", wantType: "text/html"},
		{target: "/app.js", wantBody: "console.log('loadline')"},
		{target: "/assets/app.css", wantBody: "body{margin:0}"},
		{target: "/docs/", wantBody: "<p>docs</p>", wantType: "text/html"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			h := newHarness(t)

			rec := h.do(request(http.MethodGet, tt.target, nil))

			assertStatus(t, rec, http.StatusOK)
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tt.wantType) {
				t.Errorf("Content-Type = %q, want %s", ct, tt.wantType)
			}
		})
	}
}

func TestStatic_RedirectsLikeAFileServer(t *testing.T) {
	tests := []struct{ target, location string }{
		{target: "/index.html", location: "./"},
		{target: "/docs", location: "docs/"},
		{target: "/app.js/", location: "../app.js"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			h := newHarness(t)

			rec := h.do(request(http.MethodGet, tt.target, nil))

			assertStatus(t, rec, http.StatusMovedPermanently)
			if got := rec.Header().Get("Location"); got != tt.location {
				t.Errorf("Location = %q, want %q", got, tt.location)
			}
		})
	}
}

func TestStatic_ListsNoDirectoriesAndKnowsNoOtherPaths(t *testing.T) {
	for _, target := range []string{
		"/assets/",
		"/assets",
		"/odd/",
		"/missing.js",
		"/docs/missing.html",
		"/api/unknown",
		"/ingest/hook",
		"/v1/logs",
	} {
		t.Run(target, func(t *testing.T) {
			h := newHarness(t)

			rec := h.do(request(http.MethodGet, target, nil))

			assertJSONError(t, rec, http.StatusNotFound)
			if strings.Contains(rec.Body.String(), "app.css") {
				t.Errorf("body %q lists a directory", rec.Body.String())
			}
		})
	}
}

func TestStatic_NoFilesServesNothing(t *testing.T) {
	h := newHarness(t, func(d *server.Deps) { d.Static = nil })

	assertJSONError(t, h.do(request(http.MethodGet, "/", nil)), http.StatusNotFound)
	assertStatus(t, h.do(request(http.MethodGet, "/healthz", nil)), http.StatusOK)
}
