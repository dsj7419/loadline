package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/server"
	"github.com/dsj7419/loadline/internal/tracker"
)

// unstartedServer returns an HTTP server on a free loopback port, not yet
// serving, and its port.
func unstartedServer(t *testing.T) (*httptest.Server, int) {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	t.Cleanup(ts.Close)
	_, portText, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse listener port: %v", err)
	}
	return ts, port
}

// newStreamServer serves a harness over a real connection, which an event
// stream needs. configure, when set, runs before serving starts.
func newStreamServer(t *testing.T, configure func(*server.Server, *http.Server),
	opts ...func(*server.Deps),
) (*harness, *httptest.Server) {
	t.Helper()
	ts, port := unstartedServer(t)
	h := newHarness(t, append(opts, func(d *server.Deps) { d.Port = port })...)
	if configure != nil {
		configure(h.srv, ts.Config)
	}
	ts.Config.Handler = h.handler
	ts.Start()
	return h, ts
}

type stream struct {
	resp   *http.Response
	lines  *bufio.Reader
	cancel context.CancelFunc
}

type sseEvent struct {
	name, data, comment string
}

func openStream(t *testing.T, ts *httptest.Server) *stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	if err != nil {
		cancel()
		t.Fatalf("new request: %v", err)
	}
	addCookie(req, uiCookie, uiCookieValue(testToken))
	resp, err := ts.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open stream: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	return &stream{resp: resp, lines: bufio.NewReader(resp.Body), cancel: cancel}
}

// next reads the stream up to the blank line that ends an event or comment.
func (s *stream) next(t *testing.T) (sseEvent, error) {
	t.Helper()
	var ev sseEvent
	for {
		line, err := s.lines.ReadString('\n')
		if err != nil {
			return ev, err
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "":
			return ev, nil
		case strings.HasPrefix(line, ": "):
			ev.comment = strings.TrimPrefix(line, ": ")
		case strings.HasPrefix(line, "event: "):
			ev.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if ev.data != "" {
				t.Errorf("event has a second data line %q", line)
			}
			ev.data = strings.TrimPrefix(line, "data: ")
		default:
			t.Errorf("unexpected stream line %q", line)
		}
	}
}

func (s *stream) nextView(t *testing.T) string {
	t.Helper()
	ev, err := s.next(t)
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if ev.name != "view" || ev.data == "" {
		t.Fatalf("event = %+v, want a view", ev)
	}
	return ev.data
}

// eventually waits for cond, failing the test after five seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// logRecorder is a log handler that keeps every record. The server logs no
// attributes through With, so the handler has none of its own to keep.
type logRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (l *logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (l *logRecorder) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, r.Clone())
	return nil
}

func (l *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logRecorder) WithGroup(string) slog.Handler      { return l }

// find returns the attributes, as text by key, of each record logged at level
// with message, or with any message when message is empty.
func (l *logRecorder) find(level slog.Level, message string) []map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var found []map[string]string
	for _, r := range l.records {
		if r.Level != level || (message != "" && r.Message != message) {
			continue
		}
		attrs := make(map[string]string)
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.String()
			return true
		})
		found = append(found, attrs)
	}
	return found
}

func withLog(logs *logRecorder) func(*server.Deps) {
	return func(d *server.Deps) { d.Log = slog.New(logs) }
}

func assertStreamEnded(t *testing.T, h *harness, s *stream) {
	t.Helper()
	if ev, err := s.next(t); !errors.Is(err, io.EOF) {
		t.Errorf("read %+v, %v after the stream should have ended, want EOF", ev, err)
	}
	eventually(t, "the stream to let go of its subscription", func() bool {
		return h.srv.Clients() == 0 && h.sink.unsubscribes() == 1
	})
}

