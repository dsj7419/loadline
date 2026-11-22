package tracker_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/tracker"
)

var update = flag.Bool("update", false, "rewrite testdata/view.golden.json from the current output")

const goldenPath = "testdata/view.golden.json"

// The golden scenario's accounts beyond the three every test uses: one on a
// cloud provider, and one whose sign-in is not known yet.
const (
	dirCloud domain.ConfigDir = "/home/dev/.claude-cloud"
	dirNew   domain.ConfigDir = "/home/dev/.claude-new"
)

// The golden scenario's sessions.
const (
	needsYouID   domain.SessionID = "3f1c7a52-0d4e-4b8e-9a51-6c2f0e7d9b10"
	workingID    domain.SessionID = "8a2b9c41-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	compactingID domain.SessionID = "c4d5e6f7-0819-4a2b-9c3d-4e5f6a7b8c9d"
	erroredID    domain.SessionID = "d1e2f3a4-b5c6-4d7e-8f90-a1b2c3d4e5f6"
	startingID   domain.SessionID = "e7f8a9b0-c1d2-4e3f-a4b5-c6d7e8f90a1b"
	idleID       domain.SessionID = "f0a1b2c3-d4e5-4f6a-b7c8-d9e0f1a2b3c4"
	parkedID     domain.SessionID = "a9b8c7d6-e5f4-4a3b-9c2d-1e0f9a8b7c6d"
	lostID       domain.SessionID = "b3c4d5e6-f7a8-4b9c-8d0e-1f2a3b4c5d6e"
	clearedID    domain.SessionID = "1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"
	successorID  domain.SessionID = "2d3e4f5a-6b7c-4d8e-9f0a-1b2c3d4e5f6a"
	retiredID    domain.SessionID = "5e6f7a8b-9c0d-4e1f-a2b3-c4d5e6f7a8b9"
)

