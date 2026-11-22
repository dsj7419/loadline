package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/protocol"
)

var envelopeTime = time.Date(2026, time.November, 21, 12, 0, 0, 0, time.UTC)

func validEnvelope(t *testing.T) protocol.Envelope {
	t.Helper()

	return protocol.Envelope{
		V:         protocol.SchemaVersion,
		ID:        "0123456789abcdef",
		Kind:      protocol.KindHook,
		At:        envelopeTime,
		ConfigDir: t.TempDir(),
		PID:       4242,
		Payload:   json.RawMessage(`{"session_id":"s"}`),
	}
}

func TestEnvelopeValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*protocol.Envelope)
		wantErr string
	}{
		{name: "valid hook", mutate: func(*protocol.Envelope) {}},
		{name: "valid status", mutate: func(e *protocol.Envelope) { e.Kind = protocol.KindStatus }},
		{name: "pid unknown", mutate: func(e *protocol.Envelope) { e.PID = 0 }},
		{
			name:   "payload at the limit",
			mutate: func(e *protocol.Envelope) { e.Payload = make([]byte, protocol.MaxPayloadBytes) },
		},
		{name: "version zero", mutate: func(e *protocol.Envelope) { e.V = 0 }, wantErr: "schema version"},
		{name: "future version", mutate: func(e *protocol.Envelope) { e.V = protocol.SchemaVersion + 1 }, wantErr: "schema version"},
		{name: "no id", mutate: func(e *protocol.Envelope) { e.ID = "" }, wantErr: "event id"},
		{name: "short id", mutate: func(e *protocol.Envelope) { e.ID = "0123456789abcde" }, wantErr: "event id"},
		{name: "long id", mutate: func(e *protocol.Envelope) { e.ID = "0123456789abcdef0" }, wantErr: "event id"},
		{name: "uppercase id", mutate: func(e *protocol.Envelope) { e.ID = "0123456789ABCDEF" }, wantErr: "event id"},
		{name: "non-hex id", mutate: func(e *protocol.Envelope) { e.ID = "0123456789abcdeg" }, wantErr: "event id"},
		{name: "id with newline", mutate: func(e *protocol.Envelope) { e.ID = "0123456789abcde\n" }, wantErr: "event id"},
		{name: "no kind", mutate: func(e *protocol.Envelope) { e.Kind = "" }, wantErr: "kind"},
		{name: "unknown kind", mutate: func(e *protocol.Envelope) { e.Kind = "poll" }, wantErr: "kind"},
		{name: "zero time", mutate: func(e *protocol.Envelope) { e.At = time.Time{} }, wantErr: "time"},
		{name: "time at the epoch", mutate: func(e *protocol.Envelope) { e.At = time.Unix(0, 0) }, wantErr: "time"},
		{name: "first instant after the epoch", mutate: func(e *protocol.Envelope) { e.At = time.Unix(0, 1) }},
		{
			name:   "last instant of year 9999",
			mutate: func(e *protocol.Envelope) { e.At = time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC) },
		},
		{
			// Year 9999 where it was written, year 10000 in UTC: no view
			// holding it could be encoded.
			name: "time past year 9999 in UTC",
			mutate: func(e *protocol.Envelope) {
				e.At = time.Date(9999, time.December, 31, 23, 59, 59, 0, time.FixedZone("", -3600))
			},
			wantErr: "time",
		},
		{
			name:    "time before year 0 in UTC",
			mutate:  func(e *protocol.Envelope) { e.At = time.Date(0, time.January, 1, 0, 0, 0, 0, time.FixedZone("", 3600)) },
			wantErr: "time",
		},
		{name: "no config dir", mutate: func(e *protocol.Envelope) { e.ConfigDir = "" }, wantErr: "config dir"},
		{name: "relative config dir", mutate: func(e *protocol.Envelope) { e.ConfigDir = "claude/config" }, wantErr: "config dir"},
		{name: "negative pid", mutate: func(e *protocol.Envelope) { e.PID = -1 }, wantErr: "pid"},
		{
			name:    "payload over the limit",
			mutate:  func(e *protocol.Envelope) { e.Payload = make([]byte, protocol.MaxPayloadBytes+1) },
			wantErr: "payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnvelope(t)
			tt.mutate(&env)

			err := env.Validate()

			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("Validate() = nil, want an error mentioning %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("Validate() = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestMaxPayloadBytes_Is64KiB(t *testing.T) {
	if protocol.MaxPayloadBytes != 64*1024 {
		t.Errorf("MaxPayloadBytes = %d, want %d", protocol.MaxPayloadBytes, 64*1024)
	}
}

func TestEnvelope_WireFormat(t *testing.T) {
	env := validEnvelope(t)
	env.ConfigDir = "/cfg"

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	want := `{"v":1,"id":"0123456789abcdef","kind":"hook","at":"2026-11-21T12:00:00Z",` +
		`"configDir":"/cfg","pid":4242,"payload":{"session_id":"s"}}`
	if string(data) != want {
		t.Errorf("wire format\n got %s\nwant %s", data, want)
	}
}

func TestEnvelope_WireFormatOmitsUnknownPID(t *testing.T) {
	env := validEnvelope(t)
	env.PID = 0

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), `"pid"`) {
		t.Errorf("an unknown pid must be left out, got %s", data)
	}
}

func TestEnvelope_SurvivesTheWire(t *testing.T) {
	sent := validEnvelope(t)
	data, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var received protocol.Envelope
	if err := json.Unmarshal(data, &received); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if err := received.Validate(); err != nil {
		t.Errorf("received envelope is invalid: %v", err)
	}
	if received.ID != sent.ID || !received.At.Equal(sent.At) || received.ConfigDir != sent.ConfigDir ||
		received.PID != sent.PID || string(received.Payload) != string(sent.Payload) {
		t.Errorf("received %+v, want %+v", received, sent)
	}
}

func TestNewEventID(t *testing.T) {
	first, err := protocol.NewEventID()
	if err != nil {
		t.Fatalf("NewEventID: %v", err)
	}
	second, err := protocol.NewEventID()
	if err != nil {
		t.Fatalf("NewEventID: %v", err)
	}

	if first == second {
		t.Errorf("two event IDs are both %q", first)
	}
	for _, id := range []string{first, second} {
		env := validEnvelope(t)
		env.ID = id
		if err := env.Validate(); err != nil {
			t.Errorf("generated ID %q is rejected: %v", id, err)
		}
	}
}
