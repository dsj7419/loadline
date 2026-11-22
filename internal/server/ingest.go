package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/otlp"
	"github.com/dsj7419/loadline/internal/protocol"
)

const (
	// maxBody caps every request body but an OpenTelemetry export. An
	// envelope is far smaller: its payload is capped at 64 KiB.
	maxBody = 1 << 20
	// maxLogsBody caps an OpenTelemetry export, which batches every event of
	// a few seconds.
	maxLogsBody = 4 << 20
)

func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	env, ok := s.readEnvelope(w, r, protocol.KindHook)
	if !ok {
		return
	}
	hook, refusal := hookEvent(env)
	if refusal != "" {
		s.reject(w, r, http.StatusBadRequest, refusal)
		return
	}
	s.accept(w, r, feedHook, env.ID, func() {
		s.state.Apply(hook)
		s.onDir(hook.Dir)
		s.kick()
	})
}

// ApplySpooledHook applies a hook event that a hook spooled because the
// server did not take it, held to the hook route's checks. Like the route,
// it applies an event once however often its ID comes back: the hook may
// have spooled an event the server took but answered too late. It notes the
// event's config dir but does not kick, so that a drain can kick once for
// its whole batch, and it counts nothing toward the hook feed, which tells
// whether hooks arrive now. An error means env can never be applied; it
// quotes nothing of env.
func (s *Server) ApplySpooledHook(env protocol.Envelope) error {
	if refusal := checkEnvelope(env, protocol.KindHook); refusal != "" {
		return errors.New(refusal)
	}
	hook, refusal := hookEvent(env)
	if refusal != "" {
		return errors.New(refusal)
	}
	if s.recent.add(env.ID) {
		applyOnce(s.recent, env.ID, func() {
			s.state.Apply(hook)
			s.onDir(hook.Dir)
		})
	}
	return nil
}

// hookEvent maps a hook envelope's payload to its event, or returns the
// reason it holds none.
func hookEvent(env protocol.Envelope) (hook domain.Hook, refusal string) {
	in, err := protocol.ReadHookInput(bytes.NewReader(env.Payload))
	if err != nil {
		return domain.Hook{}, "malformed payload"
	}
	hook, err = protocol.HookEvent(env, in)
	if err != nil {
		return domain.Hook{}, "payload has no session"
	}
	return hook, ""
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	env, ok := s.readEnvelope(w, r, protocol.KindStatus)
	if !ok {
		return
	}
	in, err := protocol.DecodeStatus(env.Payload)
	if err != nil {
		s.reject(w, r, http.StatusBadRequest, "malformed payload")
		return
	}
	status, err := protocol.StatusEvent(env, in)
	if err != nil {
		s.reject(w, r, http.StatusBadRequest, "payload has no session")
		return
	}
	s.accept(w, r, feedStatus, env.ID, func() {
		s.state.Apply(status)
		s.onDir(status.Dir)
	})
}

// accept notes the receipt of a valid event and applies it, unless an event
// with its ID was applied recently. A client that spooled an event the server
// had already taken sends it again, and applying it twice would count its
// subagent twice.
func (s *Server) accept(w http.ResponseWriter, r *http.Request, f feed, id string, apply func()) {
	s.feeds.note(f, s.clock())
	if s.recent.add(id) {
		applyOnce(s.recent, id, apply)
	}
	s.writeJSON(w, r, http.StatusOK, struct{}{})
}

// applyOnce runs apply for the event id names. Should apply panic, the event
// was not applied, so its ID is forgotten and a retry is applied.
func applyOnce(recent *recentIDs, id string, apply func()) {
	applied := false
	defer func() {
		if !applied {
			recent.forget(id)
		}
	}()
	apply()
	applied = true
}

// readEnvelope reads the request body as one envelope of kind. When the body
// is not one, it answers the request and reports false.
func (s *Server) readEnvelope(w http.ResponseWriter, r *http.Request, kind protocol.Kind) (protocol.Envelope, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			s.reject(w, r, http.StatusRequestEntityTooLarge, "body too large")
		} else {
			s.reject(w, r, http.StatusBadRequest, "unreadable body")
		}
		return protocol.Envelope{}, false
	}

	env, err := decodeEnvelope(body)
	if err != nil {
		s.reject(w, r, http.StatusBadRequest, "malformed envelope")
		return protocol.Envelope{}, false
	}
	if refusal := checkEnvelope(env, kind); refusal != "" {
		s.reject(w, r, http.StatusBadRequest, refusal)
		return protocol.Envelope{}, false
	}
	return env, true
}

// checkEnvelope returns the reason env cannot be taken as an envelope of
// kind, or "" when it can.
func checkEnvelope(env protocol.Envelope, kind protocol.Kind) (refusal string) {
	switch {
	case env.Validate() != nil:
		return "invalid envelope"
	case env.Kind != kind:
		return "envelope kind does not match the route"
	case isEmpty(env.Payload):
		return "envelope has no payload"
	default:
		return ""
	}
}

// decodeEnvelope decodes exactly one envelope. Only Loadline writes them, so
// an unknown field or anything after the envelope means the body is not one.
func decodeEnvelope(body []byte) (protocol.Envelope, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var env protocol.Envelope
	if err := dec.Decode(&env); err != nil {
		return protocol.Envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return protocol.Envelope{}, errors.New("decode envelope: data follows the envelope")
	}
	return env, nil
}

// isEmpty reports whether a payload is missing or JSON null, which the
// envelope's own checks let through.
func isEmpty(payload json.RawMessage) bool {
	p := bytes.TrimSpace(payload)
	return len(p) == 0 || bytes.Equal(p, []byte("null"))
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if !isJSON(r.Header.Get("Content-Type")) {
		s.reject(w, r, http.StatusUnsupportedMediaType, "unsupported content type")
		return
	}
	if !isIdentity(r.Header.Values("Content-Encoding")) {
		s.reject(w, r, http.StatusUnsupportedMediaType, "unsupported content encoding")
		return
	}
	requests, err := otlp.DecodeLogs(r.Body, maxLogsBody)
	switch {
	case errors.Is(err, otlp.ErrTooLarge):
		s.reject(w, r, http.StatusRequestEntityTooLarge, "body too large")
		return
	case err != nil:
		s.reject(w, r, http.StatusBadRequest, "malformed export")
		return
	}
	s.feeds.note(feedOTLP, s.clock())
	for _, request := range requests {
		s.state.RecordAPI(request)
	}
	// An empty object is the OTLP/HTTP JSON answer to a fully accepted export.
	s.writeJSON(w, r, http.StatusOK, struct{}{})
}

// isJSON reports whether contentType is application/json, with or without
// parameters. The exporter is configured for http/json; protobuf is not read.
func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}

// isIdentity reports whether a body sent with these Content-Encoding values
// is uncompressed. The exporter compresses only when told to, and the
// decoder reads plain JSON.
func isIdentity(encodings []string) bool {
	for _, encoding := range encodings {
		if encoding != "" && !strings.EqualFold(encoding, "identity") {
			return false
		}
	}
	return true
}
