package tracker_test

import (
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/costtest"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/tracker"
)

// twentyDollars is a grant added the day before t0.
var twentyDollars = map[domain.ConfigDir][]domain.CreditGrant{dirA: {{USD: 20, AddedAt: at(-86400)}}}

// weekResets is when the test accounts' weekly window resets.
var weekResets = at(0).Add(5 * 24 * time.Hour)

func weekly(sec int, pct float64) domain.Status {
	return withWindows(statusLine(sec, s1), nil, window(pct, weekResets))
}

// newSubscribedHarness is a harness whose accounts, dirA unless dirs are
// given, sign in with a subscription: only those have usage credits.
func newSubscribedHarness(t *testing.T, cfg tracker.Config, dirs ...domain.ConfigDir) *harness {
	t.Helper()
	h := newHarness(t, cfg)
	if len(dirs) == 0 {
		dirs = []domain.ConfigDir{dirA}
	}
	for _, dir := range dirs {
		h.tr.SetAccount(domain.Account{Dir: dir, Auth: domain.AuthSubscription})
	}
	return h
}

func alertsOfKind(v tracker.View, kind domain.AlertKind) []tracker.AlertView {
	var alerts []tracker.AlertView
	for _, a := range v.Alerts {
		if a.Kind == kind {
			alerts = append(alerts, a)
		}
	}
	return alerts
}

func TestTracker_CreditsEstimatedFromCallsAtTheLimit(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	h.tr.Apply(weekly(0, 99))
	h.tr.RecordAPI(request(at(30), s1, "Opus 5.5", 1))
	h.tr.Apply(weekly(60, 100))
	h.tr.RecordAPI(request(at(90), s1, "Opus 5.5", 2.5))
	h.tr.RecordAPI(request(at(120), s1, "Opus 5.5", 3.75))
	h.clock.Set(at(150))

	v := h.tr.Snapshot()

	want := tracker.CreditsView{
		BalanceUSD: 20, SpentUSD: 6.25, RemainingUSD: 13.75, Days: 30,
		OnCreditsSince: tracker.Timestamp(at(60)), Active: true, Source: domain.CreditSourceEstimate,
	}
	if got := h.account(dirA).Credits; got == nil || *got != want {
		t.Errorf("credits %+v, want %+v", got, want)
	}
	active := alertsOfKind(v, domain.AlertCreditsActive)
	if len(active) != 1 || active[0].Dir != dirA || !active[0].Since.Equal(at(60)) {
		t.Errorf("credits_active alerts %+v, want one for %s since %v", active, dirA, at(60))
	}
}

func TestTracker_CreditsLowAlert(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	h.tr.Apply(weekly(0, 100))
	h.tr.RecordAPI(request(at(30), s1, "Opus 5.5", 15.75))
	if low := alertsOfKind(h.tr.Snapshot(), domain.AlertCreditsLow); len(low) != 0 {
		t.Fatalf("credits_low alerts %+v at $15.75 of $20, want none", low)
	}

	h.tr.RecordAPI(request(at(40), s1, "Opus 5.5", 0.25))

	if low := alertsOfKind(h.tr.Snapshot(), domain.AlertCreditsLow); len(low) != 1 || low[0].Dir != dirA {
		t.Errorf("credits_low alerts %+v at $16 of $20, want one for %s", low, dirA)
	}
}

func TestTracker_CreditsAreLeftOutWithoutGrantsOrAReport(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Alerts: defaultAlerts})
	h.tr.Apply(weekly(0, 100))
	h.tr.RecordAPI(request(at(30), s1, "Opus 5.5", 2))

	v := h.tr.Snapshot()

	if got := h.account(dirA).Credits; got != nil {
		t.Errorf("credits %+v, want none: nothing is recorded or reported", got)
	}
	if active := alertsOfKind(v, domain.AlertCreditsActive); len(active) != 1 {
		t.Errorf("credits_active alerts %+v, want one: the account is on credits all the same", active)
	}
}

func TestTracker_ReportedSpendLimitReplacesTheEstimate(t *testing.T) {
	reporting := func(sec int, used, limit float64) domain.Status {
		st := weekly(sec, 100)
		st.SpendLimit = &domain.SpendLimit{UsedUSD: used, LimitUSD: limit}
		return st
	}
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	h.tr.Apply(weekly(0, 100))
	h.tr.RecordAPI(request(at(30), s1, "Opus 5.5", 2))
	h.tr.Apply(reporting(60, 314.25, 500))
	// A newer line without a spend limit is no news of it, and an older one
	// that arrives late is stale.
	h.tr.Apply(weekly(90, 100))
	h.tr.Apply(reporting(45, 1, 2))
	h.clock.Set(at(100))

	want := tracker.CreditsView{
		BalanceUSD: 500, SpentUSD: 314.25, RemainingUSD: 185.75, Days: 30,
		OnCreditsSince: tracker.Timestamp(at(0)), Active: true, Source: domain.CreditSourceReported,
	}
	if got := h.account(dirA).Credits; got == nil || *got != want {
		t.Errorf("credits %+v, want %+v", got, want)
	}
}

