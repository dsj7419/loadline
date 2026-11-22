package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// staticHandler serves files the way http.FileServerFS does, except that it
// never lists a directory: a directory without an index.html, like a path
// that names nothing, is not found. Nil files serve nothing.
//
// It answers "not found" itself rather than through the file server, whose
// error replies drop the Cache-Control header that API paths must keep.
func (s *Server) staticHandler(files fs.FS) http.Handler {
	var fileServer http.Handler
	if files != nil {
		fileServer = http.FileServerFS(files)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fileServer == nil || !servable(files, r.URL.Path) {
			s.reject(w, r, http.StatusNotFound, "not found")
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// servable reports whether urlPath names a file in files, or a directory
// that holds an index.html file, as http.FileServerFS resolves it.
func servable(files fs.FS, urlPath string) bool {
	name := strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if name == "" {
		name = "."
	}
	info, err := fs.Stat(files, name)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return true
	}
	index, err := fs.Stat(files, path.Join(name, "index.html"))
	return err == nil && !index.IsDir()
}
