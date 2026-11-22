package forward

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/protocol"
)

// A put's file appears under its event's name only once it is whole: a drain
// that runs while the put is still writing finds no event to deliver, and
// nothing to set aside.
func TestSpool_ADrainDuringAPutSeesNoEventYet(t *testing.T) {
	s := Spool{Dir: filepath.Join(t.TempDir(), "spool")}
	env := protocol.Envelope{
		V: protocol.SchemaVersion, ID: "00000000000000a1", Kind: protocol.KindHook,
		At: time.Date(2026, time.November, 21, 18, 0, 0, 0, time.UTC), ConfigDir: t.TempDir(),
		Payload: json.RawMessage(`{"session_id":"s1","hook_event_name":"Stop"}`),
	}
	var during []string
	testHookBeforePublish = func() {
		entries, err := os.ReadDir(s.Dir)
		if err != nil {
			t.Errorf("read the spool during the put: %v", err)
		}
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), tempPrefix) {
				t.Errorf("during the put the spool holds %s, want only the put's temporary file", e.Name())
			}
		}
		if _, err := s.Drain(func(env protocol.Envelope) error {
			during = append(during, env.ID)
			return nil
		}); err != nil {
			t.Errorf("drain during the put: %v", err)
		}
	}
	t.Cleanup(func() { testHookBeforePublish = nil })

	if err := s.Put(env); err != nil {
		t.Fatalf("Put: %v", err)
	}
	testHookBeforePublish = nil

	if len(during) != 0 {
		t.Errorf("a drain during the put delivered %v, want nothing yet", during)
	}
	var after []string
	if _, err := s.Drain(func(env protocol.Envelope) error {
		after = append(after, env.ID)
		return nil
	}); err != nil {
		t.Fatalf("drain after the put: %v", err)
	}
	if len(after) != 1 || after[0] != env.ID {
		t.Errorf("the drain after the put delivered %v, want [%s]", after, env.ID)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, badDirName)); !os.IsNotExist(err) {
		t.Errorf("something was set aside in %s: %v", badDirName, err)
	}
}
