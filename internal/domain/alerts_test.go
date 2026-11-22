package domain_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

// The alert tests evaluate at alertNow, with the default thresholds.
var alertNow = at(1000)

var defaultAlertConfig = domain.AlertConfig{WeeklyPct: 90, ContextPct: 85, NeedsYouAfter: 20 * time.Second}

// accountWithWeekly is an account with the given label whose seven-day window
// is usedPct used.
func accountWithWeekly(dir domain.ConfigDir, label string, usedPct float64) domain.Account {
	return domain.Account{
		Dir:    dir,
		Label:  label,
		Limits: domain.Limits{SevenDay: &domain.Window{UsedPct: usedPct, ResetsAt: alertNow.Add(48 * time.Hour)}},
	}
}

// alertSession is a session in state, which it entered at sinceSec.
func alertSession(id domain.SessionID, dir domain.ConfigDir, state domain.State, sinceSec int) domain.Session {
	s := domain.NewSession(id, dir, t0)
	s.State, s.StateAt = state, at(sinceSec)
	return s
}

func withContext(s domain.Session, pct float64) domain.Session {
	s.Metrics.ContextPct = pct
	return s
}

// alertKey is what the order and tie-break tests compare: an alert without its
// message, with Since as an offset from t0 so the keys compare with ==.
type alertKey struct {
	Kind    domain.AlertKind
	Dir     domain.ConfigDir
	Session domain.SessionID
	Since   time.Duration
}

func keysOf(alerts []domain.Alert) []alertKey {
	keys := make([]alertKey, len(alerts))
	for i, a := range alerts {
		keys[i] = alertKey{a.Kind, a.Dir, a.Session, a.Since.Sub(t0)}
	}
	return keys
}

func offset(sec int) time.Duration { return time.Duration(sec) * time.Second }

func kindsOf(alerts []domain.Alert) []domain.AlertKind {
	kinds := make([]domain.AlertKind, len(alerts))
	for i, a := range alerts {
		kinds[i] = a.Kind
	}
	return kinds
}

func evaluate(in domain.AlertInput) []domain.Alert {
	return domain.Evaluate(in, defaultAlertConfig, alertNow)
}

func TestEvaluate_NothingToReportIsQuiet(t *testing.T) {
	if got := evaluate(domain.AlertInput{}); len(got) != 0 {
		t.Errorf("Evaluate(zero input) = %v, want no alerts", got)
	}
}

func TestEvaluate_WeeklyThreshold(t *testing.T) {
	fiveHourOnly := domain.Account{
		Dir:    dirA,
		Limits: domain.Limits{FiveHour: &domain.Window{UsedPct: 99}},
	}
	for name, tc := range map[string]struct {
		account domain.Account
		want    bool
	}{
		"just under":                  {accountWithWeekly(dirA, "Main", 89.9), false},
		"at the threshold, inclusive": {accountWithWeekly(dirA, "Main", 90), true},
		"over":                        {accountWithWeekly(dirA, "Main", 100), true},
		"no weekly window reported":   {fiveHourOnly, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := evaluate(domain.AlertInput{Accounts: []domain.Account{tc.account}})

			if !tc.want {
				if len(got) != 0 {
					t.Errorf("Evaluate() = %v, want no alerts", got)
				}
				return
			}
			want := []alertKey{{domain.AlertWeeklyThreshold, dirA, "", alertNow.Sub(t0)}}
			if !reflect.DeepEqual(keysOf(got), want) {
				t.Errorf("Evaluate() = %v, want %v", keysOf(got), want)
			}
		})
	}
}

func TestEvaluate_WeeklyThresholdFollowsTheConfig(t *testing.T) {
	accounts := []domain.Account{accountWithWeekly(dirA, "Main", 75)}
	cfg := domain.AlertConfig{WeeklyPct: 75, ContextPct: 85, NeedsYouAfter: 20 * time.Second}

	got := domain.Evaluate(domain.AlertInput{Accounts: accounts}, cfg, alertNow)

	if !reflect.DeepEqual(kindsOf(got), []domain.AlertKind{domain.AlertWeeklyThreshold}) {
		t.Errorf("Evaluate() = %v, want a weekly_threshold alert at 75%%", got)
	}
}

