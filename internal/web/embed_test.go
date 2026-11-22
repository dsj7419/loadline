package web_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/web"
)

const csp = `<meta http-equiv="Content-Security-Policy" content="default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; font-src 'self'">`

func TestFS_ServesIndex(t *testing.T) {
	assets := web.FS()

	for _, name := range []string{
		"index.html",
		"app.js",
		"app.css",
		"fonts/inter-latin-400-normal.woff2",
		"fonts/inter-latin-500-normal.woff2",
		"fonts/inter-latin-600-normal.woff2",
		"fonts/inter-latin-700-normal.woff2",
		"fonts/jetbrains-mono-latin-500-normal.woff2",
		"fonts/inter-LICENSE.txt",
		"fonts/jetbrains-mono-LICENSE.txt",
	} {
		info, err := fs.Stat(assets, name)
		if err != nil {
			t.Errorf("stat %s: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", name)
		}
	}

	rec := httptest.NewRecorder()
	http.FileServerFS(assets).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, csp) {
		t.Errorf("GET / does not carry the CSP meta %s; got:\n%s", csp, body)
	}
	for _, ref := range []string{`<link rel="stylesheet" href="/app.css">`, `<script type="module" src="/app.js"></script>`} {
		if !strings.Contains(body, ref) {
			t.Errorf("GET / does not load %s; got:\n%s", ref, body)
		}
	}
}

// app.js bundles these MIT-licensed packages, whose license must go with
// every copy of them.
func TestFS_CarriesTheLicensesOfTheBundledPackages(t *testing.T) {
	for _, name := range []string{
		"licenses/preact-LICENSE.txt",
		"licenses/preact-signals-LICENSE.txt",
		"licenses/preact-signals-core-LICENSE.txt",
	} {
		text, err := fs.ReadFile(web.FS(), name)
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if !strings.Contains(string(text), "Permission is hereby granted") {
			t.Errorf("%s does not hold the MIT license; got:\n%s", name, text)
		}
	}
}

// The CSP blocks inline styles and scripts, so any in index.html would
// silently do nothing in the browser.
func TestFS_IndexHasNoInlineStyleOrScript(t *testing.T) {
	index, err := fs.ReadFile(web.FS(), "index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if found := inlineCode(string(index)); len(found) > 0 {
		t.Errorf("index.html has inline code the CSP blocks: %q", found)
	}
}

func TestInlineCode_FindsEachKind(t *testing.T) {
	for _, html := range []string{
		`<style>main { color: red }</style>`,
		`<main style="color: red">`,
		`<main style = "color: red">`,
		"<main\n\tSTYLE=\"color: red\">",
		`<script>start()</script>`,
		`<SCRIPT type="module">start()</SCRIPT>`,
		`<body onload="start()">`,
		`<button ONCLICK = 'start()'>`,
		"<img src=\"/a.png\"\nonerror=\"start()\">",
	} {
		if found := inlineCode(html); len(found) == 0 {
			t.Errorf("inlineCode(%q) found nothing", html)
		}
	}
	// A CSP meta carries content=, and data-* attributes may end in style or
	// start with on, none of which is inline code.
	for _, html := range []string{
		`<link rel="stylesheet" href="/app.css"><script type="module" src="/app.js"></script>`,
		csp,
		`<div data-style="compact" data-online="yes">`,
	} {
		if found := inlineCode(html); len(found) > 0 {
			t.Errorf("inlineCode(%q) flagged %q", html, found)
		}
	}
}

var (
	scriptTag = regexp.MustCompile(`<script\b[^>]*>`)
	// An attribute name follows whitespace, so data-style= and content= are
	// not matched.
	inlineAttribute = regexp.MustCompile(`\s(?:style|on\w+)\s*=`)
)

// inlineCode returns the style elements, style attributes, event handler
// attributes and scripts without a src in html.
func inlineCode(html string) []string {
	lower := strings.ToLower(html)
	var found []string
	if strings.Contains(lower, "<style") {
		found = append(found, "<style")
	}
	found = append(found, inlineAttribute.FindAllString(lower, -1)...)
	for _, tag := range scriptTag.FindAllString(lower, -1) {
		if !strings.Contains(tag, " src=") {
			found = append(found, tag)
		}
	}
	return found
}
