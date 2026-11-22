package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dsj7419/loadline/internal/protocol"
)

// FuzzDecodeEnvelope reads arbitrary bodies as the ingest routes do. An
// envelope a route takes is of that route's kind alone, carries a payload
// within the limit, and encodes again to one the route takes as the same
// event: Validate is what the spool checks before it encodes an envelope,
// so it must pass nothing that cannot be encoded.
func FuzzDecodeEnvelope(f *testing.F) {
	dir, err := json.Marshal(filepath.Join(os.TempDir(), ".claude"))
	if err != nil {
		f.Fatalf("marshal config dir: %v", err)
	}
	envelope := func(fields string) []byte {
		return fmt.Appendf(nil, `{"v":1,"id":"0123456789abcdef","configDir":%s,%s}`, dir, fields)
	}
	for _, seed := range [][]byte{
		envelope(`"kind":"hook","at":"2026-11-21T17:59:00Z","pid":4242,"payload":{"session_id":"s1","hook_event_name":"Stop"}`),
		envelope(`"kind":"status","at":"2026-11-21T19:59:00.5+02:00","payload":{"session_id":"s1","note":"<&>"}`),
		envelope(`"kind":"hook","at":"0000-01-01T00:30:00+01:00","payload":{"session_id":"s1"}`),
		envelope(`"kind":"hook","at":"9999-12-31T23:59:59-01:00","payload":{"session_id":"s1"}`),
		envelope(`"kind":"status","at":"2026-11-21T17:59:00Z","payload":null`),
		envelope(`"kind":"status","at":"2026-11-21T17:59:00Z","pid":-1,"payload":{}`),
		envelope(`"kind":"tap","at":"2026-11-21T17:59:00Z","payload":{}`),
		envelope(`"kind":"hook","at":"2026-11-21T17:59:00Z","payload":{},"extra":1`),
		append(envelope(`"kind":"hook","at":"2026-11-21T17:59:00Z","payload":{}`), `{}`...),
		[]byte(`{"v":1,"id":"0123456789ABCDEF","kind":"hook","at":"2026-11-21T17:59:00Z","configDir":"relative","payload":{}}`),
		[]byte(`null`),
		[]byte(`[]`),
		[]byte(`{}`),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		env, err := decodeEnvelope(body)
		if err != nil || checkEnvelope(env, env.Kind) != "" {
			return
		}

		other := protocol.KindStatus
		if env.Kind == protocol.KindStatus {
			other = protocol.KindHook
		}
		if checkEnvelope(env, other) == "" {
			t.Errorf("the %s route takes the %s envelope %s", other, env.Kind, body)
		}
		if isEmpty(env.Payload) || len(env.Payload) > protocol.MaxPayloadBytes {
			t.Errorf("taken envelope has a payload of %d bytes: %s", len(env.Payload), body)
		}

		encoded, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("a taken envelope does not encode: %v; body %s", err, body)
		}
		again, err := decodeEnvelope(encoded)
		if err != nil || checkEnvelope(again, env.Kind) != "" {
			t.Fatalf("the encoded envelope %s is not taken (%v); body %s", encoded, err, body)
		}
		if again.V != env.V || again.ID != env.ID || again.Kind != env.Kind || !again.At.Equal(env.At) ||
			again.ConfigDir != env.ConfigDir || again.PID != env.PID || !samePayload(t, again.Payload, env.Payload) {
			t.Errorf("the envelope encoded again reads as %+v, want %+v", again, env)
		}
	})
}

// samePayload reports whether two payloads hold the same JSON value, however
// each spells it.
func samePayload(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("payload %s does not decode: %v", a, err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("payload %s does not decode: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}