// goldenScenario is a tracker in a state that fills every field of the view:
// an account of each sign-in, a session in each state, a /clear pair, usage
// today, earlier this week and earlier this month, credits both estimated
// and reported, and an alert of each kind. Every time in it is in testZone,
// so the view must convert them to UTC.
func goldenScenario(t *testing.T) *tracker.Tracker {
	t.Helper()
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, testZone)
	minutes := func(m float64) time.Time { return now.Add(time.Duration(m * float64(time.Minute))) }
	ticker := &fakeTicker{}
	tr := tracker.New(func() time.Time { return now }, tracker.Config{
		Labels:   map[domain.ConfigDir]string{dirA: "Main"},
		Budgets:  map[domain.ConfigDir]float64{dirB: 5},
		Credits:  map[domain.ConfigDir][]domain.CreditGrant{dirA: {{USD: 5, AddedAt: now.AddDate(0, 0, -3)}}},
		Alerts:   defaultAlerts,
		Location: testZone,
		Ticker:   ticker,
	}, tracker.NopRecorder{})
	t.Cleanup(tr.Close)

	tr.Restore(tracker.Restored{
		Accounts: []domain.Account{{Dir: dirNew}},
		Samples: map[domain.ConfigDir]domain.WindowSamples{
			// The main account's five-hour window has been at its limit
			// for 25 minutes, so it is on credits.
			dirA: {FiveHour: []domain.Sample{{At: minutes(-25), UsedPct: 100}}, SevenDay: []domain.Sample{
				{At: minutes(-60), UsedPct: 88},
				{At: minutes(-45), UsedPct: 89},
				{At: minutes(-30), UsedPct: 90},
				{At: minutes(-15), UsedPct: 91},
			}},
			dirB: {SevenDay: []domain.Sample{
				{At: minutes(-30), UsedPct: 4}, {At: minutes(-20), UsedPct: 4}, {At: minutes(-10), UsedPct: 4},
			}},
		},
		Usage: []tracker.UsageRow{
			{
				// 23:30 yesterday.
				Minute: time.Date(2026, 11, 20, 23, 30, 0, 0, testZone), Dir: dirA, Session: needsYouID, Model: "Opus 5.5",
				Input: 900, Output: 400, CacheRead: 30000, CacheWrite: 1500, CostUSD: 0.75, Requests: 3,
			},
			{
				Minute: now.AddDate(0, 0, -20), Dir: dirC, Session: retiredID, Model: "Sonnet 5.5",
				Input: 40000, Output: 9000, CacheRead: 250000, CacheWrite: 12000, CostUSD: 1.5, Requests: 12,
			},
		},
	})

	probedAt := minutes(-90)
	tr.SetAccount(domain.Account{Dir: dirA, Email: "dev@example.com", Plan: "Max 20x", Org: "Example", Auth: domain.AuthSubscription, ProbedAt: probedAt})
	tr.SetAccount(domain.Account{Dir: dirB, Email: "other@example.com", Plan: "Pro", Auth: domain.AuthSubscription, ProbedAt: probedAt})
	tr.SetAccount(domain.Account{Dir: dirC, Auth: domain.AuthAPIKey, ProbedAt: probedAt})
	tr.SetAccount(domain.Account{Dir: dirCloud, Plan: "Bedrock", Auth: domain.AuthThirdParty, ProbedAt: probedAt})

	hookAt := func(when time.Time, dir domain.ConfigDir, id domain.SessionID, pid int, name string) domain.Hook {
		return domain.Hook{At: when, Dir: dir, Session: id, PID: pid, Name: name}
	}
	statusAt := func(when time.Time, dir domain.ConfigDir, id domain.SessionID, name, project string, m domain.Metrics) domain.Status {
		return domain.Status{At: when, Dir: dir, Session: id, Name: name, ProjectDir: project, Metrics: m, Limits: domain.Limits{ObservedAt: when}}
	}
	opus := func(effort string, contextPct float64, used int64, costUSD float64, added, removed int64, hitRatio float64) domain.Metrics {
		return domain.Metrics{
			Model: "Opus 5.5", ModelID: "claude-opus-5-5", Effort: effort,
			ContextPct: contextPct, ContextSize: 200000, ContextUsed: used, CostUSD: costUSD,
			LinesAdded: added, LinesRemoved: removed, CacheWarm: true, CacheHitRatio: hitRatio,
		}
	}
	sonnet := domain.Metrics{Model: "Sonnet 5.5", ModelID: "claude-sonnet-5-5", Effort: "medium", ContextPct: 12, ContextSize: 200000, ContextUsed: 24000, CostUSD: 2.4}
	requestAt := func(when time.Time, id domain.SessionID, model string, input, output, cacheRead, cacheWrite int64, costUSD float64) domain.APIRequest {
		return domain.APIRequest{At: when, Session: id, Model: model, Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite, CostUSD: costUSD}
	}

	// Waiting on a permission prompt for 72 seconds.
	copper := opus("xhigh", 31, 62000, 1.75, 120, 14, 0.87)
	tr.Apply(hookAt(minutes(-25), dirA, needsYouID, 4101, "SessionStart"))
	tr.Apply(statusAt(minutes(-24), dirA, needsYouID, "copper-lantern", "/home/dev/src/loadline", copper))
	tr.Apply(hookAt(minutes(-23), dirA, needsYouID, 4101, "UserPromptSubmit"))
	tr.Apply(hookAt(now.Add(-72*time.Second), dirA, needsYouID, 4101, "PermissionRequest"))
	tr.Apply(statusAt(now.Add(-5*time.Second), dirA, needsYouID, "copper-lantern", "/home/dev/src/loadline", copper))
	tr.RecordAPI(requestAt(minutes(-22), needsYouID, "Opus 5.5", 1200, 800, 50000, 2000, 1))

	// Working with two subagents and a nearly full context. Its status line
	// carries the main account's limits: a seven-day window on pace to run
	// out, and a five-hour window at its limit whose reset time is unknown.
	// The calls since that window filled spend 80% of the main account's
	// credits.
	harbor := opus("high", 88, 176000, 3.2, 340, 52, 0.92)
	tr.Apply(hookAt(minutes(-40), dirA, workingID, 4102, "SessionStart"))
	tr.Apply(statusAt(minutes(-39), dirA, workingID, "harbor-api", "/home/dev/src/harbor-api", harbor))
	tr.Apply(hookAt(minutes(-3), dirA, workingID, 4102, "UserPromptSubmit"))
	for _, agent := range []string{"agent-1", "agent-2"} {
		sub := hookAt(minutes(-2), dirA, workingID, 4102, "SubagentStart")
		sub.AgentID = agent
		tr.Apply(sub)
	}
	withLimits := statusAt(now, dirA, workingID, "harbor-api", "/home/dev/src/harbor-api", harbor)
	withLimits.Limits.FiveHour = &domain.Window{UsedPct: 100}
	withLimits.Limits.SevenDay = &domain.Window{UsedPct: 92, ResetsAt: now.Add(48 * time.Hour)}
	tr.Apply(withLimits)
	tr.RecordAPI(requestAt(minutes(-20), workingID, "Opus 5.5", 5000, 2400, 180000, 6000, 2.75))
	tr.RecordAPI(requestAt(minutes(-2), workingID, "Haiku 5.5", 3000, 600, 0, 0, 0.25))

	// Compacting, on the second account.
	tr.Apply(hookAt(minutes(-50), dirB, compactingID, 5201, "SessionStart"))
	tr.Apply(statusAt(minutes(-49), dirB, compactingID, "", "/home/dev/src/notes", sonnet))
	tr.Apply(hookAt(minutes(-8), dirB, compactingID, 5201, "UserPromptSubmit"))
	tr.Apply(hookAt(minutes(-1), dirB, compactingID, 5201, "PreCompact"))
	tr.RecordAPI(requestAt(minutes(-6), compactingID, "Sonnet 5.5", 30000, 4000, 120000, 8000, 3))

	// A turn that failed.
	tr.Apply(hookAt(minutes(-30), dirB, erroredID, 5202, "SessionStart"))
	tr.Apply(statusAt(minutes(-29), dirB, erroredID, "flaky-build", "/home/dev/src/ci", sonnet))
	tr.Apply(hookAt(minutes(-12), dirB, erroredID, 5202, "UserPromptSubmit"))
	tr.Apply(hookAt(minutes(-11), dirB, erroredID, 5202, "StopFailure"))

	// Just started: nothing known but its process.
	tr.Apply(hookAt(now.Add(-4*time.Second), dirA, startingID, 4103, "SessionStart"))

	// Idle on the second account. Its status line carries that account's
	// limits: a flat seven-day window, a five-hour window with too few
	// samples to project, and the spend limit of the gateway it signs in
	// through.
	tr.Apply(hookAt(minutes(-45), dirB, idleID, 5203, "SessionStart"))
	tr.Apply(hookAt(minutes(-10), dirB, idleID, 5203, "UserPromptSubmit"))
	tr.Apply(hookAt(minutes(-4), dirB, idleID, 5203, "Stop"))
	ledger := statusAt(now, dirB, idleID, "quiet-ledger", "/home/dev/notes", sonnet)
	ledger.Limits.FiveHour = &domain.Window{UsedPct: 10, ResetsAt: now.Add(3 * time.Hour)}
	ledger.Limits.SevenDay = &domain.Window{UsedPct: 4, ResetsAt: now.Add(5 * 24 * time.Hour)}
	ledger.SpendLimit = &domain.SpendLimit{UsedUSD: 12.5, LimitUSD: 50}
	tr.Apply(ledger)
	tr.RecordAPI(requestAt(minutes(-9), idleID, "Sonnet 5.5", 28000, 3500, 110000, 7000, 3))

	// A finished background session.
	tr.Apply(domain.Poll{
		At: minutes(-10), Dir: dirA, Session: parkedID, Kind: domain.KindBackground,
		BgState: "done", Name: "nightly-triage", Cwd: "/home/dev/src/triage", StartedAt: minutes(-120),
	})

	// A session that vanished: missing from the poll with no status line for
	// four minutes.
	tr.Apply(hookAt(minutes(-35), dirA, lostID, 4104, "SessionStart"))
	tr.Apply(statusAt(minutes(-34), dirA, lostID, "scratch", "/home/dev/scratch", opus("low", 5, 10000, 0.1, 0, 0, 0)))
	tr.Apply(domain.Absent{At: minutes(-30), Dir: dirA, Session: lostID})

	// /clear in one terminal: the cleared session gives way to its successor.
	tr.Apply(hookAt(minutes(-20), dirA, clearedID, 4105, "SessionStart"))
	tr.Apply(statusAt(minutes(-19), dirA, clearedID, "", "/home/dev/src/web", opus("medium", 44, 88000, 0.6, 12, 3, 0.5)))
	end := hookAt(minutes(-5), dirA, clearedID, 4105, "SessionEnd")
	end.Reason = "clear"
	tr.Apply(end)
	start := hookAt(minutes(-5).Add(time.Second), dirA, successorID, 4105, "SessionStart")
	start.Source = "clear"
	tr.Apply(start)
	tr.Apply(statusAt(minutes(-4), dirA, successorID, "", "/home/dev/src/web", opus("medium", 3, 6000, 0, 0, 0, 0)))

	return tr
}

