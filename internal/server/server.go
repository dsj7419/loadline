// Package server is Loadline's HTTP API, meant for the loopback interface
// only: it refuses any request that names another host. It is the boundary
// between the machine's other processes and the live state. Hooks, status
// lines and the OpenTelemetry exporter post to its ingest routes with the
// ingest token, and the dashboard reads the state with a cookie that a
// one-time login code grants.
package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/tracker"
)

const (
	// defaultPingInterval spaces the comments that keep an idle event stream
	// open.
	defaultPingInterval = 15 * time.Second
	// defaultStreamWriteTimeout bounds each write to an event stream, so a
	// client that stops reading cannot hold the stream, or a graceful
	// shutdown, for longer.
	defaultStreamWriteTimeout = 30 * time.Second
)

// StateSink is the live state the server feeds and serves. *tracker.Tracker
// is one.
type StateSink interface {
	Apply(domain.Event)
	RecordAPI(domain.APIRequest)
	Snapshot() tracker.View
	// Subscribe delivers the views published after the call; the function it
	// returns ends the subscription.
	Subscribe() (<-chan tracker.View, func())
}

// Deps is what a Server needs. State is required.
type Deps struct {
	// State receives every accepted event synchronously, on the request's
	// goroutine.
	State StateSink
	// Kick is called after each hook is applied, and OnDir with the config
	// dir of each hook and status line. Both run on the request's goroutine
	// and must not block. Nil means nothing is called.
	Kick  func()
	OnDir func(domain.ConfigDir)
	// Stop is called for each accepted request to stop the server, on the
	// request's goroutine, and must not block. Nil leaves the route out: a
	// stop request is then refused as by a server from before it.
	Stop func()
	// Token is the ingest token. The UI cookie is derived from it. An empty
	// token admits nobody to any authenticated route.
	Token []byte
	// Port is the loopback port the server listens on. Requests must name it
	// in their Host header.
	Port int
	// Static holds the UI's files. Nil serves none.
	Static  fs.FS
	Version string
	// Clock reads the time. Nil means time.Now.
	Clock func() time.Time
	// Log receives the server's diagnostics, which never include tokens,
	// cookies, login codes or request bodies. Nil discards them.
	Log *slog.Logger
	// Demo serves a made-up state that no event may change. The ingest
	// routes refuse every event, token and all, with 503 "demo mode": a
	// hook then keeps its event for the real server, as it does when none
	// runs. The UI and its API work as ever.
	Demo bool
	// HoldIngest has the ingest routes refuse every event, with 503
	// "starting", until OpenIngest is called: an event must not reach a
	// state that is still being put in place. A hook keeps what is refused
	// for a later drain. The UI, its API and /healthz work as ever.
	HoldIngest bool
}

// Server serves the ingest routes, the UI API and the UI. It is safe for
// concurrent use.
type Server struct {
	state   StateSink
	kick    func()
	onDir   func(domain.ConfigDir)
	stop    func()
	token   []byte
	version string
	clock   func() time.Time
	log     *slog.Logger
	demo    bool
	// ingestOpen is false while HoldIngest holds the ingest routes.
	ingestOpen atomic.Bool

	// hosts are the Host headers any request may carry, and origins the
	// Origin headers a state-changing request may carry.
	hosts, origins     []string
	uiCookie           []byte
	pingEvery          time.Duration
	streamWriteTimeout time.Duration

	limiter *tokenBucket
	recent  *recentIDs
	codes   loginCodes
	feeds   feedLog
	clients atomic.Int32

	handler http.Handler
}

