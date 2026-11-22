// Package protocol defines what loadline hook and loadline tap send to the
// server, and decodes the hook and status-line JSON Claude Code produces into
// domain events. It performs no I/O of its own.
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

const (
	// SchemaVersion is the envelope version this package reads and writes.
	SchemaVersion = 1

	// MaxPayloadBytes caps an envelope's payload. A hook or status payload
	// that survives the allowlist is far smaller.
	MaxPayloadBytes = 64 << 10

	eventIDBytes = 8
)

// Kind says what an envelope's payload holds.
type Kind string

const (
	// KindHook marks a HookInput payload.
	KindHook Kind = "hook"
	// KindStatus marks a StatusInput payload.
	KindStatus Kind = "status"
)

// Envelope wraps one payload on its way from loadline hook or loadline tap to
// the server.
type Envelope struct {
	V int `json:"v"`
	// ID names the event. The client picks it once, so a spooled event that
	// is sent again carries the ID the server may already have seen.
	ID   string    `json:"id"`
	Kind Kind      `json:"kind"`
	At   time.Time `json:"at"`
	// ConfigDir is the absolute path of the Claude Code configuration
	// directory the payload came from.
	ConfigDir string `json:"configDir"`
	// PID is the Claude Code process ID, zero when unknown.
	PID     int             `json:"pid,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

// NewEventID returns a fresh event ID: 16 lowercase hexadecimal characters.
func NewEventID() (string, error) {
	raw := make([]byte, eventIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate event id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// Validate reports why the server must not accept the envelope, or nil. It
// checks the envelope only: the payload is decoded, and found wanting, later.
func (e Envelope) Validate() error {
	if e.V != SchemaVersion {
		return fmt.Errorf("unsupported schema version %d, want %d", e.V, SchemaVersion)
	}
	if !validEventID(e.ID) {
		return fmt.Errorf("event id %q is not %d lowercase hex characters", e.ID, 2*eventIDBytes)
	}
	switch e.Kind {
	case KindHook, KindStatus:
	default:
		return fmt.Errorf("unknown envelope kind %q", e.Kind)
	}
	if e.At.IsZero() {
		return fmt.Errorf("envelope has no event time")
	}
	if !domain.ValidEventTime(e.At) {
		return fmt.Errorf("event time %s is not after 1970 and before year 10000 in UTC", e.At.Format(time.RFC3339Nano))
	}
	if !filepath.IsAbs(e.ConfigDir) {
		return fmt.Errorf("config dir %q is not an absolute path", e.ConfigDir)
	}
	if e.PID < 0 {
		return fmt.Errorf("pid %d is negative", e.PID)
	}
	if len(e.Payload) > MaxPayloadBytes {
		return fmt.Errorf("payload is %d bytes, over the %d byte limit", len(e.Payload), MaxPayloadBytes)
	}
	return nil
}

func validEventID(id string) bool {
	if len(id) != 2*eventIDBytes {
		return false
	}
	for i := range len(id) {
		if c := id[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
