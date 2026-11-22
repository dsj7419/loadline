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
		BalanceUSD: 20, SpentUSD: 6.25, RemainingUSD: 13.75,
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
		BalanceUSD: 500, SpentUSD: 314.25, RemainingUSD: 185.75,
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

	want := tracker.CreditsView{BalanceUSD: 50, SpentUSD: 12.5, RemainingUSD: 37.5, Source: domain.CreditSourceReported}
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

	want := tracker.CreditsView{BalanceUSD: 20, SpentUSD: 2, RemainingUSD: 18, Source: domain.CreditSourceEstimate}
	if got := h.account(dirA).Credits; got == nil || *got != want {
		t.Errorf("credits %+v, want %+v", got, want)
	}
	if active := alertsOfKind(v, domain.AlertCreditsActive); len(active) != 0 {
		t.Errorf("credits_active alerts %+v after the reset, want none", active)
	}
}

func TestTracker_CreditsActiveAlertIsDatedByItsSpan(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	h.tr.Apply(withWindows(statusLine(0, s1), window(100, at(20_000)), window(90, weekResets)))
	h.tr.RecordAPI(request(at(30), s1, "Opus 5.5", 1))
	h.clock.Set(at(40))
	if active := alertsOfKind(h.tr.Snapshot(), domain.AlertCreditsActive); len(active) != 1 || !active[0].Since.Equal(at(0)) {
		t.Fatalf("credits_active alerts %+v, want one since %v", active, at(0))
	}

	// Off credits and back on between two views: a new stretch on credits,
	// which the page notifies of afresh.
	h.tr.Apply(withWindows(statusLine(100, s1), window(98, at(20_000)), window(95, weekResets)))
	h.tr.Apply(withWindows(statusLine(120, s1), window(98, at(20_000)), window(100, weekResets)))
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
		BalanceUSD: 20, SpentUSD: 3.75, RemainingUSD: 16.25,
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

func TestTracker_ReportedSpendLimitExpiresAfterAnHour(t *testing.T) {
	for name, tc := range map[string]struct {
		grants   map[domain.ConfigDir][]domain.CreditGrant
		afterAll *tracker.CreditsView
	}{
		"with grants, back to the estimate": {
			grants:   twentyDollars,
			afterAll: &tracker.CreditsView{BalanceUSD: 20, SpentUSD: 2, RemainingUSD: 18, Source: domain.CreditSourceEstimate},
		},
		"without grants, gone": {},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSubscribedHarness(t, tracker.Config{Credits: tc.grants, Alerts: defaultAlerts})
			reporting := weekly(0, 100)
			reporting.SpendLimit = &domain.SpendLimit{UsedUSD: 12.5, LimitUSD: 50}
			h.tr.Apply(reporting)
			h.tr.RecordAPI(request(at(30), s1, "Opus 5.5", 2))
			// A later line, not at the limit, that reports no spend limit.
			h.tr.Apply(weekly(60, 40))

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

func TestTracker_ADroppedWindowClosesASpanWithNoKnownReset(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	h.tr.Apply(withWindows(statusLine(0, s1), window(100, time.Time{}), window(40, weekResets)))
	h.tr.RecordAPI(request(at(30), s1, "Opus 5.5", 2))
	// Claude Code drops a window from the line once it resets.
	h.tr.Apply(withWindows(statusLine(60, s1), nil, window(41, weekResets)))
	h.tr.RecordAPI(request(at(90), s1, "Opus 5.5", 4))
	h.clock.Set(at(100))

	want := tracker.CreditsView{BalanceUSD: 20, SpentUSD: 2, RemainingUSD: 18, Source: domain.CreditSourceEstimate}
	if got := h.account(dirA).Credits; got == nil || *got != want {
		t.Errorf("credits %+v, want %+v", got, want)
	}
	_, _, samples, _ := h.rec.recorded()
	last := samples[len(samples)-1]
	if !last.limits.ObservedAt.Equal(at(60)) || last.limits.FiveHour == nil || last.limits.FiveHour.UsedPct != 0 {
		t.Errorf("last sample recorded %+v, want the 5-hour window back at 0%% when the line dropped it, so a restart keeps the span closed", last)
	}
}
