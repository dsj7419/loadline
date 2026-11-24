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

// Without a reset time to tell windows apart, a reading as high as the one
// held is news of it: the window is observed afresh, and learns the reset
// time the reading brings.
func TestTracker_WithoutAResetTimeAnEqualReadingIsNews(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(withWindows(statusLine(0, s1), nil, window(50, time.Time{})))

	h.tr.Apply(withWindows(statusLine(10, s2), nil, window(50, time.Time{})))
	if got := h.account(dirA).SevenDay; got == nil || !time.Time(got.ObservedAt).Equal(at(10)) {
		t.Errorf("weekly window %+v, want the 50%% observed afresh at +10s", got)
	}

	h.tr.Apply(withWindows(statusLine(20, s2), nil, window(50, weekResets)))
	if got := h.account(dirA).SevenDay; got == nil || !time.Time(got.ResetsAt).Equal(weekResets) {
		t.Errorf("weekly window %+v, want it to reset at %v, as the reading at +20s says", got, weekResets)
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

	samples := recordedWeeklySamples(h)
	spans := domain.OnCreditsSpans(domain.WindowSamples{SevenDay: samples}, at(300))
	if len(spans) != 1 || !spans[0].From.Equal(at(0)) || !spans[0].To.IsZero() {
		t.Errorf("%d spans of the %d samples recorded, the first %v, want one open since %v", len(spans), len(samples), spans[:min(len(spans), 2)], at(0))
	}
}

// recordedWeeklySamples returns the weekly window's samples the tracker
// recorded, which are what a restart rebuilds its spans from.
func recordedWeeklySamples(h *harness) []domain.Sample {
	_, _, recorded, _ := h.rec.recorded()
	var samples []domain.Sample
	for _, r := range recorded {
		if w := r.limits.SevenDay; w != nil {
			samples = append(samples, domain.Sample{At: r.limits.ObservedAt, UsedPct: w.UsedPct, ResetsAt: w.ResetsAt})
		}
	}
	return samples
}

// withAPITime is st with the session's API time at ms.
func withAPITime(st domain.Status, ms int64) domain.Status {
	st.APIDurationMS = ms
	return st
}

// weeklyFrom is a status line of id at sec that reports the weekly window at
// pct, resetting at weekResets.
func weeklyFrom(sec int, id domain.SessionID, pct float64) domain.Status {
	return withWindows(statusLine(sec, id), nil, window(pct, weekResets))
}

// shownWeekly applies st and returns the weekly usage shown after it.
func shownWeekly(h *harness, st domain.Status) float64 {
	h.t.Helper()
	h.clock.Set(st.At)
	h.tr.Apply(st)
	return pctOf(h.account(dirA).SevenDay)
}

// The supervisor's log of one account's weekly window over half an hour, its
// readings taken in turn from the status lines of a working session and an
// idle one, all with the same reset: 74, 75, 72, 72, 78, 72, 80. The window
// shown never falls below the freshest reading.
func TestTracker_AlternatingSessionsShowTheFreshestReading(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	working, idle := s1, s2
	h.tr.RecordAPI(request(at(5), idle, "Opus 5.5", 0.1))
	shownWeekly(h, weeklyFrom(6, idle, 72))

	for i, step := range []struct {
		id        domain.SessionID
		callSec   int // the working session's call before its line, 0 for none
		sec       int
		pct, want float64
	}{
		{working, 10, 11, 74, 74},
		{working, 20, 21, 75, 75},
		{idle, 0, 26, 72, 75},
		{idle, 0, 31, 72, 75},
		{working, 35, 36, 78, 78},
		{idle, 0, 41, 72, 78},
		{working, 45, 46, 80, 80},
	} {
		if step.callSec > 0 {
			h.tr.RecordAPI(request(at(step.callSec), step.id, "Opus 5.5", 0.1))
		}
		if got := shownWeekly(h, weeklyFrom(step.sec, step.id, step.pct)); got != step.want {
			t.Errorf("reading %d, %v%% from %s: shown %v%%, want %v%%", i+1, step.pct, step.id, got, step.want)
		}
	}
}

// The owner's mid-week reset keeps the reset time and takes the window from
// 95% to 0%. The busy session's next response reports the 0%, which is news
// however much lower it is, and the 95% an idle session goes on reporting
// from its older response is not, however much higher. Here the tracker
// knows how fresh each reading is from the sessions' calls alone.
func TestTracker_AResetThatKeepsItsResetTimeShowsFromTheNextResponse(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	busy, idle := s1, s2
	h.tr.RecordAPI(request(at(0), idle, "Opus 5.5", 0.1))
	shownWeekly(h, weeklyFrom(1, idle, 95))
	h.tr.RecordAPI(request(at(10), busy, "Opus 5.5", 0.1))
	shownWeekly(h, weeklyFrom(11, busy, 95))

	h.tr.RecordAPI(request(at(60), busy, "Opus 5.5", 0.1))
	if got := shownWeekly(h, weeklyFrom(61, busy, 0)); got != 0 {
		t.Errorf("shown %v%% after the busy session's response since the reset, want 0%%", got)
	}
	if got := shownWeekly(h, weeklyFrom(66, idle, 95)); got != 0 {
		t.Errorf("shown %v%% after the idle session's older reading, want the 0%% still", got)
	}
	if sampled, ok := sampledAt(h, at(61)); !ok || heldPct(sampled.SevenDay) != 0 {
		t.Errorf("sampled %+v at the busy session's reading, want 0%%", sampled)
	}
	if sampled, ok := sampledAt(h, at(66)); ok {
		t.Errorf("sampled %+v at the idle session's older reading, want nothing kept", sampled)
	}
}

// The same reset, with the tracker knowing how fresh each reading is from the
// sessions' API time alone: a line on which it has grown came with a response
// the session got since its last line.
func TestTracker_FreshnessFromTheStatusLinesAPITime(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	busy, idle := s1, s2
	shownWeekly(h, withAPITime(weeklyFrom(0, busy, 95), 1000))
	shownWeekly(h, withAPITime(weeklyFrom(1, idle, 95), 5000))
	shownWeekly(h, withAPITime(weeklyFrom(10, busy, 95), 1200))
	shownWeekly(h, withAPITime(weeklyFrom(12, idle, 95), 5100))

	if got := shownWeekly(h, withAPITime(weeklyFrom(60, busy, 0), 1500)); got != 0 {
		t.Errorf("shown %v%% once the busy session's API time grew past the reset, want 0%%", got)
	}
	if got := shownWeekly(h, withAPITime(weeklyFrom(61, idle, 95), 5100)); got != 0 {
		t.Errorf("shown %v%% after the idle session's line, its API time as it was, want the 0%% still", got)
	}
	if got := shownWeekly(h, withAPITime(weeklyFrom(65, busy, 0), 1500)); got != 0 {
		t.Errorf("shown %v%% when the busy session reports its reading again, want 0%%", got)
	}
}

// A call delivered after a later one, as batches of events can be, leaves the
// session as fresh as the later one made it.
func TestTracker_AnOlderCallDeliveredLateKeepsTheSessionFresh(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	busy, other := s1, s2
	h.tr.RecordAPI(request(at(10), busy, "Opus 5.5", 0.1))
	shownWeekly(h, weeklyFrom(11, busy, 95))
	h.tr.RecordAPI(request(at(30), other, "Opus 5.5", 0.1))
	shownWeekly(h, weeklyFrom(31, other, 95))

	h.tr.RecordAPI(request(at(60), busy, "Opus 5.5", 0.1))
	h.tr.RecordAPI(request(at(5), busy, "Opus 5.5", 0.1))

	if got := shownWeekly(h, weeklyFrom(61, busy, 0)); got != 0 {
		t.Errorf("shown %v%%, want the 0%% of the busy session's response at %v", got, at(60))
	}
}

// A line of a session older than its last, delivered late, says nothing of
// its responses: the next line's API time is judged against the newest line's.
func TestTracker_ALateLineDoesNotMakeTheNextOneFresh(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	late, other := s1, s2
	shownWeekly(h, withAPITime(weeklyFrom(0, late, 95), 1000))
	shownWeekly(h, withAPITime(weeklyFrom(10, late, 95), 1200))
	h.tr.RecordAPI(request(at(12), other, "Opus 5.5", 0.1))
	shownWeekly(h, weeklyFrom(13, other, 96))

	h.tr.Apply(withAPITime(weeklyFrom(9, late, 95), 1100))

	if got := shownWeekly(h, withAPITime(weeklyFrom(15, late, 95), 1200)); got != 96 {
		t.Errorf("shown %v%%, want the 96%% of the fresher session: the late line's API time had not grown since", got)
	}
}

// The tracker learns of every response a session gets while it watches it,
// so a session it has seen none from has had none since its first line: what
// it reports is older than any reading whose response the tracker knows.
func TestTracker_AReadingOfUnknownFreshnessIsOlderNews(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	busy, idle := s1, s2
	h.tr.RecordAPI(request(at(10), busy, "Opus 5.5", 0.1))
	shownWeekly(h, weeklyFrom(11, busy, 95))
	shownWeekly(h, withAPITime(weeklyFrom(12, idle, 95), 4000))

	h.tr.RecordAPI(request(at(60), busy, "Opus 5.5", 0.1))
	shownWeekly(h, weeklyFrom(61, busy, 0))
	if got := shownWeekly(h, withAPITime(weeklyFrom(66, idle, 95), 4000)); got != 0 {
		t.Errorf("shown %v%% after a session the tracker knows no response of reported 95%%, want the 0%% still", got)
	}
}

// Freshness is judged against a window that still stands: once the one held
// has reset, a session's reading of the window begun since is news, however
// little the tracker knows of its responses.
func TestTracker_OnceTheWindowHeldHasResetAnyReadingIsNews(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	resets := at(100)
	h.tr.RecordAPI(request(at(10), s1, "Opus 5.5", 0.1))
	h.clock.Set(at(11))
	h.tr.Apply(withWindows(statusLine(11, s1), nil, window(95, resets)))

	h.clock.Set(at(120))
	h.tr.Apply(withWindows(statusLine(120, s3), nil, window(3, resets.Add(7*24*time.Hour))))

	if got := h.account(dirA).SevenDay; got == nil || got.UsedPct != 3 {
		t.Errorf("weekly window %+v, want the 3%% of the window begun at the reset", got)
	}
}

// After a restart the tracker knows no session's responses, and judges every
// reading by its window until a session's next response, which a reset that
// kept its reset time then shows from.
func TestTracker_AfterARestartFreshnessIsUnknownUntilTheNextResponse(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Restore(tracker.Restored{
		Accounts: []domain.Account{{Dir: dirA, Limits: domain.Limits{SevenDay: window(95, weekResets), ObservedAt: at(-60)}}},
		Sessions: []domain.Session{restoredSession(s1, domain.StateIdle, at(-60))},
	})

	if got := shownWeekly(h, withAPITime(weeklyFrom(0, s1, 0), 3000)); got != 95 {
		t.Errorf("shown %v%% from the first line after the restart, want the 95%% held: the line's freshness is unknown", got)
	}
	if got := shownWeekly(h, withAPITime(weeklyFrom(5, s1, 0), 3000)); got != 95 {
		t.Errorf("shown %v%% from a line with the same API time, want the 95%% held", got)
	}
	if got := shownWeekly(h, withAPITime(weeklyFrom(10, s1, 0), 3400)); got != 0 {
		t.Errorf("shown %v%% once the session's API time grew, want 0%%", got)
	}
}

// Only the readings taken are recorded, so the reset ends the stretch on
// credits for a restart too, and an idle session's older reading at the
// limit opens no new one.
func TestTracker_AResetThatKeepsItsResetTimeEndsTheStretchOnCredits(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	busy, idle := s1, s2
	h.tr.RecordAPI(request(at(0), idle, "Opus 5.5", 0))
	shownWeekly(h, weeklyFrom(1, idle, 100))
	h.tr.RecordAPI(request(at(10), busy, "Opus 5.5", 1))
	shownWeekly(h, weeklyFrom(11, busy, 100))
	h.tr.RecordAPI(request(at(60), busy, "Opus 5.5", 0))
	shownWeekly(h, weeklyFrom(61, busy, 0))
	shownWeekly(h, weeklyFrom(66, idle, 100))
	h.tr.RecordAPI(request(at(70), busy, "Opus 5.5", 2))
	h.clock.Set(at(80))

	want := tracker.CreditsView{BalanceUSD: 20, SpentUSD: 1, RemainingUSD: 19, Days: 30, Source: domain.CreditSourceEstimate}
	if got := h.account(dirA).Credits; got == nil || *got != want {
		t.Errorf("credits %+v, want %+v", got, want)
	}
	spans := domain.OnCreditsSpans(domain.WindowSamples{SevenDay: recordedWeeklySamples(h)}, at(80))
	if len(spans) != 1 || !spans[0].From.Equal(at(1)) || !spans[0].To.Equal(at(61)) {
		t.Errorf("spans %v of the samples recorded, want one from %v to %v", spans, at(1), at(61))
	}
}

// A session ID can belong to two live processes at once: Claude Code keeps
// it when a session is resumed, and the first terminal may still be open.
// Each process counts its own API time, so the idle one's high count going up
// from the busy one's low count says nothing of a new response.
func TestTracker_TwoProcessesOfOneSessionDoNotFlipTheWindow(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	shared := s1
	shownWeekly(h, withAPITime(weeklyFrom(0, shared, 97), 50_000))
	below, views := 0, 0
	api := int64(1200)
	for sec := 10; sec < 300; sec += 5 {
		api += 100
		shownWeekly(h, withAPITime(weeklyFrom(sec, shared, 100), api))
		if got := shownWeekly(h, withAPITime(weeklyFrom(sec+2, shared, 97), 50_000)); got < 100 {
			below++
		}
		views++
	}
	if below > 0 {
		t.Errorf("weekly window fell back to 97%% after %d of %d lines from the idle process, want none", below, views)
	}
}

// onAccountB is st sent from account B, its weekly window that account's.
func onAccountB(st domain.Status) domain.Status {
	st.Dir = dirB
	st.Limits.SevenDay = window(10, weekResets.Add(48*time.Hour))
	return st
}

// A session resumed on another account while its first process still runs,
// idle, on the first: the resumed process works on account B, while on
// account A s3 works at the limit and the old process goes on reporting A's
// older 97%. Neither B's responses nor the resumed process's API time say
// anything of how fresh the old process's reading of A is.
func TestTracker_AResumeOnAnotherAccountDoesNotFlipTheFirstAccount(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts}, dirA, dirB)
	moved, busyA := s1, s3
	h.tr.RecordAPI(request(at(0), moved, "Opus 5.5", 0.1))
	shownWeekly(h, withAPITime(weeklyFrom(1, moved, 97), 50_000))
	h.tr.RecordAPI(request(at(5), busyA, "Opus 5.5", 0.1))
	shownWeekly(h, withAPITime(weeklyFrom(6, busyA, 100), 2000))

	below, views := 0, 0
	sinces := map[int64]bool{}
	for sec := 10; sec < 310; sec += 10 {
		h.tr.RecordAPI(request(at(sec), moved, "Opus 5.5", 0.1))
		h.clock.Set(at(sec + 1))
		h.tr.Apply(withAPITime(onAccountB(statusLine(sec+1, moved)), 1000+int64(sec)))
		h.tr.RecordAPI(request(at(sec+3), busyA, "Opus 5.5", 0.5))
		shownWeekly(h, withAPITime(weeklyFrom(sec+4, busyA, 100), 2000+int64(sec)))
		h.clock.Set(at(sec + 6))
		h.tr.Apply(withAPITime(weeklyFrom(sec+6, moved, 97), 50_000))
		v := h.tr.Snapshot()
		for _, a := range v.Accounts {
			if a.Dir == dirA && (a.SevenDay == nil || a.SevenDay.UsedPct < 100) {
				below++
			}
		}
		for _, a := range alertsOfKind(v, domain.AlertCreditsActive) {
			if a.Dir == dirA {
				sinces[a.Since.UnixMilli()] = true
			}
		}
		views++
	}
	if below > 0 || len(sinces) != 1 {
		t.Errorf("account A shown below 100%% in %d of %d views, on credits with %d different since times; want none and 1: the old process's reading is older news", below, views, len(sinces))
	}
}

