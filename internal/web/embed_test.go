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
		`<script>start()</script>`,
		`<SCRIPT type="module">start()</SCRIPT>`,
	} {
		if found := inlineCode(html); len(found) == 0 {
			t.Errorf("inlineCode(%q) found nothing", html)
		}
	}
	if found := inlineCode(`<link rel="stylesheet" href="/app.css"><script type="module" src="/app.js"></script>`); len(found) > 0 {
		t.Errorf("inlineCode flagged external assets: %q", found)
	}
}

var scriptTag = regexp.MustCompile(`<script\b[^>]*>`)

// inlineCode returns the style elements, style attributes and scripts
// without a src in html.
func inlineCode(html string) []string {
	lower := strings.ToLower(html)
	var found []string
	for _, marker := range []string{"<style", " style="} {
		if strings.Contains(lower, marker) {
			found = append(found, marker)
		}
	}
	for _, tag := range scriptTag.FindAllString(lower, -1) {
		if !strings.Contains(tag, " src=") {
			found = append(found, tag)
		}
	}
	return found
}
