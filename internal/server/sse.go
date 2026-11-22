package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dsj7419/loadline/internal/tracker"
)

// errSubscriptionClosed ends a stream whose subscription the state closed.
var errSubscriptionClosed = errors.New("the subscription closed")

// handleEvents streams the view as server-sent events: the whole view once
// on connect and again each time the state publishes one, with a comment in
// between often enough to keep the connection open. The stream ends when the
// request's context does, the subscription closes, or a write fails or takes
// longer than its deadline.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	// Subscribing before taking the snapshot means no view published in
	// between is lost. Such a view then follows the snapshot, which already
	// holds what it shows.
	views, unsubscribe := s.state.Subscribe()
	defer unsubscribe()
	first, err := json.Marshal(s.state.Snapshot())
	if err != nil {
		s.log.Error("encode view", "err", err)
		s.reject(w, r, http.StatusInternalServerError, "internal error")
		return
	}

	s.clients.Add(1)
	defer s.clients.Add(-1)
	s.log.Debug("event stream opened", "clients", s.Clients())

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	// Tells a proxy not to buffer the stream, should one ever sit in front.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	stream := eventStream{
		w:          w,
		rc:         http.NewResponseController(w),
		timeout:    s.streamWriteTimeout,
		noDeadline: s.warnNoWriteDeadline,
	}
	ended := s.stream(r.Context(), stream, first, views)
	s.log.Debug("event stream closed", "reason", ended)
}

// stream sends first and then each view the state publishes, with heartbeat
// comments in between, until something ends the stream. It returns what did.
func (s *Server) stream(ctx context.Context, stream eventStream, first []byte, views <-chan tracker.View) error {
	if err := stream.sendView(first); err != nil {
		return err
	}

	ping := time.NewTicker(s.pingEvery)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("the request ended: %w", context.Cause(ctx))
		case view, ok := <-views:
			if !ok {
				return errSubscriptionClosed
			}
			data, err := json.Marshal(view)
			if err != nil {
				// Skipped: the next view may encode.
				s.log.Error("encode view", "err", err)
				continue
			}
			if err := stream.sendView(data); err != nil {
				return err
			}
		case <-ping.C:
			if err := stream.send(": ping\n\n"); err != nil {
				return err
			}
		}
	}
}

// warnNoWriteDeadline says, once, that stream writes go without a deadline,
// which leaves a client that stops reading holding its stream for as long as
// the connection lasts.
func (s *Server) warnNoWriteDeadline() {
	s.noDeadlineWarning.Do(func() {
		s.log.Warn("event stream writes have no deadline; a client that stops reading can hold its stream open")
	})
}

// eventStream writes to one client's event stream.
type eventStream struct {
	w       io.Writer
	rc      *http.ResponseController
	timeout time.Duration
	// noDeadline is called for each write the writer cannot set a deadline
	// on.
	noDeadline func()
}

// sendView writes an encoded view as one event. encoding/json escapes
// newlines inside strings, so the view fits on a single data line.
func (es eventStream) sendView(data []byte) error {
	return es.send("event: view\ndata: " + string(data) + "\n\n")
}

// send writes text and flushes it to the client, within a deadline set
// afresh for each write: a client that stops reading fills the socket's
// buffers and then blocks the write, which no context can interrupt. A
// writer without deadlines, which no connection has, is written without one.
func (es eventStream) send(text string) error {
	// A socket deadline is wall-clock time, whatever clock the state reads.
	switch err := es.rc.SetWriteDeadline(time.Now().Add(es.timeout)); {
	case errors.Is(err, http.ErrNotSupported):
		es.noDeadline()
	case err != nil:
		return fmt.Errorf("set the stream's write deadline: %w", err)
	}
	if _, err := io.WriteString(es.w, text); err != nil {
		return fmt.Errorf("write to the stream: %w", err)
	}
	if err := es.rc.Flush(); err != nil {
		return fmt.Errorf("flush the stream: %w", err)
	}
	return nil
}