func runsOut(eta time.Time) domain.Projection {
	return domain.Projection{Known: true, RatePerHour: 5, AtReset: 100, ETA: eta, RunsOutBeforeReset: true}
}

func TestEvaluate_RunsOut(t *testing.T) {
	onPace := domain.Projection{Known: true, RatePerHour: 1, AtReset: 60, ETA: alertNow.Add(72 * time.Hour)}
	unknownButFlagged := domain.Projection{RunsOutBeforeReset: true}
	for name, tc := range map[string]struct {
		projections map[domain.ConfigDir]domain.WindowProjections
		want        bool
	}{
		"seven-day runs out": {
			map[domain.ConfigDir]domain.WindowProjections{dirA: {SevenDay: runsOut(alertNow.Add(time.Hour))}},
			true,
		},
		"five-hour runs out": {
			map[domain.ConfigDir]domain.WindowProjections{dirA: {FiveHour: runsOut(alertNow.Add(time.Hour))}},
			true,
		},
		"on pace": {
			map[domain.ConfigDir]domain.WindowProjections{dirA: {SevenDay: onPace, FiveHour: onPace}},
			false,
		},
		"an unknown projection counts for nothing": {
			map[domain.ConfigDir]domain.WindowProjections{dirA: {SevenDay: unknownButFlagged}},
			false,
		},
		"no projection for the account": {
			map[domain.ConfigDir]domain.WindowProjections{dirB: {SevenDay: runsOut(alertNow.Add(time.Hour))}},
			false,
		},
		"no projections at all": {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := evaluate(domain.AlertInput{
				Accounts:    []domain.Account{{Dir: dirA, Label: "Main"}},
				Projections: tc.projections,
			})

			if !tc.want {
				if len(got) != 0 {
					t.Errorf("Evaluate() = %v, want no alerts", got)
				}
				return
			}
			want := []alertKey{{domain.AlertRunsOut, dirA, "", alertNow.Sub(t0)}}
			if !reflect.DeepEqual(keysOf(got), want) {
				t.Errorf("Evaluate() = %v, want %v", keysOf(got), want)
			}
		})
	}
}

func TestEvaluate_RunsOutFiresOncePerAccountForTheEarliestWindow(t *testing.T) {
	early, late := alertNow.Add(time.Hour), alertNow.Add(5*time.Hour)
	for name, tc := range map[string]struct {
		projection  domain.WindowProjections
		wantInAlert string
	}{
		"five-hour first":         {domain.WindowProjections{FiveHour: runsOut(early), SevenDay: runsOut(late)}, "5-hour"},
		"seven-day first":         {domain.WindowProjections{FiveHour: runsOut(late), SevenDay: runsOut(early)}, "weekly"},
		"a tie goes to seven-day": {domain.WindowProjections{FiveHour: runsOut(early), SevenDay: runsOut(early)}, "weekly"},
	} {
		t.Run(name, func(t *testing.T) {
			got := evaluate(domain.AlertInput{
				Accounts:    []domain.Account{{Dir: dirA, Label: "Main"}},
				Projections: map[domain.ConfigDir]domain.WindowProjections{dirA: tc.projection},
			})

			if len(got) != 1 {
				t.Fatalf("Evaluate() = %v, want exactly one runs_out alert", got)
			}
			if !strings.Contains(got[0].Message, tc.wantInAlert) {
				t.Errorf("Message = %q, want it to name the %s limit", got[0].Message, tc.wantInAlert)
			}
		})
	}
}