// As above with no help from API time, the old process's staying below the
// resumed one's: only the resumed process's calls, which are on B, could make
// the old process's reading of A look fresh, and they must not.
func TestTracker_AResumedProcesssCallsDoNotFreshenTheOldOnesReading(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts}, dirA, dirB)
	moved, busyA := s1, s3
	h.tr.RecordAPI(request(at(0), moved, "Opus 5.5", 0.1))
	shownWeekly(h, withAPITime(weeklyFrom(1, moved, 97), 500))
	h.tr.RecordAPI(request(at(5), busyA, "Opus 5.5", 0.1))
	shownWeekly(h, withAPITime(weeklyFrom(6, busyA, 100), 2000))

	below, views := 0, 0
	for sec := 10; sec < 310; sec += 10 {
		h.tr.RecordAPI(request(at(sec+1), busyA, "Opus 5.5", 0.5))
		shownWeekly(h, withAPITime(weeklyFrom(sec+2, busyA, 100), 2000+int64(sec)))
		h.tr.RecordAPI(request(at(sec+3), moved, "Opus 5.5", 0.1))
		h.clock.Set(at(sec + 4))
		h.tr.Apply(withAPITime(onAccountB(statusLine(sec+4, moved)), 1000+int64(sec)))
		if got := shownWeekly(h, withAPITime(weeklyFrom(sec+6, moved, 97), 500)); got < 100 {
			below++
		}
		views++
	}
	if below > 0 {
		t.Errorf("account A shown below 100%% in %d of %d views, want none: the resumed process's calls on B say nothing of the old process's reading of A", below, views)
	}
}

