package server_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// FuzzIngest_NoPayloadBreaksTheSnapshot posts three events built from two
// arbitrary payloads, a hook and a status line in either order, through the
// real routes into a real tracker, dated anywhere in a year around the
// server's clock. No route may answer with a server error, and the snapshot
// must still be served and decode: nothing the ingest routes take may leave
// the dashboard unable to be drawn.
func FuzzIngest_NoPayloadBreaksTheSnapshot(f *testing.F) {
	for _, name := range []string{
		"status_ultra.json", "hook_sessionstart.json", "hook_posttooluse.json",
		"hook_subagentstart.json", "hook_notification.json",
	} {
		data, err := os.ReadFile(filepath.Join("..", "protocol", "testdata", name))
		if err != nil {
			f.Fatalf("read fixture: %v", err)
		}
		f.Add(true, data, int64(0), int32(4242), data)
		f.Add(false, data, int64(-90), int32(0), data)
	}
	f.Add(true,
		[]byte(`{"session_id":"s","rate_limits":{"seven_day":{"used_percentage":1e308,"resets_at":99999999999},`+
			`"five_hour":{"used_percentage":-1,"resets_at":1}},"cost":{"total_cost_usd":1e308,`+
			`"total_api_duration_ms":9223372036854775807},"context_window":{"context_window_size":9223372036854775807,`+
			`"total_input_tokens":-9223372036854775808}}`),
		int64(1), int32(1), []byte(`{"session_id":"s","hook_event_name":"SessionEnd"}`))
	dir, err := json.Marshal(configDir)
	if err != nil {
		f.Fatalf("marshal config dir: %v", err)
	}

	f.Fuzz(func(t *testing.T, statusFirst bool, first []byte, offsetSec int64, pid int32, second []byte) {
		if !json.Valid(first) || !json.Valid(second) {
			return
		}
		offsetSec %= 400 * 86400
		s, _ := trackedServer(t, fixedClock(t0))
		h := s.Handler()
		at := t0.Add(time.Duration(offsetSec) * time.Second).Format(time.RFC3339Nano)
		envelope := func(n int, kind string, payload []byte) string {
			return fmt.Sprintf(`{"v":1,"id":"%016x","kind":%q,"at":%q,"configDir":%s,"pid":%d,"payload":%s}`,
				n, kind, at, dir, max(pid, 0), payload)
		}
		kinds := [2]string{"hook", "status"}
		if statusFirst {
			kinds = [2]string{"status", "hook"}
		}
		for i, payload := range [][]byte{first, second, first} {
			kind := kinds[i%2]
			if rec := post(h, "/ingest/"+kind, envelope(i+1, kind, payload)); rec.Code >= 500 {
				t.Fatalf("POST /ingest/%s answered %d: %s", kind, rec.Code, rec.Body.String())
			}
		}

		rec := getSnapshot(h)
		if rec.Code != 200 {
			t.Fatalf("snapshot answered %d: %s", rec.Code, rec.Body.String())
		}
		var view map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("snapshot does not decode: %v", err)
		}
	})
}
