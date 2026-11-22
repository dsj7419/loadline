package tracker_test

import (
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/tracker"
)

// pctOf is a shown window's usage, or -1 when none is shown.
func pctOf(w *tracker.WindowView) float64 {
	if w == nil {
		return -1
	}
	return w.UsedPct
}

// heldPct is a window's usage, or -1 when there is no window.
func heldPct(w *domain.Window) float64 {
	if w == nil {
		return -1
	}
	return w.UsedPct
}

// sampledAt returns the limits sampled at at, and whether any were.
func sampledAt(h *harness, at time.Time) (domain.Limits, bool) {
	_, _, samples, _ := h.rec.recorded()
	for _, s := range samples {
		if s.limits.ObservedAt.Equal(at) {
			return s.limits, true
		}
	}
	return domain.Limits{}, false
}

// Each session's status line carries the rate limits of that session's own
// last response, so one that sits idle goes on reporting an older reading
// while a busy one reports the current one. Within one window, told by its
// reset time, usage never goes down.
func TestTracker_AReadingIsJudgedAgainstTheWindowHeld(t *testing.T) {
	later, earlier := weekResets.Add(7*24*time.Hour), weekResets.Add(-24*time.Hour)
	for name, tc := range map[string]struct {
		held, reading *domain.Window
		want          *domain.Window
		taken         bool // the reading replaced the window, or confirmed it
		sampled       bool
	}{
		"the same window, lower: older news": {
			held: window(100, weekResets), reading: window(97, weekResets), want: window(100, weekResets),
		},
		"the same window, as high: observed afresh": {
			held: window(100, weekResets), reading: window(100, weekResets), want: window(100, weekResets), taken: true,
		},
		"the same window, higher": {
			held: window(97, weekResets), reading: window(100, weekResets), want: window(100, weekResets), taken: true, sampled: true,
		},
		"a later reset: a new window": {
			held: window(100, weekResets), reading: window(3, later), want: window(3, later), taken: true, sampled: true,
		},
		"an earlier reset: a window gone by": {
			held: window(3, weekResets), reading: window(100, earlier), want: window(3, weekResets),
		},
		"no reset known, lower: older news while the window lasts": {
			held: window(100, time.Time{}), reading: window(97, time.Time{}), want: window(100, time.Time{}),
		},
		"no reset known, higher": {
			held: window(97, time.Time{}), reading: window(100, time.Time{}), want: window(100, time.Time{}), taken: true, sampled: true,
		},
		"one reset known, lower: older news while the window lasts": {
			held: window(100, weekResets), reading: window(97, time.Time{}), want: window(100, weekResets),
		},
		"the held window has reset: any reading": {
			held: window(100, at(20)), reading: window(5, time.Time{}), want: window(5, time.Time{}), taken: true, sampled: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, tracker.Config{})
			h.tr.Apply(withWindows(statusLine(0, s1), nil, tc.held))
			h.clock.Set(at(30))

			h.tr.Apply(withWindows(statusLine(30, s2), nil, tc.reading))

			got := h.account(dirA).SevenDay
			if got == nil || got.UsedPct != tc.want.UsedPct || !time.Time(got.ResetsAt).Equal(tc.want.ResetsAt) {
				t.Errorf("weekly window %+v, want %v%% resetting at %v", got, tc.want.UsedPct, tc.want.ResetsAt)
			}
			wantObserved := at(0)
			if tc.taken {
				wantObserved = at(30)
			}
			if got != nil && !time.Time(got.ObservedAt).Equal(wantObserved) {
				t.Errorf("observed at %v, want %v", time.Time(got.ObservedAt), wantObserved)
			}
			if _, sampled := sampledAt(h, at(30)); sampled != tc.sampled {
				t.Errorf("sampled the reading: %v, want %v: only a reading taken is kept", sampled, tc.sampled)
			}
		})
	}
}

// Claude Code drops a window from the line once it resets. A session that has
// not had a window yet leaves it out too, so a window whose reset is still to
// come stays. Whether it goes does not hang on the line's other window, here
// older news.
func TestTracker_AWindowALineLeavesOut(t *testing.T) {
	for name, tc := range map[string]struct {
		fiveHour *domain.Window
		kept     bool
	}{
		"its reset still to come: kept": {fiveHour: window(40, at(3*3600)), kept: true},
		"its reset passed: gone":        {fiveHour: window(40, at(20))},
		"its reset unknown: gone":       {fiveHour: window(40, time.Time{})},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, tracker.Config{})
			h.tr.Apply(withWindows(statusLine(0, s1), tc.fiveHour, window(50, weekResets)))

			h.tr.Apply(withWindows(statusLine(30, s2), nil, window(45, weekResets)))

			_, accounts, _, _ := h.rec.recorded()
			held := accounts[len(accounts)-1].Limits.FiveHour
			if kept := held != nil; kept != tc.kept {
				t.Errorf("five-hour window held %+v, want kept: %v", held, tc.kept)
			}
		})
	}
}