func TestEvaluate_RunsOutIgnoresAWindowThatIsSafe(t *testing.T) {
	onPace := domain.Projection{Known: true, RatePerHour: 1, AtReset: 60, ETA: alertNow.Add(72 * time.Hour)}
	got := evaluate(domain.AlertInput{
		Accounts: []domain.Account{{Dir: dirA, Label: "Main"}},
		Projections: map[domain.ConfigDir]domain.WindowProjections{
			// The five-hour window is on pace; only the seven-day one is at risk.
			dirA: {FiveHour: onPace, SevenDay: runsOut(alertNow.Add(6 * time.Hour))},
		},
	})

	if len(got) != 1 || !strings.Contains(got[0].Message, "weekly") {
		t.Errorf("Evaluate() = %v, want one alert naming the weekly limit", got)
	}
}

func TestEvaluate_NeedsYouAfter20s(t *testing.T) {
	for name, tc := range map[string]struct {
		session domain.Session
		want    bool
	}{
		"19 seconds":                  {alertSession(sid, dirA, domain.StateNeedsYou, 1000-19), false},
		"20 seconds":                  {alertSession(sid, dirA, domain.StateNeedsYou, 1000-20), true},
		"minutes":                     {alertSession(sid, dirA, domain.StateNeedsYou, 1000-600), true},
		"just entered":                {alertSession(sid, dirA, domain.StateNeedsYou, 1000), false},
		"a clock ahead of the events": {alertSession(sid, dirA, domain.StateNeedsYou, 1000+5), false},
		"working":                     {alertSession(sid, dirA, domain.StateWorking, 1000-600), false},
		"idle":                        {alertSession(sid, dirA, domain.StateIdle, 1000-600), false},
		"error is not a request":      {alertSession(sid, dirA, domain.StateError, 1000-600), false},
	} {
		t.Run(name, func(t *testing.T) {
			got := evaluate(domain.AlertInput{Sessions: []domain.Session{tc.session}})

			if !tc.want {
				if len(got) != 0 {
					t.Errorf("Evaluate() = %v, want no alerts", got)
				}
				return
			}
			want := []alertKey{{domain.AlertNeedsYou, dirA, sid, offset(int(tc.session.StateAt.Sub(t0).Seconds()))}}
			if !reflect.DeepEqual(keysOf(got), want) {
				t.Errorf("Evaluate() = %v, want %v: Since is when the session started waiting", keysOf(got), want)
			}
		})
	}
}

func TestEvaluate_ContextHigh(t *testing.T) {
	for name, tc := range map[string]struct {
		session domain.Session
		want    bool
	}{
		"just under":                  {withContext(alertSession(sid, dirA, domain.StateWorking, 0), 84.9), false},
		"at the threshold, inclusive": {withContext(alertSession(sid, dirA, domain.StateWorking, 0), 85), true},
		"over, while idle":            {withContext(alertSession(sid, dirA, domain.StateIdle, 0), 97), true},
		"over, while compacting":      {withContext(alertSession(sid, dirA, domain.StateCompacting, 0), 97), true},
		"over, with an error":         {withContext(alertSession(sid, dirA, domain.StateError, 0), 97), true},
	} {
		t.Run(name, func(t *testing.T) {
			got := evaluate(domain.AlertInput{Sessions: []domain.Session{tc.session}})

			if !tc.want {
				if len(got) != 0 {
					t.Errorf("Evaluate() = %v, want no alerts", got)
				}
				return
			}
			want := []alertKey{{domain.AlertContextHigh, dirA, sid, alertNow.Sub(t0)}}
			if !reflect.DeepEqual(keysOf(got), want) {
				t.Errorf("Evaluate() = %v, want %v", keysOf(got), want)
			}
		})
	}
}

