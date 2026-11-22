package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// handleEvents streams the view as server-sent events: the whole view once
// on connect and again each time the state publishes one, with a comment in
// between often enough to keep the connection open. The stream ends when the
// request's context does or the subscription closes.
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
	defer s.log.Debug("event stream closed")

	rc := http.NewResponseController(w)
	// The stream outlives any write timeout the HTTP server keeps for other
	// responses.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		s.log.Warn("clear the event stream's write deadline", "err", err)
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	// Tells a proxy not to buffer the stream, should one ever sit in front.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if writeView(w, rc, first) != nil {
		return
	}

	ping := time.NewTicker(s.pingEvery)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case view, ok := <-views:
			if !ok {
				return
			}
			data, err := json.Marshal(view)
			if err != nil {
				// Skipped: the next view may encode.
				s.log.Error("encode view", "err", err)
				continue
			}
			if writeView(w, rc, data) != nil {
				return
			}
		case <-ping.C:
			if send(w, rc, ": ping\n\n") != nil {
				return
			}
		}
	}
}

// writeView writes an encoded view as one event. encoding/json escapes
// newlines inside strings, so the view fits on a single data line.
func writeView(w io.Writer, rc *http.ResponseController, data []byte) error {
	return send(w, rc, "event: view\ndata: "+string(data)+"\n\n")
}

// send writes text to the stream and flushes it to the client.
func send(w io.Writer, rc *http.ResponseController, text string) error {
	if _, err := io.WriteString(w, text); err != nil {
		return err
	}
	return rc.Flush()
}