func TestTracker_ReportedSpendLimitIsShownWithoutGrants(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{})
	st := statusLine(0, s1)
	st.SpendLimit = &domain.SpendLimit{UsedUSD: 12.5, LimitUSD: 50}
	h.tr.Apply(st)

	want := tracker.CreditsView{BalanceUSD: 50, SpentUSD: 12.5, RemainingUSD: 37.5, Days: 30, Source: domain.CreditSourceReported}
	if got := h.account(dirA).Credits; got == nil || *got != want {
		t.Errorf("credits %+v, want %+v", got, want)
	}
}

// A line observed before the newest report and delivered after it is stale,
// however far the clock has moved on by then.
func TestTracker_AnOlderReportDeliveredLateIsStale(t *testing.T) {
	reporting := func(sec int, used, limit float64) domain.Status {
		st := statusLine(sec, s1)
		st.SpendLimit = &domain.SpendLimit{UsedUSD: used, LimitUSD: limit}
		return st
	}
	h := newSubscribedHarness(t, tracker.Config{})
	h.clock.Set(at(60))
	h.tr.Apply(reporting(60, 314.25, 500))

	h.clock.Set(at(70))
	h.tr.Apply(reporting(45, 1, 2))

	want := tracker.CreditsView{BalanceUSD: 500, SpentUSD: 314.25, RemainingUSD: 185.75, Days: 30, Source: domain.CreditSourceReported}
	if got := h.account(dirA).Credits; got == nil || *got != want {
		t.Errorf("credits %+v, want %+v: the report observed at %v is the newest", got, want, at(60))
	}
}

// A call is held while a status line that arrives late could still show it
// was made at a limit, and after that only while a span on credits holds it,
// so the calls held stay bounded.
func TestTracker_ForgetsCallsNoSpanHoldsOnceTheyAgeOut(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	resets := at(50)
	h.tr.Apply(withWindows(statusLine(0, s1), nil, window(100, resets)))
	h.tr.RecordAPI(request(at(30), s1, "Opus 5.5", 1))
	h.tr.Apply(withWindows(statusLine(60, s1), nil, window(40, resets.Add(7*24*time.Hour))))
	const minutes = 180
	for m := 1; m <= minutes; m++ {
		h.clock.Set(at(60 * m))
		h.tr.RecordAPI(request(at(60*m), s1, "Opus 5.5", 0.1))
	}

	h.tr.Snapshot()

	// The view is at minute 180: the calls from minute 60 on are within the
	// last two hours, and the one at 30 s is in the span on credits.
	if got, want := tracker.CallsHeld(h.tr, dirA), minutes-60+1+1; got != want {
		t.Errorf("%d calls held after the view, want %d: those of the last two hours and the one on credits", got, want)
	}
	if got := h.account(dirA).Credits; got == nil || got.SpentUSD != 1 {
		t.Errorf("credits %+v, want the $1 spent on credits kept", got)
	}
}

// Samples and calls older than any projection reads are still needed while
// they say what was spent on credits.
func TestTracker_CreditSpendOutlivesTheSampleHorizon(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	h.tr.Apply(weekly(0, 99))
	const halfHour = 1800
	for sec := 60; sec <= 8*halfHour; sec += halfHour {
		h.clock.Set(at(sec))
		h.tr.Apply(weekly(sec, 100))
		h.tr.RecordAPI(request(at(sec+10), s1, "Opus 5.5", 0.5))
		h.tr.Snapshot()
	}
	h.clock.Set(at(8*halfHour + 60))

	got := h.account(dirA).Credits

	if got == nil || got.SpentUSD != 4 || !time.Time(got.OnCreditsSince).Equal(at(60)) || !got.Active {
		t.Errorf("credits %+v after four hours on credits, want $4 spent since %v", got, at(60))
	}
	if week := h.account(dirA).SevenDay; week == nil || week.Projection == nil {
		t.Errorf("weekly window %+v, want its projection still made from the recent samples", week)
	}
}

func TestTracker_CreditsSpanClosesAtTheReset(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	fiveHourResets := at(600)
	h.tr.Apply(withWindows(statusLine(0, s1), window(100, fiveHourResets), window(40, weekResets)))
	h.tr.RecordAPI(request(at(300), s1, "Opus 5.5", 2))
	// Once the window resets, Claude Code stops reporting it until the next
	// call, and the account is back on its plan.
	h.tr.RecordAPI(request(at(700), s1, "Opus 5.5", 4))
	h.clock.Set(at(800))

	v := h.tr.Snapshot()

	want := tracker.CreditsView{BalanceUSD: 20, SpentUSD: 2, RemainingUSD: 18, Days: 30, Source: domain.CreditSourceEstimate}
	if got := h.account(dirA).Credits; got == nil || *got != want {
		t.Errorf("credits %+v, want %+v", got, want)
	}
	if active := alertsOfKind(v, domain.AlertCreditsActive); len(active) != 0 {
		t.Errorf("credits_active alerts %+v after the reset, want none", active)
	}
}