func TestEvaluate_Budget(t *testing.T) {
	for name, tc := range map[string]struct {
		spendToday, budget map[domain.ConfigDir]float64
		want               bool
	}{
		"under":              {map[domain.ConfigDir]float64{dirA: 9.99}, map[domain.ConfigDir]float64{dirA: 10}, false},
		"exactly the budget": {map[domain.ConfigDir]float64{dirA: 10}, map[domain.ConfigDir]float64{dirA: 10}, true},
		"over":               {map[domain.ConfigDir]float64{dirA: 25}, map[domain.ConfigDir]float64{dirA: 10}, true},
		"no budget set":      {map[domain.ConfigDir]float64{dirA: 25}, map[domain.ConfigDir]float64{dirA: 0}, false},
		"no budget entry":    {map[domain.ConfigDir]float64{dirA: 25}, nil, false},
		"a negative budget":  {map[domain.ConfigDir]float64{dirA: 25}, map[domain.ConfigDir]float64{dirA: -1}, false},
		"no spend recorded":  {nil, map[domain.ConfigDir]float64{dirA: 10}, false},
		"another account's":  {map[domain.ConfigDir]float64{dirB: 25}, map[domain.ConfigDir]float64{dirA: 10}, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := evaluate(domain.AlertInput{
				Accounts:   []domain.Account{{Dir: dirA, Label: "API"}},
				SpendToday: tc.spendToday,
				Budgets:    tc.budget,
			})

			if !tc.want {
				if len(got) != 0 {
					t.Errorf("Evaluate() = %v, want no alerts", got)
				}
				return
			}
			want := []alertKey{{domain.AlertBudget, dirA, "", alertNow.Sub(t0)}}
			if !reflect.DeepEqual(keysOf(got), want) {
				t.Errorf("Evaluate() = %v, want %v", keysOf(got), want)
			}
		})
	}
}

func TestEvaluate_CreditsActiveAndLow(t *testing.T) {
	onSince := at(300)
	for name, tc := range map[string]struct {
		credits domain.CreditEstimate
		want    []alertKey
	}{
		"no credits": {},
		"on credits": {
			credits: domain.CreditEstimate{BalanceUSD: 20, SpentUSD: 6.4, RemainingUSD: 13.6, OnCreditsSince: onSince, Active: true},
			want:    []alertKey{{domain.AlertCreditsActive, dirA, "", onSince.Sub(t0)}},
		},
		"just under 80% spent": {
			credits: domain.CreditEstimate{BalanceUSD: 20, SpentUSD: 15.99, RemainingUSD: 4.01},
		},
		"80% spent, inclusive": {
			credits: domain.CreditEstimate{BalanceUSD: 20, SpentUSD: 16, RemainingUSD: 4},
			want:    []alertKey{{domain.AlertCreditsLow, dirA, "", alertNow.Sub(t0)}},
		},
		"spent past the balance, while on credits": {
			credits: domain.CreditEstimate{BalanceUSD: 20, SpentUSD: 23, OnCreditsSince: onSince, Active: true},
			want: []alertKey{
				{domain.AlertCreditsActive, dirA, "", onSince.Sub(t0)},
				{domain.AlertCreditsLow, dirA, "", alertNow.Sub(t0)},
			},
		},
		"spend with no balance recorded": {
			credits: domain.CreditEstimate{SpentUSD: 3},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := evaluate(domain.AlertInput{
				Accounts: []domain.Account{{Dir: dirA, Label: "Main"}},
				Credits:  map[domain.ConfigDir]domain.CreditEstimate{dirA: tc.credits},
			})

			if len(got) != 0 || len(tc.want) != 0 {
				if !reflect.DeepEqual(keysOf(got), tc.want) {
					t.Errorf("Evaluate() = %v, want %v", keysOf(got), tc.want)
				}
			}
		})
	}
}