// A session's process ends, and the owner's reset comes before the session is
// resumed: the resumed process counts its API time from nothing, and an idle
// session goes on reporting its older 95%. The resumed process's 0% shows by
// its second response, and the idle 95% does not come back.
func TestTracker_ASessionResumedAfterItEndedCountsItsAPITimeAfresh(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	resumed, idle := s1, s2
	shownWeekly(h, withAPITime(weeklyFrom(0, idle, 95), 3000))
	shownWeekly(h, withAPITime(weeklyFrom(1, idle, 95), 3100))
	shownWeekly(h, withAPITime(weeklyFrom(2, resumed, 95), 40_000))
	shownWeekly(h, withAPITime(weeklyFrom(3, resumed, 95), 50_000))

	h.clock.Set(at(50))
	h.tr.Apply(sessionEnd(50, resumed, "prompt_input_exit"))
	start := hook(99, resumed, "SessionStart")
	start.Source = "resume"
	h.clock.Set(at(99))
	h.tr.Apply(start)
	// The resumed process's first line is a first line: nothing says what its
	// count started from, so it dates no response.
	if got := shownWeekly(h, withAPITime(weeklyFrom(100, resumed, 0), 800)); got != 95 {
		t.Errorf("shown %v%% from the resumed process's first line, want the 95%% held", got)
	}
	shownWeekly(h, withAPITime(weeklyFrom(105, idle, 95), 3100))

	if got := shownWeekly(h, withAPITime(weeklyFrom(110, resumed, 0), 1600)); got != 0 {
		t.Errorf("shown %v%% after the resumed process's second response, want 0%%", got)
	}
	for sec := 115; sec < 200; sec += 5 {
		if got := shownWeekly(h, withAPITime(weeklyFrom(sec, idle, 95), 3100)); got != 0 {
			t.Fatalf("at %ds the idle session's older 95%% came back (%v%%)", sec, got)
		}
	}
}