// New returns a server for d.
func New(d Deps) *Server {
	s := &Server{
		state:   d.State,
		kick:    d.Kick,
		onDir:   d.OnDir,
		stop:    d.Stop,
		token:   d.Token,
		version: d.Version,
		demo:    d.Demo,
		clock:   d.Clock,
		log:     d.Log,
		hosts: []string{
			fmt.Sprintf("127.0.0.1:%d", d.Port),
			fmt.Sprintf("localhost:%d", d.Port),
		},
		origins: []string{
			fmt.Sprintf("http://127.0.0.1:%d", d.Port),
			fmt.Sprintf("http://localhost:%d", d.Port),
		},
		pingEvery:          defaultPingInterval,
		streamWriteTimeout: defaultStreamWriteTimeout,
	}
	// Anyone can compute the cookie of an empty token, so there is none.
	if len(d.Token) > 0 {
		s.uiCookie = uiCookieValue(d.Token)
	}
	s.ingestOpen.Store(!d.HoldIngest)
	if s.kick == nil {
		s.kick = func() {}
	}
	if s.onDir == nil {
		s.onDir = func(domain.ConfigDir) {}
	}
	if s.clock == nil {
		s.clock = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.limiter = newTokenBucket(s.clock)
	s.recent = newRecentIDs(s.clock)
	s.handler = s.routes(s.staticHandler(d.Static))
	return s
}

// Handler returns the server's HTTP handler. An event stream it serves ends
// when the request's context does, so a server shutting down should cancel
// the base context of its requests (http.Server.BaseContext together with
// RegisterOnShutdown) rather than wait for the stream to go idle. Each write
// to a stream sets its own deadline, which overrides any write timeout the
// http.Server keeps.
func (s *Server) Handler() http.Handler { return s.handler }

// Clients returns the number of event streams currently open.
func (s *Server) Clients() int { return int(s.clients.Load()) }

// OpenIngest lets the ingest routes of a server made with HoldIngest take
// events, once the state they apply to is in place.
func (s *Server) OpenIngest() { s.ingestOpen.Store(true) }

func (s *Server) routes(static http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("POST /ingest/hook", s.ingest(s.handleHook))
	mux.Handle("POST /ingest/status", s.ingest(s.handleStatus))
	mux.Handle("POST /v1/logs", s.ingest(s.handleLogs))
	mux.Handle("GET /api/snapshot", s.requireUI(http.HandlerFunc(s.handleSnapshot)))
	mux.Handle("GET /api/diag", s.requireToken(http.HandlerFunc(s.handleDiag)))
	mux.Handle("POST /api/login-code", s.requireToken(http.HandlerFunc(s.handleLoginCode)))
	// The one method each of these routes takes, which a 405 for any other
	// lists alone in Allow.
	onlyMethod := map[string]string{}
	if s.stop != nil {
		// The token alone, never the cookie: a browser sends the cookie to
		// every port on 127.0.0.1, while a page of another origin cannot
		// send the token's header without a preflight the server never
		// grants.
		mux.Handle("POST /api/stop", s.requireToken(http.HandlerFunc(s.handleStop)))
		// Otherwise the UI's files would answer a GET or HEAD of the path,
		// with "not found".
		mux.Handle("GET /api/stop", s.refuseAllBut(http.MethodPost))
		onlyMethod["/api/stop"] = http.MethodPost
	}
	// These routes take GET alone. The mux routes HEAD to a GET route too,
	// but a HEAD from a link checker or a preview must not spend a login
	// code, and a HEAD must not hold an event stream whose output is thrown
	// away.
	getOnly := map[string]http.Handler{
		"/api/events": s.requireUI(http.HandlerFunc(s.handleEvents)),
		"/login":      http.HandlerFunc(s.handleLogin),
	}
	for path, h := range getOnly {
		mux.Handle("GET "+path, s.refuseHead(h))
		onlyMethod[path] = http.MethodGet
	}
	mux.Handle("GET /", static)
	return secureHeaders(s.checkHost(s.checkOrigin(s.refuseUnroutedMethods(mux, onlyMethod))))
}

// refuseAllBut answers every request with the 405 that refuseUnroutedMethods
// gives a method the route does not take, for a route that takes method
// alone.
func (s *Server) refuseAllBut(method string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", method)
		s.reject(w, r, http.StatusMethodNotAllowed, "method not allowed")
	})
}