func TestView_GoldenJSON(t *testing.T) {
	v := goldenScenario(t).Snapshot()
	assertGoldenCoverage(t, v)

	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal the view: %v", err)
	}
	got = append(got, '\n')

	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s (run with -update to create it): %v", goldenPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the view differs from %s; if the change is intended, run go test ./internal/tracker -run TestView_GoldenJSON -update\n got:\n%s", goldenPath, got)
	}
}

// assertGoldenCoverage guards the scenario itself: the UI's contract test
// checks only the keys the golden file holds, so it must hold all of them.
func assertGoldenCoverage(t *testing.T, v tracker.View) {
	t.Helper()
	var states []domain.State
	for _, s := range v.Sessions {
		states = append(states, s.State)
	}
	for state := domain.StateStarting; state <= domain.StateEnded; state++ {
		if !slices.Contains(states, state) {
			t.Errorf("no session is %v", state)
		}
	}
	var kinds []domain.AlertKind
	for _, a := range v.Alerts {
		kinds = append(kinds, a.Kind)
	}
	allKinds := []domain.AlertKind{
		domain.AlertNeedsYou, domain.AlertRunsOut, domain.AlertCreditsActive, domain.AlertCreditsLow,
		domain.AlertBudget, domain.AlertWeeklyThreshold, domain.AlertContextHigh,
	}
	if !slices.Equal(kinds, allKinds) {
		t.Errorf("alerts %v, want one of each kind %v", kinds, allKinds)
	}

	var windows []*tracker.WindowView
	var noWindow bool
	for _, a := range v.Accounts {
		for _, w := range []*tracker.WindowView{a.FiveHour, a.SevenDay} {
			if w == nil {
				noWindow = true
				continue
			}
			windows = append(windows, w)
		}
	}
	var unknownReset, noProjection, withETA, withoutETA bool
	for _, w := range windows {
		unknownReset = unknownReset || time.Time(w.ResetsAt).IsZero()
		if w.Projection == nil {
			noProjection = true
			continue
		}
		withETA = withETA || !time.Time(w.Projection.ETA).IsZero()
		withoutETA = withoutETA || time.Time(w.Projection.ETA).IsZero()
	}
	if !noWindow || !unknownReset || !noProjection || !withETA || !withoutETA {
		t.Errorf("windows: one missing %v, unknown reset %v, without projection %v, projection with ETA %v and without %v; want all",
			noWindow, unknownReset, noProjection, withETA, withoutETA)
	}

	var lost, subagents, share, waitReason, backgroundWithoutProcess bool
	for _, s := range v.Sessions {
		lost = lost || s.Lost
		subagents = subagents || s.Subagents > 0
		share = share || s.Share > 0
		waitReason = waitReason || s.WaitReason != ""
		backgroundWithoutProcess = backgroundWithoutProcess || (s.Kind == domain.KindBackground && s.PID == 0)
	}
	if !lost || !subagents || !share || !waitReason || !backgroundWithoutProcess {
		t.Errorf("sessions: lost %v, with subagents %v, with a share %v, with a wait reason %v, background without a process %v; want all",
			lost, subagents, share, waitReason, backgroundWithoutProcess)
	}
	var budget, weekBelowMonth bool
	var auths []domain.AuthKind
	for _, a := range v.Accounts {
		budget = budget || a.BudgetUSD > 0
		weekBelowMonth = weekBelowMonth || a.SpendWeekUSD != a.SpendMonthUSD
		auths = append(auths, a.Auth)
	}
	if !budget || !weekBelowMonth {
		t.Errorf("accounts: with a budget %v, with week and month spend differing %v; want both", budget, weekBelowMonth)
	}
	for _, auth := range []domain.AuthKind{domain.AuthUnknown, domain.AuthSubscription, domain.AuthAPIKey, domain.AuthThirdParty} {
		if !slices.Contains(auths, auth) {
			t.Errorf("no account signs in with %v", auth)
		}
	}
	assertCreditsCoverage(t, v)

	successor, _ := findSession(v, successorID)
	if successor.Replaces != clearedID {
		t.Errorf("the successor replaces %q, want %s", successor.Replaces, clearedID)
	}
	if _, shown := findSession(v, clearedID); shown {
		t.Error("the cleared session is shown")
	}
	if len(v.Models) == 0 || slices.Equal(v.Models, v.ModelsWeek) {
		t.Errorf("models today %v and this week %v, want both, differing", v.Models, v.ModelsWeek)
	}
}

