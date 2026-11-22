package forward_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/protocol"
)

// FuzzSpool_DrainTakesOrSetsAsideAnyFile drains a spool file that holds
// whatever bytes are on the disk. What it holds never fails the drain: an
// envelope that passes its checks goes to the drain function once, anything
// else is moved to the bad directory, and the spool is left empty.
func FuzzSpool_DrainTakesOrSetsAsideAnyFile(f *testing.F) {
	valid, err := json.Marshal(envelopeAt(1, t0))
	if err != nil {
		f.Fatalf("marshal: %v", err)
	}
	newer := envelopeAt(2, t0)
	newer.V = protocol.SchemaVersion + 1
	future, err := json.Marshal(newer)
	if err != nil {
		f.Fatalf("marshal: %v", err)
	}
	for _, seed := range [][]byte{
		valid, future,
		[]byte(`{"v":1,"id":`), []byte(`[]`), []byte(`null`), []byte(``),
		[]byte(`{"v":1,"id":"0000000000000001","kind":"hook","at":"2026-11-21T18:00:00Z","configDir":"relative","payload":{}}`),
		[]byte(`{"v":1,"id":"0000000000000001","kind":"hook","at":"0000-01-01T00:30:00+01:00","configDir":"/x","payload":{}}`),
	} {
		f.Add(seed)
	}
	spool := forward.Spool{Dir: filepath.Join(f.TempDir(), "spool")}
	bad := filepath.Join(spool.Dir, "bad")
	const name = "1795284000000000000-0000000a.json"

	f.Fuzz(func(t *testing.T, data []byte) {
		if err := os.RemoveAll(spool.Dir); err != nil {
			t.Fatalf("clear the spool: %v", err)
		}
		if err := os.MkdirAll(spool.Dir, 0o700); err != nil {
			t.Fatalf("create the spool: %v", err)
		}
		if err := os.WriteFile(filepath.Join(spool.Dir, name), data, 0o600); err != nil {
			t.Fatalf("write the spool file: %v", err)
		}

		var taken []protocol.Envelope
		drained, err := spool.Drain(func(env protocol.Envelope) error {
			taken = append(taken, env)
			return nil
		})
		if err != nil {
			t.Fatalf("Drain = %v, want what the file holds taken or set aside", err)
		}
		if drained != len(taken) || len(taken) > 1 {
			t.Fatalf("Drain took %d events and reported %d, want at most the one file's", len(taken), drained)
		}
		for _, env := range taken {
			if err := env.Validate(); err != nil {
				t.Errorf("Drain handed over an envelope that fails its checks: %v", err)
			}
		}
		if left := names(t, spool.Dir); len(left) != 0 {
			t.Errorf("the spool still holds %v", left)
		}
		if setAside := names(t, bad); len(setAside)+len(taken) != 1 {
			t.Errorf("bad/ holds %v after %d events were taken, want the file in one place or the other", setAside, len(taken))
		}
	})
}
