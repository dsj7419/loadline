package web_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
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
