package domain_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

func TestSession_ShiftedMovesEveryTimeItHolds(t *testing.T) {
	s := applyAll(fresh(), poll(10, "busy"), status(12), sessionEnd(20, "other"))

	got := s.Shifted(-time.Hour)

	back := func(sec int) time.Time { return at(sec).Add(-time.Hour) }
	for name, pair := range map[string][2]time.Time{
		"StateAt":      {got.StateAt, back(20)},
		"observedAt":   {domain.ObservedAt(got), back(20)},
		"StartedAt":    {got.StartedAt, back(0)},
		"LastSeenAt":   {got.LastSeenAt, back(20)},
		"LastStatusAt": {got.LastStatusAt, back(12)},
		"EndedAt":      {got.EndedAt, back(20)},
	} {
		if !pair[0].Equal(pair[1]) {
			t.Errorf("%s = %s, want %s", name, sinceT0(pair[0]), sinceT0(pair[1]))
		}
	}
	if again := got.Shifted(time.Hour); !reflect.DeepEqual(again, s) {
		t.Errorf("shifted back and forth:\n%+v\nwant\n%+v", again, s)
	}
}

func TestSession_ShiftedLeavesUnsetTimesUnset(t *testing.T) {
	got := fresh().Shifted(-time.Hour)

	if !got.LastStatusAt.IsZero() || !got.EndedAt.IsZero() || !domain.ObservedAt(got).IsZero() {
		t.Errorf("unset times moved: LastStatusAt %s, EndedAt %s, observed %s",
			sinceT0(got.LastStatusAt), sinceT0(got.EndedAt), sinceT0(domain.ObservedAt(got)))
	}
}

// What a clock an hour fast stamped, moved back by the hour, is older than
// what the corrected clock stamps next.
func TestSession_ShiftedTakesTheEventsAfterAClockStep(t *testing.T) {
	s := applyAll(fresh(), hook(3600, "PermissionRequest"))

	s = domain.Apply(s.Shifted(-3595*time.Second), poll(10, "busy"))

	if got, want := outcomeOf(s), wantIn(domain.StateWorking, 10); !got.equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
