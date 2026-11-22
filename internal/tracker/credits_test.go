package tracker_test

import (
	"testing"
	"time"

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
// holds its lock while it builds one, so a slow view stalls ingest.
func TestTracker_ADayOfAnIdleSessionsReadingsKeepsViewsCheap(t *testing.T) {
	grants := map[domain.ConfigDir][]domain.CreditGrant{dirA: {{USD: 1000, AddedAt: at(-86400)}}}
	h := newSubscribedHarness(t, tracker.Config{Credits: grants, Alerts: defaultAlerts})
	const hours = 24
	for sec := 0; sec < hours*3600; sec += 5 {
		h.clock.Set(at(sec))
		h.tr.Apply(withWindows(statusLine(sec, s1), nil, window(100, weekResets)))
		h.tr.RecordAPI(request(at(sec+1), s1, "Opus 5.5", 0.01))
		h.clock.Set(at(sec + 2))
		h.tr.Apply(withWindows(statusLine(sec+2, s2), nil, window(97, weekResets)))
		h.tr.RecordAPI(request(at(sec+3), s1, "Opus 5.5", 0.01))
		if sec%3600 == 0 {
			h.tr.Snapshot()
		}
	}
	h.clock.Set(at(hours*3600 + 1))
	for range 3 {
		start := time.Now()
		h.tr.Snapshot()
		if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
			t.Errorf("a view after %d hours took %v, want well under 100 ms", hours, elapsed)
		}
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