func TestEvaluate_CreditMessagesNameTheAccountAndTheAmounts(t *testing.T) {
	for name, tc := range map[string]struct {
		credits domain.CreditEstimate
		want    map[domain.AlertKind]string
	}{
		"estimated": {
			credits: domain.CreditEstimate{
				BalanceUSD: 20, SpentUSD: 16.4, OnCreditsSince: at(300), Active: true, Source: domain.CreditSourceEstimate,
			},
			want: map[domain.AlertKind]string{
				domain.AlertCreditsActive: "Main is using usage credits",
				domain.AlertCreditsLow:    "Main has used about $16.40 of its $20.00 in usage credits",
			},
		},
		"reported": {
			credits: domain.CreditEstimate{BalanceUSD: 500, SpentUSD: 450, Source: domain.CreditSourceReported},
			want: map[domain.AlertKind]string{
				domain.AlertCreditsLow: "Main has used $450.00 of its $500.00 in usage credits",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := evaluate(domain.AlertInput{
				Accounts: []domain.Account{{Dir: dirA, Label: "Main"}},
				Credits:  map[domain.ConfigDir]domain.CreditEstimate{dirA: tc.credits},
			})

			messages := make(map[domain.AlertKind]string, len(got))
			for _, a := range got {
				messages[a.Kind] = a.Message
			}
			if !reflect.DeepEqual(messages, tc.want) {
				t.Errorf("messages %q, want %q", messages, tc.want)
			}
		})
	}
}

func TestEvaluate_EndedSessionsIgnored(t *testing.T) {
	ended := withContext(alertSession("ended", dirA, domain.StateEnded, 0), 99)
	lost := withContext(alertSession("lost", dirA, domain.StateEnded, 0), 99)
	lost.Lost = true
	parked := withContext(alertSession("parked", dirA, domain.StateParked, 0), 99)

	got := evaluate(domain.AlertInput{Sessions: []domain.Session{ended, lost, parked}})

	if len(got) != 0 {
		t.Errorf("Evaluate() = %v, want no alerts for ended, lost or parked sessions", got)
	}
}

func TestEvaluate_Order(t *testing.T) {
	accounts := []domain.Account{
		accountWithWeekly(dirB, "B", 95),
		accountWithWeekly(dirA, "Main", 91),
	}
	sessions := []domain.Session{
		withContext(alertSession("c2", dirB, domain.StateWorking, 0), 90),
		alertSession("zzz", dirB, domain.StateNeedsYou, 900),
		alertSession("old", dirB, domain.StateNeedsYou, 100),
		withContext(alertSession("c1", dirA, domain.StateIdle, 0), 90),
		alertSession("bbb", dirA, domain.StateNeedsYou, 900),
		alertSession("aaa", dirA, domain.StateNeedsYou, 900),
	}
	in := domain.AlertInput{
		Accounts: accounts,
		Sessions: sessions,
		Projections: map[domain.ConfigDir]domain.WindowProjections{
			dirA: {SevenDay: runsOut(alertNow.Add(time.Hour))},
			dirB: {FiveHour: runsOut(alertNow.Add(time.Hour))},
		},
		SpendToday: map[domain.ConfigDir]float64{dirA: 12, dirB: 12},
		Budgets:    map[domain.ConfigDir]float64{dirA: 10, dirB: 10},
		Credits: map[domain.ConfigDir]domain.CreditEstimate{
			dirA: {BalanceUSD: 20, SpentUSD: 19, OnCreditsSince: at(700), Active: true},
			dirB: {BalanceUSD: 20, SpentUSD: 17, OnCreditsSince: at(200), Active: true},
		},
	}
	now := int(alertNow.Sub(t0).Seconds())

	want := []alertKey{
		// needs_you first, the longest wait first whatever the account...
		{domain.AlertNeedsYou, dirB, "old", offset(100)},
		// ...and alerts with the same Since by account, then session.
		{domain.AlertNeedsYou, dirA, "aaa", offset(900)},
		{domain.AlertNeedsYou, dirA, "bbb", offset(900)},
		{domain.AlertNeedsYou, dirB, "zzz", offset(900)},
		{domain.AlertRunsOut, dirA, "", offset(now)},
		{domain.AlertRunsOut, dirB, "", offset(now)},
		// On credits since its span began: the longest-standing first.
		{domain.AlertCreditsActive, dirB, "", offset(200)},
		{domain.AlertCreditsActive, dirA, "", offset(700)},
		{domain.AlertCreditsLow, dirA, "", offset(now)},
		{domain.AlertCreditsLow, dirB, "", offset(now)},
		{domain.AlertBudget, dirA, "", offset(now)},
		{domain.AlertBudget, dirB, "", offset(now)},
		{domain.AlertWeeklyThreshold, dirA, "", offset(now)},
		{domain.AlertWeeklyThreshold, dirB, "", offset(now)},
		{domain.AlertContextHigh, dirA, "c1", offset(now)},
		{domain.AlertContextHigh, dirB, "c2", offset(now)},
	}

	got := keysOf(evaluate(in))

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Evaluate() order:\n got %v\nwant %v", got, want)
	}

	// The order does not depend on how the caller happened to list things.
	reversed := in
	reversed.Accounts = []domain.Account{accounts[1], accounts[0]}
	reversed.Sessions = make([]domain.Session, len(sessions))
	for i, s := range sessions {
		reversed.Sessions[len(sessions)-1-i] = s
	}
	if again := keysOf(evaluate(reversed)); !reflect.DeepEqual(again, want) {
		t.Errorf("Evaluate(reversed input) order:\n got %v\nwant %v", again, want)
	}
}

