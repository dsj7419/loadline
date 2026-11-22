package server_test

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

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

// The page sends X-Content-Type-Options: nosniff, so a browser runs a script
// or applies a stylesheet only under its exact type. The types are the
// server's own rather than the operating system's, which on Windows come
// from the registry and can be anything.
func TestStatic_TypesEachEmbeddedFileItself(t *testing.T) {
	files := fstest.MapFS{
		"index.html":        {Data: []byte("<!doctype html>")},
		"app.js":            {Data: []byte("start()")},
		"app.css":           {Data: []byte("body{}")},
		"fonts/inter.woff2": {Data: []byte("wOF2")},
		"fonts/LICENSE.txt": {Data: []byte("OFL")},
		"img/mark.png":      {Data: []byte{0x89, 'P', 'N', 'G'}},
		"img/mark.svg":      {Data: []byte("<svg/>")},
		"docs/index.html":   {Data: []byte("<p>docs</p>")},
		"upper/APP.JS":      {Data: []byte("start()")},
		"other/data.bin":    {Data: []byte{0, 1}},
	}
	tests := []struct{ target, want string }{
		{target: "/", want: "text/html; charset=utf-8"},
		{target: "/docs/", want: "text/html; charset=utf-8"},
		{target: "/app.js", want: "text/javascript; charset=utf-8"},
		{target: "/upper/APP.JS", want: "text/javascript; charset=utf-8"},
		{target: "/app.css", want: "text/css; charset=utf-8"},
		{target: "/fonts/inter.woff2", want: "font/woff2"},
		{target: "/fonts/LICENSE.txt", want: "text/plain; charset=utf-8"},
		{target: "/img/mark.png", want: "image/png"},
		{target: "/img/mark.svg", want: "image/svg+xml"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			h := newHarness(t, func(d *server.Deps) { d.Static = files })

			rec := h.do(request(http.MethodGet, tt.target, nil))

			assertStatus(t, rec, http.StatusOK)
			if got := rec.Header().Get("Content-Type"); got != tt.want {
				t.Errorf("Content-Type = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatic_TypesNoRedirectOrMissingFile(t *testing.T) {
	files := fstest.MapFS{"app.js": {Data: []byte("start()")}, "index.html": {Data: []byte("<!doctype html>")}}
	for _, target := range []string{"/app.js/", "/index.html"} {
		t.Run(target, func(t *testing.T) {
			h := newHarness(t, func(d *server.Deps) { d.Static = files })

			rec := h.do(request(http.MethodGet, target, nil))

			assertStatus(t, rec, http.StatusMovedPermanently)
			if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "text/javascript") {
				t.Errorf("a redirect carries the script type %q", got)
			}
		})
	}
}