// refuseHead answers HEAD with the 405 that refuseUnroutedMethods gives any
// other method the route does not take.
func (s *Server) refuseHead(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Allow", http.MethodGet)
			s.reject(w, r, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// refuseUnroutedMethods answers a request that no route takes with the same
// short JSON error as every other refusal; the mux would answer in plain
// text. Since GET / takes every path, such a request has the wrong method,
// or a path the mux redirects to its canonical form, which it is left to do.
// The methods to list in Allow come from the handler the mux reserves for
// that case, run against a writer that keeps only headers; a route that takes
// one method alone (onlyMethod, by path) lists that method alone.
func (s *Server) refuseUnroutedMethods(mux *http.ServeMux, onlyMethod map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		probe := headerOnly{header: make(http.Header)}
		h.ServeHTTP(probe, r)
		allow := probe.header.Get("Allow")
		if allow == "" {
			mux.ServeHTTP(w, r)
			return
		}
		if method, ok := onlyMethod[r.URL.Path]; ok {
			allow = method
		}
		w.Header().Set("Allow", allow)
		s.reject(w, r, http.StatusMethodNotAllowed, "method not allowed")
	})
}

// headerOnly is a response writer that keeps the headers and drops the rest.
type headerOnly struct{ header http.Header }

func (h headerOnly) Header() http.Header         { return h.header }
func (h headerOnly) Write(b []byte) (int, error) { return len(b), nil }
func (h headerOnly) WriteHeader(int)             {}

// ingest guards an ingest route: the token first, so that requests without
// it cannot spend the rate limit that hooks rely on, and then demo mode,
// which takes no event, and a hold on ingest.
func (s *Server) ingest(h http.HandlerFunc) http.Handler {
	return s.requireToken(s.refuseInDemo(s.refuseWhileHeld(s.rateLimit(h))))
}

// refuseWhileHeld answers every request with 503 until ingest opens.
func (s *Server) refuseWhileHeld(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.ingestOpen.Load() {
			s.reject(w, r, http.StatusServiceUnavailable, "starting")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// refuseInDemo answers every request with 503 in demo mode.
func (s *Server) refuseInDemo(next http.Handler) http.Handler {
	if !s.demo {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.reject(w, r, http.StatusServiceUnavailable, "demo mode")
	})
}

// contentSecurityPolicy confines the UI to its own files: no inline script or
// style, no frames, and nothing loaded from anywhere else.
const contentSecurityPolicy = "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; " +
	"script-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// secureHeaders sets the headers every response carries. The content security
// policy goes on every response, not only on the pages, so that whatever a
// browser renders as a document is confined. Responses that hold live state
// or credentials are never stored.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		if uncacheable(r.URL.Path) {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func uncacheable(path string) bool {
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/ingest/") ||
		path == "/login" || path == "/v1/logs"
}

// uiCookieValue is the hex HMAC-SHA256 of "ui" under the token: the UI cookie
// proves its holder redeemed a login code, and reveals nothing of the token.
func uiCookieValue(token []byte) []byte {
	mac := hmac.New(sha256.New, token)
	mac.Write([]byte("ui"))
	return []byte(hex.EncodeToString(mac.Sum(nil)))
}

type errorBody struct {
	Error string `json:"error"`
}

// reject answers with status and a fixed reason. The reason never quotes the
// request, and neither does the log line.
func (s *Server) reject(w http.ResponseWriter, r *http.Request, status int, reason string) {
	s.log.Debug("request rejected", "method", r.Method, "path", r.URL.Path, "status", status, "reason", reason)
	s.writeJSON(w, r, status, errorBody{Error: reason})
}

// writeJSON answers with status and v as JSON, or with a server error when v
// does not encode.
func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		s.log.Error("encode response", "path", r.URL.Path, "err", err)
		status, body = http.StatusInternalServerError, []byte(`{"error":"internal error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body) // a client that hung up has nothing to be told
}
