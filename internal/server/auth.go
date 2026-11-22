package server

import (
	"crypto/subtle"
	"net/http"
	"slices"
)

const (
	tokenHeader  = "X-Loadline-Token" //nolint:gosec // G101: the name of the header, not a credential
	uiCookieName = "loadline_ui"
)

// checkHost refuses a request that names any host but the server's loopback
// address and port. A page on another site that rebinds its DNS name to
// 127.0.0.1 still sends its own name, so it cannot reach any route.
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(s.hosts, r.Host) {
			s.reject(w, r, http.StatusMisdirectedRequest, "unknown host")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkOrigin refuses a state-changing request that a browser sent from a
// page of another origin. A request without an Origin header passes: the
// command-line clients and the OpenTelemetry exporter send none.
func (s *Server) checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			for _, origin := range r.Header.Values("Origin") {
				if !slices.Contains(s.origins, origin) {
					s.reject(w, r, http.StatusForbidden, "foreign origin")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireToken admits only requests that carry the ingest token.
func (s *Server) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hasToken(r) {
			s.reject(w, r, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireUI admits requests that carry the UI cookie, or the ingest token
// for command-line clients.
func (s *Server) requireUI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hasUICookie(r) && !s.hasToken(r) {
			s.reject(w, r, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hasToken and hasUICookie refuse an empty credential first: two empty
// values compare equal, and the server's own is empty when it has no token.
func (s *Server) hasToken(r *http.Request) bool {
	got := r.Header.Get(tokenHeader)
	return got != "" && subtle.ConstantTimeCompare([]byte(got), s.token) == 1
}

func (s *Server) hasUICookie(r *http.Request) bool {
	cookie, err := r.Cookie(uiCookieName)
	return err == nil && cookie.Value != "" && subtle.ConstantTimeCompare([]byte(cookie.Value), s.uiCookie) == 1
}
