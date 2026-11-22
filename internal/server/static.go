package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// contentTypes are the types of the files the UI is built from. The server
// names them itself: mime.TypeByExtension would read the operating system's
// table, which on Windows is the registry, and with nosniff a browser runs a
// script or applies a stylesheet only under its exact type.
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".woff2": "font/woff2",
	".png":   "image/png",
	".svg":   "image/svg+xml",
	".txt":   "text/plain; charset=utf-8",
}

// staticHandler serves files the way http.FileServerFS does, except that it
// never lists a directory: a directory without an index.html, like a path
// that names nothing, is not found. Nil files serve nothing. Files of the
// UI's types go out under the types in contentTypes.
//
// It answers "not found" itself rather than through the file server, whose
// error replies drop the Cache-Control header that API paths must keep.
func (s *Server) staticHandler(files fs.FS) http.Handler {
	var fileServer http.Handler
	if files != nil {
		fileServer = http.FileServerFS(files)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fileServer == nil {
			s.reject(w, r, http.StatusNotFound, "not found")
			return
		}
		name, ok := served(files, r.URL.Path)
		if !ok {
			s.reject(w, r, http.StatusNotFound, "not found")
			return
		}
		if contentType, known := contentTypes[strings.ToLower(path.Ext(name))]; known {
			w = typedWriter{ResponseWriter: w, contentType: contentType}
		}
		fileServer.ServeHTTP(w, r)
	})
}

// served returns the file in files that urlPath serves, as http.FileServerFS
// resolves it: the file itself, or a directory's index.html. ok is false when
// the path serves no file.
func served(files fs.FS, urlPath string) (name string, ok bool) {
	name = strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if name == "" {
		name = "."
	}
	info, err := fs.Stat(files, name)
	if err != nil {
		return "", false
	}
	if !info.IsDir() {
		return name, true
	}
	index := path.Join(name, "index.html")
	indexInfo, err := fs.Stat(files, index)
	if err != nil || indexInfo.IsDir() {
		return "", false
	}
	return index, true
}

// typedWriter gives the file's content its type as the file server starts
// to send it. The file server's other replies, redirects and "not modified"
// among them, carry no content and keep their own headers.
type typedWriter struct {
	http.ResponseWriter
	contentType string
}

func (w typedWriter) WriteHeader(code int) {
	// A request for several ranges gets a multipart reply, whose type is its
	// own.
	multipart := strings.HasPrefix(w.Header().Get("Content-Type"), "multipart/")
	if code == http.StatusOK || (code == http.StatusPartialContent && !multipart) {
		w.Header().Set("Content-Type", w.contentType)
	}
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w typedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