func TestTracker_CreditsActiveAlertIsDatedByItsSpan(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	fiveHourResets := at(90)
	h.tr.Apply(withWindows(statusLine(0, s1), window(100, fiveHourResets), window(90, weekResets)))
	h.tr.RecordAPI(request(at(30), s1, "Opus 5.5", 1))
	h.clock.Set(at(40))
	if active := alertsOfKind(h.tr.Snapshot(), domain.AlertCreditsActive); len(active) != 1 || !active[0].Since.Equal(at(0)) {
		t.Fatalf("credits_active alerts %+v, want one since %v", active, at(0))
	}

	// Off credits at the five-hour reset and back on at the weekly limit
	// between two views: a new stretch on credits, which the page notifies of
	// afresh.
	nextFiveHour := window(2, fiveHourResets.Add(5*time.Hour))
	h.tr.Apply(withWindows(statusLine(100, s1), nextFiveHour, window(95, weekResets)))
	h.tr.Apply(withWindows(statusLine(120, s1), nextFiveHour, window(100, weekResets)))
	h.tr.RecordAPI(request(at(130), s1, "Opus 5.5", 1))
	h.clock.Set(at(140))

	if active := alertsOfKind(h.tr.Snapshot(), domain.AlertCreditsActive); len(active) != 1 || !active[0].Since.Equal(at(120)) {
		t.Errorf("credits_active alerts %+v, want one since %v", active, at(120))
	}
}

func TestTracker_CreditsSurviveARestart(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	spanStart := at(-3 * 3600)
	minute := func(sec int) time.Time { return at(sec).Truncate(time.Minute) }
	h.tr.Restore(tracker.Restored{
		Accounts: []domain.Account{{Dir: dirA, Auth: domain.AuthSubscription}},
		Samples: map[domain.ConfigDir]domain.WindowSamples{dirA: {SevenDay: []domain.Sample{
			{At: spanStart.Add(-time.Minute), UsedPct: 99, ResetsAt: weekResets},
			{At: spanStart, UsedPct: 100, ResetsAt: weekResets},
			{At: at(-60), UsedPct: 100, ResetsAt: weekResets},
		}}},
		Usage: []tracker.UsageRow{
			{Minute: minute(-3*3600 - 120), Dir: dirA, Session: s1, Model: "Opus 5.5", CostUSD: 1, Requests: 1},
			{Minute: minute(-2 * 3600), Dir: dirA, Session: s1, Model: "Opus 5.5", CostUSD: 2.5, Requests: 3},
			{Minute: minute(-120), Dir: dirA, Session: s1, Model: "Opus 5.5", CostUSD: 1.25, Requests: 1},
		},
	})

	want := tracker.CreditsView{
		BalanceUSD: 20, SpentUSD: 3.75, RemainingUSD: 16.25, Days: 30,
		OnCreditsSince: tracker.Timestamp(spanStart), Active: true, Source: domain.CreditSourceEstimate,
	}
	if got := h.account(dirA).Credits; got == nil || *got != want {
		t.Errorf("credits %+v, want %+v", got, want)
	}
}

func TestTracker_ClockSetBackKeepsTheSpendOnCredits(t *testing.T) {
	const hour = 3600
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	h.clock.Set(at(hour))
	h.tr.Apply(withWindows(statusLine(hour, s1), window(100, at(9*hour)), window(40, weekResets)))
	h.tr.RecordAPI(request(at(hour+300), s1, "Opus 5.5", 2))
	h.tr.Apply(withWindows(statusLine(hour+600, s1), window(30, at(9*hour)), window(40, weekResets)))
	h.clock.Set(at(hour + 700))
	h.tr.Snapshot()

	// A time sync sets the clock back by more than an hour.
	h.clock.Set(at(100))

	if got := h.account(dirA).Credits; got == nil || got.SpentUSD != 2 {
		t.Errorf("credits %+v after the clock was set back, want the $2 spent on credits kept", got)
	}
}

func TestTracker_SamplesAWindowAgainWhenItsResetMoves(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(withWindows(statusLine(0, s1), nil, window(100, at(3600))))
	h.tr.Apply(withWindows(statusLine(10, s1), nil, window(100, at(7200))))

	_, _, samples, _ := h.rec.recorded()

	if len(samples) != 2 || !samples[1].limits.SevenDay.ResetsAt.Equal(at(7200)) {
		t.Errorf("samples %+v, want a second one for the new reset time", samples)
	}
}

func TestNew_KeepsItsOwnCopyOfTheGrants(t *testing.T) {
	grants := map[domain.ConfigDir][]domain.CreditGrant{dirA: {{USD: 20, AddedAt: at(-86400)}}}
	h := newSubscribedHarness(t, tracker.Config{Credits: grants}, dirA, dirB)
	grants[dirA][0].USD = 1
	grants[dirB] = []domain.CreditGrant{{USD: 5, AddedAt: at(-86400)}}
	h.tr.Apply(statusLine(0, s1))
	h.tr.Apply(domain.Status{At: at(0), Dir: dirB, Session: s2})

	if got := h.account(dirA).Credits; got == nil || got.BalanceUSD != 20 {
		t.Errorf("credits %+v, want the $20 the tracker was given", got)
	}
	if got := h.account(dirB).Credits; got != nil {
		t.Errorf("credits %+v for an account added to the caller's map later, want none", got)
	}
}