// Resuming a session while its first process still runs starts a second
// process without ending the first: the two count their API time apart, so
// the most either has reported still holds.
func TestTracker_AResumeWhileTheFirstProcessRunsKeepsTheMostAPITime(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	shared := s1
	shownWeekly(h, withAPITime(weeklyFrom(0, shared, 97), 50_000))
	start := hook(5, shared, "SessionStart")
	start.Source, start.PID = "resume", pidB
	h.clock.Set(at(5))
	h.tr.Apply(start)

	below, views := 0, 0
	api := int64(1200)
	for sec := 10; sec < 300; sec += 5 {
		api += 100
		shownWeekly(h, withAPITime(weeklyFrom(sec, shared, 100), api))
		if got := shownWeekly(h, withAPITime(weeklyFrom(sec+2, shared, 97), 50_000)); got < 100 {
			below++
		}
		views++
	}
	if below > 0 {
		t.Errorf("weekly window fell back to 97%% after %d of %d lines from the first process, want none", below, views)
	}
}

// fromProcess is st sent from the Claude process pid.
func fromProcess(st domain.Status, pid int) domain.Status {
	st.PID = pid
	return st
}

// A session is resumed on the same account while its first process stays
// open and idle, and the owner's reset keeps the reset time. When the lines
// name their processes, each is judged by its own: the resumed process's API
// time growing from where its first line put it dates its 0%, however far
// below the first process's count it stays, and the first process's older 97%
// does not come back. The resumed process counts from nothing when the session
// saved no API time, and otherwise continues from what it saved.
func TestTracker_ASameWindowResetShowsWhileAnotherProcessOfTheSessionLives(t *testing.T) {
	for name, base := range map[string]int64{
		"counting from nothing":                              0,
		"continuing from a restored count below the first's": 30_000,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, tracker.Config{})
			shared := s1
			first := func(sec int) domain.Status {
				return fromProcess(withAPITime(weeklyFrom(sec, shared, 97), 50_000), pidA)
			}
			shownWeekly(h, fromProcess(withAPITime(weeklyFrom(0, shared, 97), 49_000), pidA))
			shownWeekly(h, first(1))
			start := hook(5, shared, "SessionStart")
			start.Source, start.PID = "resume", pidB
			h.clock.Set(at(5))
			h.tr.Apply(start)

			if got := shownWeekly(h, fromProcess(withAPITime(weeklyFrom(10, shared, 0), base), pidB)); got != 97 {
				t.Errorf("shown %v%% from the resumed process's first line, want the 97%% held: nothing says what its count started from", got)
			}
			shownWeekly(h, first(12))
			if got := shownWeekly(h, fromProcess(withAPITime(weeklyFrom(20, shared, 0), base+400), pidB)); got != 0 {
				t.Errorf("shown %v%% once the resumed process's API time grew from %d, want 0%%", got, base)
			}
			for sec := 25; sec < 120; sec += 5 {
				if got := shownWeekly(h, first(sec)); got != 0 {
					t.Fatalf("at %ds the first process's older 97%% came back (%v%%)", sec, got)
				}
			}
		})
	}
}

