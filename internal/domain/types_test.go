package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/dsj7419/loadline/internal/domain"
)

func TestStateText(t *testing.T) {
	for state, want := range map[domain.State]string{
		domain.StateStarting:   "starting",
		domain.StateWorking:    "working",
		domain.StateNeedsYou:   "needs_you",
		domain.StateIdle:       "idle",
		domain.StateCompacting: "compacting",
		domain.StateError:      "error",
		domain.StateParked:     "parked",
		domain.StateEnded:      "ended",
	} {
		if got := state.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
		text, err := state.MarshalText()
		if err != nil || string(text) != want {
			t.Errorf("MarshalText() = %q, %v; want %q", text, err, want)
		}
		parsed := domain.State(255)
		if err := parsed.UnmarshalText([]byte(want)); err != nil || parsed != state {
			t.Errorf("UnmarshalText(%q) = %v, %v; want %v", want, parsed, err, state)
		}
	}
}

func TestStateText_RejectsUnknown(t *testing.T) {
	for _, text := range []string{"", "sleeping", "Working", "needs-you"} {
		state := domain.StateIdle
		if err := state.UnmarshalText([]byte(text)); err == nil {
			t.Errorf("UnmarshalText(%q) accepted it as %v", text, state)
		}
		if state != domain.StateIdle {
			t.Errorf("failed UnmarshalText(%q) changed the state to %v", text, state)
		}
	}

	// The first value past the defined states is as unknown as any later one.
	for unknown, want := range map[domain.State]string{
		domain.StateEnded + 1: "State(8)",
		99:                    "State(99)",
	} {
		if text, err := unknown.MarshalText(); err == nil {
			t.Errorf("MarshalText of %s = %q, want an error", want, text)
		}
		if got := unknown.String(); got != want {
			t.Errorf("String() = %q, want %s", got, want)
		}
	}
}

func TestKindText(t *testing.T) {
	for kind, want := range map[domain.Kind]string{
		domain.KindInteractive: "interactive",
		domain.KindBackground:  "background",
	} {
		if got := kind.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
		text, err := kind.MarshalText()
		if err != nil || string(text) != want {
			t.Errorf("MarshalText() = %q, %v; want %q", text, err, want)
		}
		parsed := domain.Kind(255)
		if err := parsed.UnmarshalText([]byte(want)); err != nil || parsed != kind {
			t.Errorf("UnmarshalText(%q) = %v, %v; want %v", want, parsed, err, kind)
		}
	}
}

func TestKindText_RejectsUnknown(t *testing.T) {
	for _, text := range []string{"", "batch", "Background"} {
		kind := domain.KindBackground
		if err := kind.UnmarshalText([]byte(text)); err == nil {
			t.Errorf("UnmarshalText(%q) accepted it as %v", text, kind)
		}
		if kind != domain.KindBackground {
			t.Errorf("failed UnmarshalText(%q) changed the kind to %v", text, kind)
		}
	}

	// The first value past the defined kinds is as unknown as any later one.
	for unknown, want := range map[domain.Kind]string{
		domain.KindBackground + 1: "Kind(2)",
		7:                         "Kind(7)",
	} {
		if text, err := unknown.MarshalText(); err == nil {
			t.Errorf("MarshalText of %s = %q, want an error", want, text)
		}
		if got := unknown.String(); got != want {
			t.Errorf("String() = %q, want %s", got, want)
		}
	}
}

func TestStateAndKind_RoundTripThroughJSON(t *testing.T) {
	type row struct {
		State domain.State `json:"state"`
		Kind  domain.Kind  `json:"kind"`
	}
	in := row{State: domain.StateNeedsYou, Kind: domain.KindBackground}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"state":"needs_you","kind":"background"}`; string(data) != want {
		t.Errorf("Marshal = %s, want %s", data, want)
	}

	var out row
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal(%s): %v", data, err)
	}
	if out != in {
		t.Errorf("round trip gave %+v, want %+v", out, in)
	}
}