// Usage credits are a subscription's: an account billed otherwise, or whose
// sign-in is not known yet, shows none and raises no credit alerts, whatever
// its config and status lines say.
func TestTracker_CreditsOnlyForSubscriptions(t *testing.T) {
	for _, auth := range []domain.AuthKind{domain.AuthAPIKey, domain.AuthThirdParty, domain.AuthUnknown} {
		t.Run(auth.String(), func(t *testing.T) {
			h := newHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
			h.tr.SetAccount(domain.Account{Dir: dirA, Auth: auth})
			reporting := weekly(0, 100)
			reporting.SpendLimit = &domain.SpendLimit{UsedUSD: 19, LimitUSD: 20}
			h.tr.Apply(reporting)
			h.tr.RecordAPI(request(at(30), s1, "Opus 5.5", 17))

			v := h.tr.Snapshot()

			if got := h.account(dirA).Credits; got != nil {
				t.Errorf("credits %+v, want none for an account that signs in with %v", got, auth)
			}
			for _, kind := range []domain.AlertKind{domain.AlertCreditsActive, domain.AlertCreditsLow} {
				if alerts := alertsOfKind(v, kind); len(alerts) != 0 {
					t.Errorf("%s alerts %+v, want none", kind, alerts)
				}
			}
		})
	}
}

// A span that opened before the thirty days the tracker keeps still holds the
// calls made in them, until the calls themselves age out.
func TestTracker_SpendInASpanOpenedBeforeTheKeptDaysIsCounted(t *testing.T) {
	const day = 86400
	grants := map[domain.ConfigDir][]domain.CreditGrant{dirA: {{USD: 20, AddedAt: at(-40 * day)}}}
	h := newSubscribedHarness(t, tracker.Config{Credits: grants, Alerts: defaultAlerts})
	h.clock.Set(at(-31 * day))
	h.tr.Apply(withWindows(statusLine(-31*day, s1), nil, window(100, at(-24*day))))
	h.clock.Set(at(-29 * day))
	h.tr.RecordAPI(request(at(-29*day), s1, "Opus 5.5", 5))
	h.tr.Snapshot()
	h.clock.Set(at(-23 * day))
	h.tr.Apply(withWindows(statusLine(-23*day, s1), nil, window(40, at(-17*day))))
	h.tr.Snapshot()

	for _, now := range []int{-2 * day, 0, 9 * day / 10} {
		h.clock.Set(at(now))
		if got := h.account(dirA).Credits; got == nil || got.SpentUSD != 5 || got.RemainingUSD != 15 {
			t.Errorf("credits %+v on day %v, want the $5 spent 29 days before day 0 still counted", got, float64(now)/day)
		}
	}
	h.clock.Set(at(day + day/2))
	if got := h.account(dirA).Credits; got == nil || got.SpentUSD != 0 {
		t.Errorf("credits %+v once the call is 30 days old, want it aged out", got)
	}
}

// An account that stays at a limit whose reset it never learns stays on
// credits since the start, however long that is: a later start would make the
// alert look new.
func TestTracker_ALongStretchOnCreditsKeepsItsStart(t *testing.T) {
	const day = 86400
	grants := map[domain.ConfigDir][]domain.CreditGrant{dirA: {{USD: 100, AddedAt: at(-40 * day)}}}
	h := newSubscribedHarness(t, tracker.Config{Credits: grants, Alerts: defaultAlerts})
	for d := -31; d <= 0; d++ {
		h.clock.Set(at(d * day))
		h.tr.Apply(withWindows(statusLine(d*day, s1), nil, window(100, time.Time{})))
		h.tr.RecordAPI(request(at(d*day+10), s1, "Opus 5.5", 1))
		h.tr.Snapshot()
	}
	h.clock.Set(at(60))

	v := h.tr.Snapshot()

	got := h.account(dirA).Credits
	if got == nil || !got.Active || !time.Time(got.OnCreditsSince).Equal(at(-31*day)) || got.SpentUSD != 30 {
		t.Errorf("credits %+v, want $30 spent over the 30 days kept, on credits since %v", got, at(-31*day))
	}
	if active := alertsOfKind(v, domain.AlertCreditsActive); len(active) != 1 || !active[0].Since.Equal(at(-31*day)) {
		t.Errorf("credits_active alerts %+v, want one since %v", active, at(-31*day))
	}
}