// A line can be news of one window and older news of the other: it is
// observed all the same, and only the window it changed is reported.
func TestTracker_OneWindowTakenTheOtherOlderNews(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	fiveHourResets := at(3 * 3600)
	h.tr.Apply(withWindows(statusLine(0, s1), window(30, fiveHourResets), window(100, weekResets)))

	h.tr.Apply(withWindows(statusLine(30, s2), window(35, fiveHourResets), window(97, weekResets)))

	a := h.account(dirA)
	if pctOf(a.FiveHour) != 35 || pctOf(a.SevenDay) != 100 {
		t.Errorf("five-hour %+v and weekly %+v, want 35%% and the 100%% held", a.FiveHour, a.SevenDay)
	}
	if !time.Time(a.SevenDay.ObservedAt).Equal(at(30)) {
		t.Errorf("observed at %v, want %v: the line was news of the five-hour window", time.Time(a.SevenDay.ObservedAt), at(30))
	}
	_, accounts, _, _ := h.rec.recorded()
	if last := accounts[len(accounts)-1].Limits; heldPct(last.FiveHour) != 35 || heldPct(last.SevenDay) != 100 {
		t.Errorf("account recorded with five-hour %+v and weekly %+v, want 35%% and 100%%", last.FiveHour, last.SevenDay)
	}
	if sampled, ok := sampledAt(h, at(30)); !ok || heldPct(sampled.FiveHour) != 35 || sampled.SevenDay != nil {
		t.Errorf("sampled %+v, want the five-hour window alone", sampled)
	}
}

// Without the reset time, a lower reading can be told from an older one only
// by age: once the reading held was first taken longer ago than its window
// lasts, the window has reset since. Taking the same reading again does not
// make it any younger.
func TestTracker_WithoutAResetTimeALowerReadingWaitsOutTheWindow(t *testing.T) {
	for name, tc := range map[string]struct {
		length   time.Duration
		withLine func(st domain.Status, w *domain.Window) domain.Status
		shown    func(tracker.AccountView) *tracker.WindowView
	}{
		"five-hour": {
			length:   5 * time.Hour,
			withLine: func(st domain.Status, w *domain.Window) domain.Status { return withWindows(st, w, nil) },
			shown:    func(a tracker.AccountView) *tracker.WindowView { return a.FiveHour },
		},
		"seven-day": {
			length:   7 * 24 * time.Hour,
			withLine: func(st domain.Status, w *domain.Window) domain.Status { return withWindows(st, nil, w) },
			shown:    func(a tracker.AccountView) *tracker.WindowView { return a.SevenDay },
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, tracker.Config{})
			length := int(tc.length / time.Second)
			apply := func(sec int, id domain.SessionID, pct float64) {
				h.clock.Set(at(sec))
				h.tr.Apply(tc.withLine(statusLine(sec, id), window(pct, time.Time{})))
			}
			apply(0, s1, 100)
			apply(length-60, s1, 100)

			apply(length, s2, 20)
			if got := tc.shown(h.account(dirA)); pctOf(got) != 100 {
				t.Fatalf("window %+v a window's length after the reading held was first taken, want the 100%% held", got)
			}
			apply(length+1, s2, 20)
			if got := tc.shown(h.account(dirA)); pctOf(got) != 20 {
				t.Errorf("window %+v past a window's length, want the 20%% reading", got)
			}
		})
	}
}

// A restored window's reading dates from when it was observed, so a restart
// takes no older reading for news.
func TestTracker_ARestoredWindowIsJudgedAsOfItsObservation(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Restore(tracker.Restored{Accounts: []domain.Account{{
		Dir:    dirA,
		Limits: domain.Limits{SevenDay: window(100, time.Time{}), ObservedAt: at(-60)},
	}}})

	h.tr.Apply(withWindows(statusLine(0, s2), nil, window(97, time.Time{})))

	if got := h.account(dirA).SevenDay; pctOf(got) != 100 {
		t.Errorf("weekly window %+v, want the 100%% observed a minute before", got)
	}
}

// Only the readings taken are recorded, so the spans a restart rebuilds from
// the samples are those the tracker held.
func TestTracker_RecordsOnlyTheReadingsItTakes(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	for sec := 0; sec < 300; sec += 5 {
		h.clock.Set(at(sec))
		h.tr.Apply(withWindows(statusLine(sec, s1), nil, window(100, weekResets)))
		h.tr.Apply(withWindows(statusLine(sec+2, s2), nil, window(97, weekResets)))
	}

	_, _, recorded, _ := h.rec.recorded()
	var samples []domain.Sample
	for _, r := range recorded {
		if r.limits.SevenDay != nil {
			samples = append(samples, domain.Sample{At: r.limits.ObservedAt, UsedPct: r.limits.SevenDay.UsedPct, ResetsAt: r.limits.SevenDay.ResetsAt})
		}
	}
	spans := domain.OnCreditsSpans(domain.WindowSamples{SevenDay: samples}, at(300))
	if len(spans) != 1 || !spans[0].From.Equal(at(0)) || !spans[0].To.IsZero() {
		t.Errorf("%d spans of the %d samples recorded, the first %v, want one open since %v", len(spans), len(samples), spans[:min(len(spans), 2)], at(0))
	}
}