func TestSSE_SendsSnapshotThenUpdates(t *testing.T) {
	h, ts := newStreamServer(t, nil)
	first, second := testView(t0), testView(t0.Add(time.Second))
	h.sink.setView(first)

	s := openStream(t, ts)

	for header, want := range map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-store",
		"X-Accel-Buffering": "no",
	} {
		if got := s.resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if got := s.nextView(t); got != marshalView(t, first) {
		t.Errorf("first event = %s, want the snapshot %s", got, marshalView(t, first))
	}
	if calls := h.sink.callOrder(); !slices.Equal(calls, []string{"subscribe", "snapshot"}) {
		t.Errorf("state calls = %v, want [subscribe snapshot]: a view published in between would be lost", calls)
	}
	if got := h.srv.Clients(); got != 1 {
		t.Errorf("Clients() = %d, want 1", got)
	}

	h.sink.publish(second)
	if got := s.nextView(t); got != marshalView(t, second) {
		t.Errorf("second event = %s, want the published view %s", got, marshalView(t, second))
	}
}

func TestSSE_DiagCountsClients(t *testing.T) {
	h, ts := newStreamServer(t, nil)
	clients := func() int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/diag", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set(tokenHeader, string(testToken))
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("get diag: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var diag struct {
			Clients int `json:"clients"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&diag); err != nil {
			t.Fatalf("decode diag: %v", err)
		}
		return diag.Clients
	}

	first, second := openStream(t, ts), openStream(t, ts)
	first.nextView(t)
	second.nextView(t)
	if got := clients(); got != 2 {
		t.Errorf("diag clients = %d with two streams open, want 2", got)
	}

	first.cancel()
	eventually(t, "the closed stream to be counted out", func() bool { return h.srv.Clients() == 1 })
	if got := clients(); got != 1 {
		t.Errorf("diag clients = %d with one stream open, want 1", got)
	}
}

func TestSSE_SendsHeartbeatComments(t *testing.T) {
	_, ts := newStreamServer(t, func(srv *server.Server, _ *http.Server) {
		server.SetPingInterval(srv, 10*time.Millisecond)
	})
	s := openStream(t, ts)
	s.nextView(t)

	for range 2 {
		ev, err := s.next(t)
		if err != nil {
			t.Fatalf("read heartbeat: %v", err)
		}
		if ev != (sseEvent{comment: "ping"}) {
			t.Errorf("event = %+v, want the comment ping", ev)
		}
	}
}

func TestSSE_EndsWhenTheClientDisconnects(t *testing.T) {
	h, ts := newStreamServer(t, nil)
	s := openStream(t, ts)
	s.nextView(t)

	s.cancel()

	eventually(t, "the stream to let go of its subscription", func() bool {
		return h.srv.Clients() == 0 && h.sink.unsubscribes() == 1
	})
}

func TestSSE_EndsWhenTheServerShutsDown(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, ts := newStreamServer(t, func(_ *server.Server, hs *http.Server) {
		hs.BaseContext = func(net.Listener) context.Context { return base }
		hs.RegisterOnShutdown(cancel)
	})
	s := openStream(t, ts)
	s.nextView(t)

	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := ts.Config.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v; the stream held the server up", err)
	}

	assertStreamEnded(t, h, s)
}

// The read timeout that guards the server against slow requests bounds the
// whole request read. A stream reads nothing after its request, so the
// timeout must not end it.
func TestSSE_OutlivesTheServersReadTimeout(t *testing.T) {
	const readTimeout = 100 * time.Millisecond
	h, ts := newStreamServer(t, func(_ *server.Server, hs *http.Server) { hs.ReadTimeout = readTimeout })
	s := openStream(t, ts)
	s.nextView(t)

	time.Sleep(3 * readTimeout)
	h.sink.publish(tracker.View{GeneratedAt: t0.Add(time.Minute)})

	var got tracker.View
	if err := json.Unmarshal([]byte(s.nextView(t)), &got); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if !got.GeneratedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("view generated at %v, want the one published after the read timeout", got.GeneratedAt)
	}
	if h.srv.Clients() != 1 {
		t.Errorf("Clients = %d, want the stream still open", h.srv.Clients())
	}
}

func TestSSE_EndsWhenTheSubscriptionCloses(t *testing.T) {
	h, ts := newStreamServer(t, nil)
	s := openStream(t, ts)
	s.nextView(t)

	h.sink.closeSubscriptions()

	assertStreamEnded(t, h, s)
}

func TestSSE_UnencodableSnapshotIsAServerError(t *testing.T) {
	h := newHarness(t)
	h.sink.setView(unencodableView())

	assertJSONError(t, h.do(cookieRequest("/api/events")), http.StatusInternalServerError)
	if h.srv.Clients() != 0 || h.sink.unsubscribes() != 1 {
		t.Errorf("Clients() = %d and %d unsubscribes after a failed stream, want 0 and 1",
			h.srv.Clients(), h.sink.unsubscribes())
	}
}

func TestSSE_SkipsAnUnencodableView(t *testing.T) {
	h, ts := newStreamServer(t, nil)
	s := openStream(t, ts)
	s.nextView(t)

	h.sink.publish(unencodableView())
	next := testView(t0.Add(time.Second))
	h.sink.publish(next)

	if got := s.nextView(t); got != marshalView(t, next) {
		t.Errorf("event = %s, want the view after the unencodable one, %s", got, marshalView(t, next))
	}
}

// brokenStream is a response writer whose writes fail once writesLeft are
// spent, as on a connection the client dropped. It records the write
// deadlines set on it, and counts the writes no fresh deadline covered.
type brokenStream struct {
	*httptest.ResponseRecorder
	writesLeft  int
	deadlineErr error
	flushErr    error

	deadlines []time.Time
	armed     bool
	unguarded int
}

func (b *brokenStream) Write(p []byte) (int, error) {
	if !b.armed {
		b.unguarded++
	}
	b.armed = false
	if b.writesLeft == 0 {
		return 0, errors.New("broken pipe")
	}
	b.writesLeft--
	return b.ResponseRecorder.Write(p)
}

// WriteString hides the recorder's own, which io.WriteString would call.
func (b *brokenStream) WriteString(s string) (int, error) { return b.Write([]byte(s)) }

// FlushError is what http.ResponseController calls in preference to Flush.
func (b *brokenStream) FlushError() error {
	if b.flushErr != nil {
		return b.flushErr
	}
	b.Flush()
	return nil
}

func (b *brokenStream) SetWriteDeadline(deadline time.Time) error {
	b.deadlines = append(b.deadlines, deadline)
	b.armed = true
	return b.deadlineErr
}

// serveBrokenStream serves an event stream into w, runs during once the
// stream has subscribed, and waits for the handler to return.
func serveBrokenStream(t *testing.T, h *harness, w *brokenStream, during func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handler.ServeHTTP(w, cookieRequest("/api/events"))
	}()
	eventually(t, "the stream to subscribe", func() bool { return len(h.sink.callOrder()) > 0 })
	during()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream kept running after a write failed")
	}
	if h.srv.Clients() != 0 || h.sink.unsubscribes() != 1 {
		t.Errorf("Clients() = %d and %d unsubscribes after the stream ended, want 0 and 1",
			h.srv.Clients(), h.sink.unsubscribes())
	}
}

func TestSSE_EndsWhenAWriteFails(t *testing.T) {
	tests := []struct {
		name       string
		writesLeft int
		flushErr   error
		ping       time.Duration
		publish    bool
	}{
		{name: "the snapshot", writesLeft: 0, ping: time.Hour},
		{name: "an update", writesLeft: 1, ping: time.Hour, publish: true},
		{name: "a heartbeat", writesLeft: 1, ping: time.Millisecond},
		{name: "a flush", writesLeft: 10, flushErr: errors.New("write timeout"), ping: time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			server.SetPingInterval(h.srv, tt.ping)
			w := &brokenStream{ResponseRecorder: httptest.NewRecorder(), writesLeft: tt.writesLeft, flushErr: tt.flushErr}

			serveBrokenStream(t, h, w, func() {
				if tt.publish {
					h.sink.publish(testView(t0))
				}
			})
		})
	}
}

func TestSSE_SetsAWriteDeadlineBeforeEachWrite(t *testing.T) {
	h := newHarness(t)
	server.SetPingInterval(h.srv, time.Millisecond)
	w := &brokenStream{ResponseRecorder: httptest.NewRecorder(), writesLeft: 3}
	before := time.Now()

	serveBrokenStream(t, h, w, func() { h.sink.publish(testView(t0)) })

	after := time.Now()
	if w.unguarded != 0 {
		t.Errorf("%d writes had no fresh write deadline, want none", w.unguarded)
	}
	if len(w.deadlines) != 4 {
		t.Errorf("set %d write deadlines, want one for each of the 4 writes", len(w.deadlines))
	}
	for _, deadline := range w.deadlines {
		if deadline.Before(before.Add(30*time.Second)) || deadline.After(after.Add(30*time.Second)) {
			t.Errorf("write deadline %v is not 30 s after the write, which happened in [%v, %v]", deadline, before, after)
		}
	}
}

func TestSSE_EndsWhenAWriteDeadlineCannotBeSet(t *testing.T) {
	h := newHarness(t)
	server.SetPingInterval(h.srv, time.Hour)
	w := &brokenStream{ResponseRecorder: httptest.NewRecorder(), writesLeft: 10, deadlineErr: errors.New("connection closed")}

	serveBrokenStream(t, h, w, func() {})

	if w.Body.Len() != 0 {
		t.Errorf("streamed %q without a write deadline, want nothing", w.Body.String())
	}
}

func TestSSE_StreamsToAWriterWithoutDeadlines(t *testing.T) {
	h := newHarness(t)
	h.sink.setView(testView(t0))
	rec := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(t.Context())
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/events", nil)
	r.Host = testHost
	addCookie(r, uiCookie, uiCookieValue(testToken))
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handler.ServeHTTP(rec, r)
	}()
	eventually(t, "the stream to subscribe", func() bool { return len(h.sink.callOrder()) > 0 })

	cancel()
	<-done

	if want := "event: view\ndata: " + marshalView(t, testView(t0)) + "\n\n"; rec.Body.String() != want {
		t.Errorf("stream = %q, want the snapshot %q", rec.Body.String(), want)
	}
}

func TestSSE_EndsWhenTheClientStopsReading(t *testing.T) {
	h, ts := newStreamServer(t, func(srv *server.Server, _ *http.Server) {
		server.SetStreamWriteTimeout(srv, 200*time.Millisecond)
	})
	s := openStream(t, ts)
	s.nextView(t)

	// The client reads no further. Views far larger than the socket buffers
	// fill them, and then a write blocks.
	big := testView(t0)
	big.Sessions = []tracker.SessionView{{ID: "s1", Name: strings.Repeat("x", 1<<20)}}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
				h.sink.publishLatest(big)
			}
		}
	}()
	defer func() {
		close(stop)
		<-stopped
	}()

	eventually(t, "the stalled stream to end", func() bool {
		return h.srv.Clients() == 0 && h.sink.unsubscribes() == 1
	})
}

func TestSSE_StreamsTheTrackersViews(t *testing.T) {
	ts, port := unstartedServer(t)
	tr := tracker.New(time.Now, tracker.Config{}, nil)
	t.Cleanup(tr.Close)
	srv := server.New(server.Deps{State: tr, Token: testToken, Port: port, Static: testFiles, Version: testVersion})
	ts.Config.Handler = srv.Handler()
	ts.Start()
	decode := func(data string) tracker.View {
		t.Helper()
		var v tracker.View
		if err := json.Unmarshal([]byte(data), &v); err != nil {
			t.Fatalf("decode view: %v", err)
		}
		return v
	}

	s := openStream(t, ts)
	if v := decode(s.nextView(t)); len(v.Sessions) != 0 {
		t.Fatalf("first view has %d sessions, want none", len(v.Sessions))
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/ingest/hook",
		bytes.NewReader(hookEnvelope(t, eventID(1))))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(tokenHeader, string(testToken))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("post hook: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post hook: status %d, want 200", resp.StatusCode)
	}

	v := decode(s.nextView(t))
	if len(v.Sessions) != 1 || v.Sessions[0].ID != "s1" {
		t.Errorf("view after the hook has sessions %+v, want s1", v.Sessions)
	}
}

func TestSSE_HeadIsRefused(t *testing.T) {
	h := newHarness(t)
	r := withDeadline(t, cookieRequest("/api/events"))
	r.Method = http.MethodHead

	rec := h.do(r)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Errorf("Allow = %q, want GET", got)
	}
	if calls := h.sink.callOrder(); len(calls) != 0 {
		t.Errorf("state calls = %v for a refused HEAD, want none", calls)
	}
}

func TestSSE_LogsWhyAStreamEnded(t *testing.T) {
	tests := []struct {
		name   string
		end    func(*harness, *stream)
		reason string
	}{
		{name: "the client went away", end: func(_ *harness, s *stream) { s.cancel() }, reason: "request ended"},
		{name: "the subscription closed", end: func(h *harness, _ *stream) { h.sink.closeSubscriptions() }, reason: "subscription closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &logRecorder{}
			h, ts := newStreamServer(t, nil, withLog(logs))
			s := openStream(t, ts)
			s.nextView(t)

			tt.end(h, s)

			eventually(t, "the stream to log its end", func() bool {
				return len(logs.find(slog.LevelDebug, "event stream closed")) > 0
			})
			closed := logs.find(slog.LevelDebug, "event stream closed")
			if len(closed) != 1 || !strings.Contains(closed[0]["reason"], tt.reason) {
				t.Errorf("closing records = %v, want one whose reason mentions %q", closed, tt.reason)
			}
			if warnings := logs.find(slog.LevelWarn, ""); len(warnings) != 0 {
				t.Errorf("warnings = %v for a stream over a real connection, want none", warnings)
			}
		})
	}
}

func TestSSE_LogsAFailedWrite(t *testing.T) {
	logs := &logRecorder{}
	h := newHarness(t, withLog(logs))
	server.SetPingInterval(h.srv, time.Hour)
	w := &brokenStream{ResponseRecorder: httptest.NewRecorder(), writesLeft: 0}

	serveBrokenStream(t, h, w, func() {})

	closed := logs.find(slog.LevelDebug, "event stream closed")
	if len(closed) != 1 || !strings.Contains(closed[0]["reason"], "broken pipe") {
		t.Errorf("closing records = %v, want one whose reason is the write error", closed)
	}
}

// A writer without deadlines streams, but a client that stops reading could
// then hold its stream forever, so the process says so, once, however many
// servers it runs: here three, one stream each, logging to two loggers.
func TestSSE_WarnsOncePerProcessWhenStreamWritesHaveNoDeadline(t *testing.T) {
	server.ResetNoDeadlineWarning()
	t.Cleanup(server.ResetNoDeadlineWarning)
	first, second := &logRecorder{}, &logRecorder{}

	for _, logs := range []*logRecorder{first, second, first} {
		h := newHarness(t, withLog(logs))
		// A request that has already ended still gets the snapshot, and
		// then the stream ends at once.
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, cookieRequest("/api/events").WithContext(ctx))
		if !strings.HasPrefix(rec.Body.String(), "event: view\n") {
			t.Fatalf("stream = %q, want the snapshot", rec.Body.String())
		}
	}

	warnings := len(first.find(slog.LevelWarn, "")) + len(second.find(slog.LevelWarn, ""))
	if warnings != 1 {
		t.Errorf("%d warnings from three servers streaming without write deadlines, want exactly one", warnings)
	}
}