// A 5-hour span that runs across the thirty days kept and merges into a 7-day
// span still open now keeps its start, so the merged span's start, which
// dates the credits_active alert, stays put as the days kept move on. It
// changes once, when the 5-hour span has aged out entirely and the open span
// starts where it began itself: that takes a window held at its limit, with
// no known reset, for a month, and the page notifies of it once more.
func TestTracker_OnCreditsSinceHoldsWhileAMergedSpanAgesOut(t *testing.T) {
	const day, hour = 86400, 3600
	grants := map[domain.ConfigDir][]domain.CreditGrant{dirA: {{USD: 100, AddedAt: at(-40 * day)}}}
	h := newSubscribedHarness(t, tracker.Config{Credits: grants, Alerts: defaultAlerts})
	start := -31 * day
	fiveHourResets := at(start + 2*hour)
	h.clock.Set(at(start))
	h.tr.Apply(withWindows(statusLine(start, s1), window(100, fiveHourResets), window(50, at(start+3*day))))
	h.clock.Set(at(start + hour))
	h.tr.Apply(withWindows(statusLine(start+hour, s1), window(100, fiveHourResets), window(100, time.Time{})))
	h.tr.RecordAPI(request(at(start+hour+10), s1, "Opus 5.5", 1))
	h.clock.Set(at(start + 2*hour))
	h.tr.Apply(withWindows(statusLine(start+2*hour, s1), window(30, fiveHourResets.Add(5*time.Hour)), window(100, time.Time{})))
	h.tr.Snapshot()
	for d := start + day; d <= start+30*day-2*hour; d += day {
		h.clock.Set(at(d))
		h.tr.Apply(withWindows(statusLine(d, s1), nil, window(100, time.Time{})))
		h.tr.RecordAPI(request(at(d+10), s1, "Opus 5.5", 1))
		h.tr.Snapshot()
	}

	for _, tc := range []struct{ now, since int }{
		{start + 30*day - hour, start},
		{start + 30*day + 15*60, start},
		{start + 30*day + 30*60, start},
		{start + 30*day + 45*60, start},
		{start + 30*day + 90*60, start},
		{start + 30*day + 3*hour, start + hour},
	} {
		h.clock.Set(at(tc.now))
		v := h.tr.Snapshot()
		credits := h.account(dirA).Credits
		if credits == nil || !credits.Active || !time.Time(credits.OnCreditsSince).Equal(at(tc.since)) {
			t.Errorf("credits %+v at day %+.4f, want on credits since day %+.4f", credits, float64(tc.now)/day, float64(tc.since)/day)
		}
		if active := alertsOfKind(v, domain.AlertCreditsActive); len(active) != 1 || !active[0].Since.Equal(at(tc.since)) {
			t.Errorf("credits_active alerts %+v at day %+.4f, want one since day %+.4f", active, float64(tc.now)/day, float64(tc.since)/day)
		}
	}
}

// Credit estimates count no further back than the history keeps, at most
// thirty days, so a tracker restarted from it counts what one that ran on
// does; the view says how many days that is.
func TestTracker_CreditsCountTheDaysTheHistoryKeeps(t *testing.T) {
	const day = 86400
	for name, tc := range map[string]struct {
		creditDays, wantDays int
		wantSpentUSD         float64
	}{
		"a week of history":         {creditDays: 7, wantDays: 7, wantSpentUSD: 7},
		"a day of history":          {creditDays: 1, wantDays: 1, wantSpentUSD: 1},
		"unset, thirty days":        {creditDays: 0, wantDays: 30, wantSpentUSD: 30},
		"more history, thirty days": {creditDays: 45, wantDays: 30, wantSpentUSD: 30},
	} {
		t.Run(name, func(t *testing.T) {
			grants := map[domain.ConfigDir][]domain.CreditGrant{dirA: {{USD: 100, AddedAt: at(-40 * day)}}}
			h := newSubscribedHarness(t, tracker.Config{Credits: grants, CreditDays: tc.creditDays, Alerts: defaultAlerts})
			for d := -31; d <= 0; d++ {
				h.clock.Set(at(d * day))
				h.tr.Apply(withWindows(statusLine(d*day, s1), nil, window(100, time.Time{})))
				h.tr.RecordAPI(request(at(d*day+10), s1, "Opus 5.5", 1))
				h.tr.Snapshot()
			}
			h.clock.Set(at(60))

			got := h.account(dirA).Credits

			if got == nil || got.Days != tc.wantDays || got.SpentUSD != tc.wantSpentUSD || !time.Time(got.OnCreditsSince).Equal(at(-31*day)) {
				t.Errorf("credits %+v, want $%v spent over the last %d days, on credits since %v", got, tc.wantSpentUSD, tc.wantDays, at(-31*day))
			}
		})
	}
}