func TestEvaluate_MessagesNameTheirSubject(t *testing.T) {
	const longID domain.SessionID = "3f1c7a52-0d4e-4b8e-9a51-6c2f0e7d9b10"
	named := alertSession(longID, dirA, domain.StateNeedsYou, 0)
	named.Name = "refactor billing"
	unnamed := withContext(alertSession(longID+"2", dirA, domain.StateWorking, 0), 90)

	got := evaluate(domain.AlertInput{
		Accounts: []domain.Account{
			accountWithWeekly(dirA, "Main", 95),
			accountWithWeekly("/home/dev/.claude-work", "", 95),
		},
		Sessions: []domain.Session{named, unnamed},
	})

	messages := make(map[domain.AlertKind][]string)
	for _, a := range got {
		messages[a.Kind] = append(messages[a.Kind], a.Message)
	}
	for kind, wants := range map[domain.AlertKind][]string{
		domain.AlertWeeklyThreshold: {"Main", ".claude-work", "95%"},
		domain.AlertNeedsYou:        {"refactor billing"},
		domain.AlertContextHigh:     {"3f1c7a52", "90%"},
	} {
		text := strings.Join(messages[kind], "\n")
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("%s messages %q do not contain %q", kind, messages[kind], want)
			}
		}
	}
	if text := strings.Join(messages[domain.AlertContextHigh], "\n"); strings.Contains(text, "3f1c7a52-") {
		t.Errorf("context_high message %q shows more of the session ID than its first 8 characters", text)
	}
}

func TestEvaluate_NeedsYouMessageSaysWhy(t *testing.T) {
	s := alertSession(sid, dirA, domain.StateNeedsYou, 0)
	s.WaitReason = "permission prompt"

	got := evaluate(domain.AlertInput{Sessions: []domain.Session{s}})

	if len(got) != 1 || !strings.Contains(got[0].Message, "permission prompt") {
		t.Errorf("Evaluate() = %v, want one alert that says it is a permission prompt", got)
	}
}

func TestEvaluate_DoesNotChangeItsInput(t *testing.T) {
	accounts := []domain.Account{accountWithWeekly(dirB, "B", 95), accountWithWeekly(dirA, "Main", 95)}
	sessions := []domain.Session{alertSession("b", dirB, domain.StateNeedsYou, 0), alertSession("a", dirA, domain.StateNeedsYou, 0)}
	wantAccounts, wantSessions := append([]domain.Account(nil), accounts...), append([]domain.Session(nil), sessions...)

	evaluate(domain.AlertInput{Accounts: accounts, Sessions: sessions})

	if !reflect.DeepEqual(accounts, wantAccounts) || !reflect.DeepEqual(sessions, wantSessions) {
		t.Error("Evaluate reordered or changed the caller's slices")
	}
}
