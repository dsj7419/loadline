package tracker_test

import (
	"encoding/json"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/tracker"
)

// Usage too large to add up stops at the largest a count or a cost can be:
// a count that wrapped would show as negative, and a cost that overflowed
// to infinity could not be encoded, so the view could not be served at all.
func TestTracker_UsageSumsSaturate(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(hook(0, s1, "SessionStart"))
	huge := domain.APIRequest{
		At: at(1), Session: s1, Model: "Opus 5.5",
		Input: math.MaxInt64 - 1, Output: math.MaxInt64 - 1, CacheRead: math.MaxInt64 - 1, CacheWrite: math.MaxInt64 - 1,
		CostUSD: 1,
	}
	// Restored from earlier today, before the share window, so that the
	// costs add up in the day's totals alone.
	restored := tracker.UsageRow{
		Minute: at(-7200), Dir: dirA, Session: s1, Model: "Opus 5.5",
		Input: math.MaxInt64, Output: math.MaxInt64, CacheRead: math.MaxInt64, CacheWrite: math.MaxInt64,
		CostUSD: math.MaxFloat64, Requests: math.MaxInt,
	}
	later := restored
	later.Minute = restored.Minute.Add(time.Minute)
	// Another session's row makes the account's totals add up more than one.
	other := restored
	other.Session = s2

	h.tr.RecordAPI(huge)
	h.tr.RecordAPI(huge)
	h.tr.Restore(tracker.Restored{Usage: []tracker.UsageRow{restored, later, other}})

	v := h.tr.Snapshot()
	account := h.account(dirA)
	totals := map[string][]tracker.ModelTotal{
		"today": v.Models, "week": v.ModelsWeek, "account today": account.Models, "account week": account.ModelsWeek,
		"session": h.session(s1).Models,
	}
	for name, models := range totals {
		if len(models) != 1 {
			t.Errorf("%s: models %+v, want Opus 5.5 alone", name, models)
			continue
		}
		m := models[0]
		if m.Input != math.MaxInt64 || m.Output != math.MaxInt64 || m.CacheRead != math.MaxInt64 || m.CacheWrite != math.MaxInt64 ||
			m.Requests != math.MaxInt || m.CostUSD != math.MaxFloat64 {
			t.Errorf("%s: %+v, want every count at its largest and the cost at the largest finite one", name, m)
		}
	}
	for name, spend := range map[string]float64{"today": account.SpendTodayUSD, "week": account.SpendWeekUSD, "month": account.SpendMonthUSD} {
		if spend != math.MaxFloat64 {
			t.Errorf("spend %s %v, want the largest finite cost", name, spend)
		}
	}
	if _, err := json.Marshal(v); err != nil {
		t.Errorf("the view cannot be encoded: %v", err)
	}
}

// Once a day of usage ages out, the days kept go on adding up as before.
func TestTracker_UsageAddsUpAfterADayAgesOut(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(hook(0, s1, "SessionStart"))
	h.tr.RecordAPI(request(at(0), s1, "Opus 5.5", 1))

	month := at(0).Add(30 * 24 * time.Hour)
	h.clock.Set(month)
	h.tr.RecordAPI(request(month, s1, "Opus 5.5", 2))
	// Thirty days on, the first day is no longer among the 30 the view keeps,
	// today included, so building the view forgets it.
	h.tr.Snapshot()
	h.tr.RecordAPI(request(month.Add(time.Minute), s1, "Opus 5.5", 3))

	want := []modelCost{{"Opus 5.5", 5, 2}}
	if got := costsOf(h.account(dirA).Models); !slices.Equal(got, want) {
		t.Errorf("today's models %v, want %v: both of today's calls", got, want)
	}
	if got := costsOf(h.session(s1).Models); !slices.Equal(got, want) {
		t.Errorf("session models %v, want %v: the calls of the days kept", got, want)
	}
}

// Negative counts, which no real call reports, add up as numbers do and stop
// at the smallest a count can be rather than wrapping round to a large one.
func TestTracker_NegativeUsageSumsSaturate(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(hook(0, s1, "SessionStart"))
	row := func(model string, count int64, requests int) tracker.UsageRow {
		return tracker.UsageRow{
			Minute: at(-7200), Dir: dirA, Session: s1, Model: model,
			Input: count, Output: count, CacheRead: count, CacheWrite: count, Requests: requests,
		}
	}
	h.tr.Restore(tracker.Restored{Usage: []tracker.UsageRow{
		row("Haiku 5", -5, -5), row("Haiku 5", -3, -3),
		row("Sonnet 5.5", math.MinInt64+1, math.MinInt+1), row("Sonnet 5.5", math.MinInt64+1, math.MinInt+1),
	}})

	want := []tracker.ModelTotal{
		{Model: "Haiku 5", Input: -8, Output: -8, CacheRead: -8, CacheWrite: -8, Requests: -8},
		{
			Model: "Sonnet 5.5", Input: math.MinInt64, Output: math.MinInt64, CacheRead: math.MinInt64, CacheWrite: math.MinInt64,
			Requests: math.MinInt,
		},
	}
	if got := h.session(s1).Models; !slices.Equal(got, want) {
		t.Errorf("session models\n got %+v\nwant %+v", got, want)
	}
}