func TestTracker_ReportedSpendLimitExpiresAfterAnHour(t *testing.T) {
	for name, tc := range map[string]struct {
		grants   map[domain.ConfigDir][]domain.CreditGrant
		afterAll *tracker.CreditsView
	}{
		"with grants, back to the estimate": {
			grants:   twentyDollars,
			afterAll: &tracker.CreditsView{BalanceUSD: 20, SpentUSD: 2, RemainingUSD: 18, Days: 30, Source: domain.CreditSourceEstimate},
		},
		"without grants, gone": {},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSubscribedHarness(t, tracker.Config{Credits: tc.grants, Alerts: defaultAlerts})
			resets := at(50)
			reporting := withWindows(statusLine(0, s1), nil, window(100, resets))
			reporting.SpendLimit = &domain.SpendLimit{UsedUSD: 12.5, LimitUSD: 50}
			h.tr.Apply(reporting)
			h.tr.RecordAPI(request(at(30), s1, "Opus 5.5", 2))
			// A later line, of the window begun at the reset, that reports no
			// spend limit.
			h.tr.Apply(withWindows(statusLine(60, s1), nil, window(40, resets.Add(7*24*time.Hour))))

			h.clock.Set(at(3599))
			if got := h.account(dirA).Credits; got == nil || got.Source != domain.CreditSourceReported {
				t.Fatalf("credits %+v just under an hour after the report, want the reported figures", got)
			}
			h.clock.Set(at(3600))
			got := h.account(dirA).Credits
			if (got == nil) != (tc.afterAll == nil) || (got != nil && *got != *tc.afterAll) {
				t.Errorf("credits %+v an hour after the report, want %+v", got, tc.afterAll)
			}
		})
	}
}

// Two sessions of one account: s1 works on credits, its status line reporting
// the weekly window at 100%, while s2 sits idle and, refreshed every 5 s, goes
// on reporting the 97% its own last response carried. Both readings have the
// same reset, so they describe one window, in which usage never goes down:
// the 97% is older news, not a drop below the limit. The account is at its
// limit throughout, so every call after the first 100% reading is paid for
// with credits, and the stretch on credits has a single start.
func TestTracker_AnIdleSessionsOlderReadingKeepsTheStretchOnCredits(t *testing.T) {
	grants := map[domain.ConfigDir][]domain.CreditGrant{dirA: {{USD: 1000, AddedAt: at(-86400)}}}
	h := newSubscribedHarness(t, tracker.Config{Credits: grants, Alerts: defaultAlerts})

	const minutes = 10
	spent := 0.0
	sinces := map[int64]bool{}
	activeViews, views, weeklyBelowLimit := 0, 0, 0
	look := func(sec int) {
		h.clock.Set(at(sec))
		v := h.tr.Snapshot()
		views++
		if active := alertsOfKind(v, domain.AlertCreditsActive); len(active) == 1 {
			activeViews++
			sinces[active[0].Since.UnixMilli()] = true
		}
		for _, a := range v.Accounts {
			if a.Dir == dirA && (a.SevenDay == nil || a.SevenDay.UsedPct != 100) {
				weeklyBelowLimit++
			}
		}
	}
	for sec := 0; sec < minutes*60; sec += 5 {
		h.clock.Set(at(sec))
		h.tr.Apply(withWindows(statusLine(sec, s1), nil, window(100, weekResets)))
		h.tr.RecordAPI(request(at(sec+1), s1, "Opus 5.5", 0.10))
		look(sec + 1)
		h.clock.Set(at(sec + 2))
		h.tr.Apply(withWindows(statusLine(sec+2, s2), nil, window(97, weekResets)))
		h.tr.RecordAPI(request(at(sec+3), s1, "Opus 5.5", 0.10))
		spent += 0.20
		look(sec + 4)
	}
	h.clock.Set(at(minutes * 60))

	if got := h.account(dirA).Credits; got == nil || !near(got.SpentUSD, spent) {
		t.Errorf("credits %+v, want $%.2f spent: every call after the first 100%% reading was made at the limit", got, spent)
	}
	if activeViews != views {
		t.Errorf("credits_active shown in %d of %d views, want all: the account never left its limit", activeViews, views)
	}
	if len(sinces) != 1 {
		t.Errorf("credits_active came with %d different since times over %d minutes, want 1: the page notifies afresh for each new since", len(sinces), minutes)
	}
	if weeklyBelowLimit != 0 {
		t.Errorf("the card showed the weekly window below 100%% in %d of %d views, want none: 100%% is the newest usage of the window", weeklyBelowLimit, views)
	}
}

// A day of the same disagreement leaves the view as cheap as ever: the tracker
// holds its lock while it builds one, so a slow view stalls ingest. A view's
// cost is held to one that grows linearly with the hours of readings, which
// costtest checks on any machine; BenchmarkTracker_ViewAfterADayOfIdleReadings
// measures a view after the day.
func TestTracker_ADayOfAnIdleSessionsReadingsKeepsViewsCheap(t *testing.T) {
	after := func(hours int) *tracker.Tracker {
		h := newSubscribedHarness(t, tracker.Config{Credits: aThousandDollars, Alerts: defaultAlerts})
		disagreeFor(h.tr, h.clock, hours)
		return h.tr
	}
	// Six hours of readings against a day of them show a quadratic cost
	// plainly. The race detector slows building them several times over, and
	// a loaded machine as much again, so under it two hours against eight,
	// which build in seconds where a day of a quadratic cost takes minutes.
	// On the machine these sizes were set on, a day's view takes 3.4 to 3.7
	// times as long as six hours' (3.1 to 3.7 at two hours against eight),
	// more than twice short of costtest's limit of 8, while a quadratic cost
	// comes out at 10 or more.
	hours := 6
	if raceEnabled {
		hours = 2
	}

	costtest.AssertLinear(t, hours, "hours of readings", func(hours int) func() { return viewing(after(hours)) })
}

