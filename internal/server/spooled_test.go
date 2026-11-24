package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/protocol"
)

// spooledEnvelope is a hook envelope as a hook leaves it in the spool.
func spooledEnvelope(t *testing.T, kind protocol.Kind, id, payload string) protocol.Envelope {
	t.Helper()
	var env protocol.Envelope
	if err := json.Unmarshal(envelope(t, kind, id, payload), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	return env
}

func TestApplySpooledHook_AppliesTheHookAndNotesItsDir(t *testing.T) {
	h := newHarness(t)

	if err := h.srv.ApplySpooledHook(spooledEnvelope(t, protocol.KindHook, eventID(1), hookPayload)); err != nil {
		t.Fatalf("ApplySpooledHook: %v", err)
	}

	events, _ := h.sink.recorded()
	want := domain.Hook{
		At:      eventTime,
		Dir:     domain.ConfigDir(configDir),
		Session: "s1",
		PID:     testPID,
		Name:    "SessionStart",
		Source:  "startup",
	}
	if len(events) != 1 || events[0] != want {
		t.Errorf("applied %+v, want [%+v]", events, want)
	}
	kicks, dirs := h.observed()
	if kicks != 0 {
		t.Errorf("kicked %d times, want 0: the drain kicks once for its whole batch", kicks)
	}
	if len(dirs) != 1 || dirs[0] != domain.ConfigDir(configDir) {
		t.Errorf("noted dirs %v, want [%s]", dirs, configDir)
	}
	// A spooled event is old news: it says nothing of whether hooks arrive.
	if at := h.srv.Feeds()["hook"]; !at.IsZero() {
		t.Errorf("hook feed noted at %v, want it untouched", at)
	}
}

// A hook that timed out waiting for the server's answer spools the event the
// server had already applied.
func TestApplySpooledHook_AnEventTheRouteTookIsAppliedOnce(t *testing.T) {
	t.Run("posted, then drained", func(t *testing.T) {
		h := newHarness(t)
		assertStatus(t, h.do(hookRequest(hookEnvelope(t, eventID(1)))), http.StatusOK)

		if err := h.srv.ApplySpooledHook(spooledEnvelope(t, protocol.KindHook, eventID(1), hookPayload)); err != nil {
			t.Fatalf("ApplySpooledHook: %v", err)
		}

		if events, _ := h.sink.recorded(); len(events) != 1 {
			t.Errorf("applied %d events, want 1", len(events))
		}
	})
	t.Run("drained, then posted", func(t *testing.T) {
		h := newHarness(t)
		if err := h.srv.ApplySpooledHook(spooledEnvelope(t, protocol.KindHook, eventID(1), hookPayload)); err != nil {
			t.Fatalf("ApplySpooledHook: %v", err)
		}

		assertStatus(t, h.do(hookRequest(hookEnvelope(t, eventID(1)))), http.StatusOK)

		if events, _ := h.sink.recorded(); len(events) != 1 {
			t.Errorf("applied %d events, want 1", len(events))
		}
	})
}

func TestApplySpooledHook_RefusesWhatTheHookRouteRefuses(t *testing.T) {
	valid := func() protocol.Envelope { return spooledEnvelope(t, protocol.KindHook, eventID(1), hookPayload) }
	tests := map[string]func() protocol.Envelope{
		"unsupported version": func() protocol.Envelope { e := valid(); e.V = 2; return e },
		"bad event id":        func() protocol.Envelope { e := valid(); e.ID = canary; return e },
		"relative config dir": func() protocol.Envelope { e := valid(); e.ConfigDir = ".claude"; return e },
		"status envelope":     func() protocol.Envelope { return spooledEnvelope(t, protocol.KindStatus, eventID(1), statusPayload) },
		"no payload":          func() protocol.Envelope { e := valid(); e.Payload = nil; return e },
		"null payload":        func() protocol.Envelope { e := valid(); e.Payload = json.RawMessage("null"); return e },
		"payload is a string": func() protocol.Envelope { e := valid(); e.Payload = json.RawMessage(`"` + canary + `"`); return e },
		"payload over the limit": func() protocol.Envelope {
			e := valid()
			e.Payload = json.RawMessage(`{"session_id":"s1","x":"` + strings.Repeat("a", protocol.MaxPayloadBytes) + `"}`)
			return e
		},
		"no session": func() protocol.Envelope {
			return spooledEnvelope(t, protocol.KindHook, eventID(1), `{"hook_event_name":"`+canary+`"}`)
		},
	}
	for name, env := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)

			err := h.srv.ApplySpooledHook(env())

			if err == nil {
				t.Fatal("ApplySpooledHook took an event the hook route refuses")
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("error %q quotes the event", err)
			}
			h.assertNothingApplied(t)
		})
	}
}