// Every line from a current Claude Code names its process. The calls waiting
// for a line date the process whose line takes them, so a reset that keeps
// its reset time shows from the busy session's next response, as it does when
// lines name no process.
func TestTracker_TheCallsALineTakesDateItsProcess(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	busy, idle := s1, s2
	h.tr.RecordAPI(request(at(0), idle, "Opus 5.5", 0.1))
	shownWeekly(h, fromProcess(weeklyFrom(1, idle, 95), pidA))
	h.tr.RecordAPI(request(at(10), busy, "Opus 5.5", 0.1))
	shownWeekly(h, fromProcess(weeklyFrom(11, busy, 95), pidB))

	h.tr.RecordAPI(request(at(60), busy, "Opus 5.5", 0.1))
	if got := shownWeekly(h, fromProcess(weeklyFrom(61, busy, 0), pidB)); got != 0 {
		t.Errorf("shown %v%% after the busy process's response since the reset, want 0%%", got)
	}
	if got := shownWeekly(h, fromProcess(weeklyFrom(66, idle, 95), pidA)); got != 0 {
		t.Errorf("shown %v%% after the idle process's older reading, want the 0%% still", got)
	}
}

// A session is resumed on account B while its first process stays open and
// idle on account A. The resumed process's API time never passes the old
// one's, so its calls wait unplaced. When the resumed process ends, the old
// process's next line counts as a first line again, but the calls that were
// waiting were made on B and must not freshen the old process's older
// reading of A.
func TestTracker_AnEndedResumeLeavesItsCallsOffTheOldProcesssAccount(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts}, dirA, dirB)
	moved, busyA := s1, s3
	onB := func(st domain.Status) domain.Status {
		st.Dir = dirB
		st.Limits.SevenDay = window(10, weekResets.Add(48*time.Hour))
		return st
	}
	h.tr.RecordAPI(request(at(0), moved, "Opus 5.5", 0.1))
	shownWeekly(h, withAPITime(weeklyFrom(1, moved, 97), 50_000))
	h.tr.RecordAPI(request(at(5), busyA, "Opus 5.5", 0.1))
	shownWeekly(h, withAPITime(weeklyFrom(6, busyA, 100), 2000))
	sec := 10
	for ; sec < 110; sec += 10 {
		h.tr.RecordAPI(request(at(sec+1), busyA, "Opus 5.5", 0.5))
		shownWeekly(h, withAPITime(weeklyFrom(sec+2, busyA, 100), 2000+int64(sec)))
		h.tr.RecordAPI(domain.APIRequest{At: at(sec + 3), Session: moved, Model: "Opus 5.5", CostUSD: 0.1})
		h.clock.Set(at(sec + 4))
		h.tr.Apply(withAPITime(onB(statusLine(sec+4, moved)), 1000+int64(sec)))
		shownWeekly(h, withAPITime(weeklyFrom(sec+6, moved, 97), 50_000))
	}
	end := sessionEnd(sec, moved, "prompt_input_exit")
	end.Dir, end.PID = dirB, pidB
	h.clock.Set(at(sec))
	h.tr.Apply(end)

	if got := shownWeekly(h, withAPITime(weeklyFrom(sec+1, moved, 97), 50_000)); got != 100 {
		t.Errorf("account A shown %v%% after the old process's next line, want 100%%: the calls it took were made on B", got)
	}
}