// assertCreditsCoverage checks that the golden view's accounts show credits
// estimated while on them, credits reported while off them, and no credits.
func assertCreditsCoverage(t *testing.T, v tracker.View) {
	t.Helper()
	var estimatedOn, reportedOff, none bool
	for _, a := range v.Accounts {
		c := a.Credits
		switch {
		case c == nil:
			none = true
		case c.Source == domain.CreditSourceEstimate && c.Active && !time.Time(c.OnCreditsSince).IsZero():
			estimatedOn = true
		case c.Source == domain.CreditSourceReported && !c.Active && time.Time(c.OnCreditsSince).IsZero():
			reportedOff = true
		}
	}
	if !estimatedOn || !reportedOff || !none {
		t.Errorf("credits: estimated while on them %v, reported while off them %v, none %v; want all", estimatedOn, reportedOff, none)
	}
}

func TestView_EmptyTrackerEncodesEmptyLists(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.clock.Set(time.Date(2026, 11, 21, 10, 0, 0, 0, testZone))
	got, err := json.Marshal(h.tr.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"generatedAt":"2026-11-21T18:00:00Z","accounts":[],"sessions":[],"models":[],"modelsWeek":[],"alerts":[]}`
	if string(got) != want {
		t.Errorf("empty view\n got %s\nwant %s", got, want)
	}
}

func TestView_EmptyListsInsideAccountsAndSessions(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Restore(tracker.Restored{Sessions: []domain.Session{restoredSession(s1, domain.StateIdle, at(-60))}})
	got, err := json.Marshal(h.tr.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		Accounts []map[string]json.RawMessage `json:"accounts"`
		Sessions []map[string]json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(got, &shape); err != nil {
		t.Fatal(err)
	}
	if len(shape.Accounts) != 1 || len(shape.Sessions) != 1 {
		t.Fatalf("view %s, want one account and one session", got)
	}
	for _, key := range []string{"models", "modelsWeek"} {
		if raw := string(shape.Accounts[0][key]); raw != "[]" {
			t.Errorf("account %s = %s, want []", key, raw)
		}
	}
	for _, key := range []string{"models", "timeline"} {
		if raw := string(shape.Sessions[0][key]); raw != "[]" {
			t.Errorf("session %s = %s, want []", key, raw)
		}
	}
}

func TestTimestamp_JSON(t *testing.T) {
	local := time.Date(2026, 11, 21, 10, 0, 0, 500, testZone)
	for _, tc := range []struct {
		name string
		in   tracker.Timestamp
		want string
	}{
		{"unknown", tracker.Timestamp{}, `null`},
		{"known, in UTC", tracker.Timestamp(local), `"2026-11-21T18:00:00.0000005Z"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("marshal = %s, want %s", got, tc.want)
			}
			var back tracker.Timestamp
			if err := json.Unmarshal(got, &back); err != nil {
				t.Fatalf("unmarshal %s: %v", got, err)
			}
			if !time.Time(back).Equal(time.Time(tc.in)) {
				t.Errorf("round trip gives %v, want %v", time.Time(back), time.Time(tc.in))
			}
		})
	}

	var ts tracker.Timestamp
	if err := json.Unmarshal([]byte(`"yesterday"`), &ts); err == nil {
		t.Error("unmarshal accepted text that is not a time")
	}
}