// A month of stretches on credits half a minute long, one every minute, each
// with a call in it: a view walks the calls and the spans together, where
// looking through the spans for each call would take seconds under the lock.
// A view's cost is held to one that grows linearly with the days of
// stretches, which costtest checks on any machine;
// BenchmarkTracker_ViewOverManySpansAndCalls measures a view of the month.
func TestTracker_AViewTakesOnePassOverTheSpansAndCalls(t *testing.T) {
	over := func(days int) *tracker.Tracker {
		h := newSubscribedHarness(t, tracker.Config{Credits: aBillionDollars, Alerts: defaultAlerts})
		stretches := restoreStretchesOnCredits(h.tr, days)
		if got := h.account(dirA).Credits; got == nil || got.SpentUSD != float64(stretches) || got.Active {
			t.Errorf("credits %+v over %d days, want $%d spent, one for each stretch on credits, none open", got, days, stretches)
		}
		return h.tr
	}
	// Four days against sixteen show a quadratic cost plainly, in seconds
	// where a month of a quadratic cost takes minutes. Under the race
	// detector, which slows the views several times over, one day against
	// four.
	days := 4
	if raceEnabled {
		days = 1
	}

	costtest.AssertLinear(t, days, "days of stretches on credits", func(days int) func() { return viewing(over(days)) })
	// The month itself, untimed, for its credits at full scale. A cost found
	// to grow faster than linearly would take minutes over it, so it is
	// built only once the cost is known to be linear.
	if !t.Failed() {
		over(30)
	}
}

func TestTracker_ADroppedWindowClosesASpanWithNoKnownReset(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	h.tr.Apply(withWindows(statusLine(0, s1), window(100, time.Time{}), window(40, weekResets)))
	h.tr.RecordAPI(request(at(30), s1, "Opus 5.5", 2))
	// Claude Code drops a window from the line once it resets.
	h.tr.Apply(withWindows(statusLine(60, s1), nil, window(41, weekResets)))
	h.tr.RecordAPI(request(at(90), s1, "Opus 5.5", 4))
	h.clock.Set(at(100))

	want := tracker.CreditsView{BalanceUSD: 20, SpentUSD: 2, RemainingUSD: 18, Days: 30, Source: domain.CreditSourceEstimate}
	if got := h.account(dirA).Credits; got == nil || *got != want {
		t.Errorf("credits %+v, want %+v", got, want)
	}
	_, _, samples, _ := h.rec.recorded()
	last := samples[len(samples)-1]
	if !last.limits.ObservedAt.Equal(at(60)) || last.limits.FiveHour == nil || last.limits.FiveHour.UsedPct != 0 {
		t.Errorf("last sample recorded %+v, want the 5-hour window back at 0%% when the line dropped it, so a restart keeps the span closed", last)
	}
}

// busyAccount plays the sessions of account A, sending the signals that date
// the readings their status lines carry: the process's API time on each line,
// when apiTime, and each call, with how long it took, when calls.
type busyAccount struct {
	h              *harness
	apiTime, calls bool
}

// line applies a status line of id at sec that reports the weekly window at
// pct, and the process's API time as apiMS, and returns the weekly usage
// shown after it.
func (b busyAccount) line(sec int, id domain.SessionID, pct float64, apiMS int64) float64 {
	st := weeklyFrom(sec, id, pct)
	if b.apiTime {
		st = withAPITime(st, apiMS)
	}
	return shownWeekly(b.h, st)
}

// call records a call of id that ended at sec, costing costUSD, after
// durationMS.
func (b busyAccount) call(sec int, id domain.SessionID, costUSD float64, durationMS int64) {
	if !b.calls {
		return
	}
	r := request(at(sec), id, "Opus 5.5", costUSD)
	r.DurationMS = durationMS
	b.h.tr.RecordAPI(r)
}

// crossTheLimit plays two busy sessions of account A up to its weekly limit:
// long's line at 0 s, its API time at longAPIMS, and short's at 2 s report
// 98%, and short's calls report 100% from its line at 62 s. Meanwhile long
// begins a call at 5 s, whose response headers report 98%.
func (b busyAccount) crossTheLimit(long, short domain.SessionID, longAPIMS int64) {
	b.line(0, long, 98, longAPIMS)
	b.line(2, short, 98, 4_000)
	b.call(60, short, 1, 2_000)
	b.line(62, short, 100, 6_000)
	b.call(120, short, 2, 2_000)
	b.line(122, short, 100, 8_000)
}

// onCreditsSpans returns account A's spans on credits as of sec, from the
// weekly samples recorded.
func onCreditsSpans(h *harness, sec int) []domain.OnCreditsSpan {
	return domain.OnCreditsSpans(domain.WindowSamples{SevenDay: recordedWeeklySamples(h)}, at(sec))
}