// A time outside the years JSON can hold never comes through the ingest
// routes, which refuse it, but the view encodes whatever the state holds:
// one bad time must not cost every client its dashboard. A time the view
// always sets is held at the nearest year it can show; one that may be
// unknown shows as unknown.
func TestView_TimesOutsideTheEncodableYearsStillEncode(t *testing.T) {
	h := newHarness(t, tracker.Config{Alerts: defaultAlerts})
	pastYear9999 := time.Date(9999, 12, 31, 23, 59, 59, 0, time.FixedZone("", -3600))
	beforeYear0 := time.Date(0, 1, 1, 0, 0, 0, 0, time.FixedZone("", 3600))
	h.tr.Apply(domain.Hook{At: pastYear9999, Dir: dirA, Session: s1, PID: pidA, Name: "PermissionRequest"})
	h.tr.Apply(domain.Hook{At: beforeYear0, Dir: dirA, Session: s2, PID: pidA, Name: "UserPromptSubmit"})
	status := statusLine(1, s3)
	status.Limits = domain.Limits{FiveHour: window(40, pastYear9999), ObservedAt: pastYear9999}
	h.tr.Apply(status)

	for _, clock := range []time.Time{t0, pastYear9999} {
		h.clock.Set(clock)
		data, err := json.Marshal(h.tr.Snapshot())
		if err != nil {
			t.Fatalf("with the clock at %v, the view does not encode: %v", clock, err)
		}
		var got struct {
			GeneratedAt string `json:"generatedAt"`
			Sessions    []struct {
				ID         domain.SessionID `json:"id"`
				StartedAt  string           `json:"startedAt"`
				StateAt    string           `json:"stateAt"`
				LastSeenAt string           `json:"lastSeenAt"`
				Timeline   []struct {
					At string `json:"at"`
				} `json:"timeline"`
			} `json:"sessions"`
			Accounts []struct {
				FiveHour *struct {
					ResetsAt   *string `json:"resetsAt"`
					ObservedAt *string `json:"observedAt"`
				} `json:"fiveHour"`
			} `json:"accounts"`
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("decode the view: %v", err)
		}

		const latest, earliest = "9999-12-31T23:59:59.999999999Z", "0000-01-01T00:00:00Z"
		if clock.Equal(pastYear9999) && got.GeneratedAt != latest {
			t.Errorf("generatedAt = %s, want %s", got.GeneratedAt, latest)
		}
		want := map[domain.SessionID]string{s1: latest, s2: earliest}
		for _, s := range got.Sessions {
			at, ok := want[s.ID]
			if !ok {
				continue
			}
			if s.StateAt != at || s.LastSeenAt != at || s.StartedAt != at || len(s.Timeline) != 1 || s.Timeline[0].At != at {
				t.Errorf("session %s shows startedAt %s, stateAt %s, lastSeenAt %s, timeline %+v; want each at %s",
					s.ID, s.StartedAt, s.StateAt, s.LastSeenAt, s.Timeline, at)
			}
		}
		if !clock.Equal(t0) {
			// The window has reset by then.
			continue
		}
		if len(got.Accounts) != 1 || got.Accounts[0].FiveHour == nil {
			t.Fatalf("accounts %s, want one with its five-hour window", data)
		}
		if w := got.Accounts[0].FiveHour; w.ResetsAt != nil || w.ObservedAt != nil {
			t.Errorf("five-hour window resets at %v, observed at %v; want both null", w.ResetsAt, w.ObservedAt)
		}
	}
}

func TestTimestamp_OutsideTheEncodableYearsIsNull(t *testing.T) {
	for _, at := range []time.Time{
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(-1, 12, 31, 23, 59, 59, 0, time.UTC),
	} {
		got, err := json.Marshal(tracker.Timestamp(at))
		if err != nil || string(got) != "null" {
			t.Errorf("marshal %v = %s, %v; want null", at, got, err)
		}
	}
}

func TestView_DecodesFromItsJSON(t *testing.T) {
	v := goldenScenario(t).Snapshot()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var back tracker.View
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal the view: %v", err)
	}
	again, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, data) {
		t.Errorf("the view changes on a round trip through JSON\nbefore %s\n after %s", data, again)
	}
}