// Two sessions of one account are busy as it crosses its weekly limit. A
// status line carries the rate limits that came with the response headers of
// its process's newest call, as the call began, but only once the call has
// ended, when the line's API time grows by the whole call. long's four-minute
// call began at 98%, and its line at 246 s still reports that 98%, after
// short's calls have reported 100% since 62 s. The reading is older than the
// crossing, so the stretch on credits that began there goes on: one span, one
// since, and every call made in it counted.
func TestTracker_TwoBusySessionsCrossingTheLimitKeepOneSpan(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: aThousandDollars, Alerts: defaultAlerts})
	account := busyAccount{h: h, apiTime: true, calls: true}
	long, short := s1, s2
	sinces := map[int64]bool{}
	shownBelowLimit := 0
	look := func() {
		if pct := pctOf(h.account(dirA).SevenDay); pct != 100 {
			shownBelowLimit++
		}
		for _, a := range alertsOfKind(h.tr.Snapshot(), domain.AlertCreditsActive) {
			sinces[a.Since.UnixMilli()] = true
		}
	}

	account.crossTheLimit(long, short, 10_000)
	look()
	account.call(245, long, 4, 240_000)
	account.line(246, long, 98, 250_000)
	look()
	account.call(300, short, 8, 2_000)
	account.line(302, short, 100, 10_000)
	look()
	account.call(360, short, 16, 2_000)
	account.line(362, short, 100, 12_000)
	look()
	h.clock.Set(at(370))

	if spans := onCreditsSpans(h, 370); len(spans) != 1 || !spans[0].From.Equal(at(62)) || !spans[0].To.IsZero() {
		t.Errorf("spans %v of the samples recorded, want one open since %v", spans, at(62))
	}
	want := tracker.CreditsView{
		BalanceUSD: 1000, SpentUSD: 30, RemainingUSD: 970, Days: 30,
		OnCreditsSince: tracker.Timestamp(at(62)), Active: true, Source: domain.CreditSourceEstimate,
	}
	if got := h.account(dirA).Credits; got == nil || *got != want {
		t.Errorf("credits %+v, want %+v: every call after the crossing was made at the limit", got, want)
	}
	if len(sinces) != 1 {
		t.Errorf("credits_active came with %d different since times, want 1: the page notifies afresh for each new since", len(sinces))
	}
	if shownBelowLimit != 0 {
		t.Errorf("the card showed the weekly window below 100%% in %d views after the crossing, want none", shownBelowLimit)
	}
}

// The same crossing, from a Claude Code that sends only one of the signals
// that date a reading: the line's API time, when its calls are not exported,
// or the call's duration, when its lines report no API time. Either alone
// dates long's 98% to before the crossing.
func TestTracker_EitherSignalAloneDatesALongCallsReadingAtItsStart(t *testing.T) {
	for name, account := range map[string]busyAccount{
		"the line's API time": {apiTime: true},
		"the call's duration": {calls: true},
	} {
		t.Run(name, func(t *testing.T) {
			account.h = newHarness(t, tracker.Config{})
			long, short := s1, s2
			account.crossTheLimit(long, short, 10_000)
			account.call(245, long, 4, 240_000)

			if got := account.line(246, long, 98, 250_000); got != 100 {
				t.Errorf("shown %v%% after long's line, want 100%%: its 98%% was heard before the crossing", got)
			}
			account.call(300, short, 8, 2_000)
			account.line(302, short, 100, 10_000)
			if spans := onCreditsSpans(account.h, 310); len(spans) != 1 || !spans[0].From.Equal(at(62)) || !spans[0].To.IsZero() {
				t.Errorf("spans %v of the samples recorded, want one open since %v", spans, at(62))
			}
		})
	}
}

// The crossing again, but long's last call is a short one that began at 244 s,
// after the crossing, and its line reports 0%: the owner's mid-week reset,
// which kept its reset time. The reading was taken after the crossing, so it
// ends the stretch on credits, from either signal or both, however much API
// time long had counted before.
func TestTracker_AResetHeardAfterTheCrossingEndsTheSpan(t *testing.T) {
	const anHourMS = 3_600_000
	for name, account := range map[string]busyAccount{
		"the line's API time and the call's duration": {apiTime: true, calls: true},
		"the line's API time":                         {apiTime: true},
		"the call's duration":                         {calls: true},
	} {
		t.Run(name, func(t *testing.T) {
			account.h = newHarness(t, tracker.Config{})
			long, short := s1, s2
			account.crossTheLimit(long, short, anHourMS)
			account.call(245, long, 4, 1_000)

			if got := account.line(246, long, 0, anHourMS+2_000); got != 0 {
				t.Errorf("shown %v%% after long's line, want the 0%% it heard after the crossing", got)
			}
			if spans := onCreditsSpans(account.h, 250); len(spans) != 1 || !spans[0].From.Equal(at(62)) || !spans[0].To.Equal(at(246)) {
				t.Errorf("spans %v of the samples recorded, want one from %v to %v", spans, at(62), at(246))
			}
		})
	}
}
