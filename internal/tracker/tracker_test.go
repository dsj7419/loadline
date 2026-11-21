package tracker_test

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/tracker"
)

const (
	dirA domain.ConfigDir = "/home/dev/.claude"
	dirB domain.ConfigDir = "/home/dev/.claude-b"
	dirC domain.ConfigDir = "/home/dev/.claude-work"

	s1 domain.SessionID = "s1"
	s2 domain.SessionID = "s2"
	s3 domain.SessionID = "s3"

	pidA = 4242
	pidB = 5151
)

// testZone is eight hours behind UTC, so a day bucketed in UTC by mistake
// lands on the wrong date.
var testZone = time.FixedZone("test", -8*3600)

// t0 is 10:00 on 21 November in testZone.
var t0 = time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)

var defaultAlerts = domain.AlertConfig{WeeklyPct: 90, ContextPct: 85, NeedsYouAfter: 20 * time.Second}

// at is sec seconds after t0.
func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

// The test doubles.

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeTicker runs nothing until the test fires it.
type fakeTicker struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

type fakeTimer struct {
	delay time.Duration
	f     func()
	done  bool
}

func (k *fakeTicker) AfterFunc(d time.Duration, f func()) func() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	timer := &fakeTimer{delay: d, f: f}
	k.timers = append(k.timers, timer)
	return func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		stopped := !timer.done
		timer.done = true
		return stopped
	}
}

// pending returns the delays of the timers that have neither run nor been
// stopped.
func (k *fakeTicker) pending() []time.Duration {
	k.mu.Lock()
	defer k.mu.Unlock()
	var delays []time.Duration
	for _, timer := range k.timers {
		if !timer.done {
			delays = append(delays, timer.delay)
		}
	}
	return delays
}

// fire runs the timers pending now, as if their delays had passed. Timers
// they schedule wait for the next fire.
func (k *fakeTicker) fire() {
	k.mu.Lock()
	var due []func()
	for _, timer := range k.timers {
		if !timer.done {
			timer.done = true
			due = append(due, timer.f)
		}
	}
	k.mu.Unlock()
	for _, f := range due {
		f()
	}
}

// runAll runs every timer's function, stopped or not, as a wall-clock timer
// can when it fires just as it is stopped.
func (k *fakeTicker) runAll() {
	k.mu.Lock()
	all := slices.Clone(k.timers)
	k.mu.Unlock()
	for _, timer := range all {
		timer.f()
	}
}

type limitSample struct {
	dir    domain.ConfigDir
	limits domain.Limits
}

type usageRecord struct {
	dir     domain.ConfigDir
	request domain.APIRequest
}

type fakeRecorder struct {
	mu       sync.Mutex
	sessions []domain.Session
	accounts []domain.Account
	samples  []limitSample
	usage    []usageRecord
}

func (r *fakeRecorder) SessionChanged(s domain.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = append(r.sessions, s)
}

func (r *fakeRecorder) AccountChanged(a domain.Account) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accounts = append(r.accounts, a)
}

func (r *fakeRecorder) LimitSampled(dir domain.ConfigDir, l domain.Limits) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples = append(r.samples, limitSample{dir, l})
}

func (r *fakeRecorder) UsageRecorded(dir domain.ConfigDir, req domain.APIRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage = append(r.usage, usageRecord{dir, req})
}

func (r *fakeRecorder) recorded() (sessions []domain.Session, accounts []domain.Account, samples []limitSample, usage []usageRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sessions), slices.Clone(r.accounts), slices.Clone(r.samples), slices.Clone(r.usage)
}

// harness is a tracker on a fake clock, starting at t0, and a fake ticker.
type harness struct {
	t      *testing.T
	clock  *fakeClock
	ticker *fakeTicker
	rec    *fakeRecorder
	tr     *tracker.Tracker
}

func newHarness(t *testing.T, cfg tracker.Config) *harness {
	t.Helper()
	h := newUnclosedHarness(t, cfg)
	t.Cleanup(h.tr.Close)
	return h
}

// newUnclosedHarness leaves closing the tracker to the test, for a test that
// must fail rather than hang when the tracker's lock is stuck.
func newUnclosedHarness(t *testing.T, cfg tracker.Config) *harness {
	t.Helper()
	h := &harness{t: t, clock: &fakeClock{now: t0}, ticker: &fakeTicker{}, rec: &fakeRecorder{}}
	cfg.Ticker = h.ticker
	if cfg.Location == nil {
		cfg.Location = testZone
	}
	h.tr = tracker.New(h.clock.Now, cfg, h.rec)
	return h
}

func (h *harness) session(id domain.SessionID) tracker.SessionView {
	h.t.Helper()
	s, ok := findSession(h.tr.Snapshot(), id)
	if !ok {
		h.t.Fatalf("session %s is not in the view", id)
	}
	return s
}

func (h *harness) account(dir domain.ConfigDir) tracker.AccountView {
	h.t.Helper()
	for _, a := range h.tr.Snapshot().Accounts {
		if a.Dir == dir {
			return a
		}
	}
	h.t.Fatalf("account %s is not in the view", dir)
	return tracker.AccountView{}
}

func findSession(v tracker.View, id domain.SessionID) (tracker.SessionView, bool) {
	for _, s := range v.Sessions {
		if s.ID == id {
			return s, true
		}
	}
	return tracker.SessionView{}, false
}

func sessionIDs(v tracker.View) []domain.SessionID {
	ids := make([]domain.SessionID, len(v.Sessions))
	for i, s := range v.Sessions {
		ids[i] = s.ID
	}
	return ids
}

// receive takes the view waiting on ch, failing when there is none.
func receive(t *testing.T, ch <-chan tracker.View) tracker.View {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("the subscription is closed")
		}
		return v
	default:
		t.Fatal("no view was published")
		return tracker.View{}
	}
}

func assertClosed(t *testing.T, ch <-chan tracker.View) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("a view arrived on a subscription that should be closed")
		}
	default:
		t.Error("the subscription is open")
	}
}

func assertNoView(t *testing.T, ch <-chan tracker.View) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected view published at %v", v.GeneratedAt)
	default:
	}
}

// Events.

func hook(sec int, id domain.SessionID, name string) domain.Hook {
	return domain.Hook{At: at(sec), Dir: dirA, Session: id, PID: pidA, Name: name}
}

func onDir(h domain.Hook, dir domain.ConfigDir) domain.Hook {
	h.Dir = dir
	return h
}

// inProcessB is h sent from another Claude process.
func inProcessB(h domain.Hook) domain.Hook {
	h.PID = pidB
	return h
}

func sessionEnd(sec int, id domain.SessionID, reason string) domain.Hook {
	h := hook(sec, id, "SessionEnd")
	h.Reason = reason
	return h
}

// statusLine is a status line without rate limits.
func statusLine(sec int, id domain.SessionID) domain.Status {
	return domain.Status{At: at(sec), Dir: dirA, Session: id, Limits: domain.Limits{ObservedAt: at(sec)}}
}

func withWindows(st domain.Status, fiveHour, sevenDay *domain.Window) domain.Status {
	st.Limits.FiveHour, st.Limits.SevenDay = fiveHour, sevenDay
	return st
}

func withContext(st domain.Status, pct float64) domain.Status {
	st.Metrics.ContextPct = pct
	return st
}

func window(pct float64, resetsAt time.Time) *domain.Window {
	return &domain.Window{UsedPct: pct, ResetsAt: resetsAt}
}

// busyPoll is an interactive session's entry while it runs a turn.
func busyPoll(sec int, id domain.SessionID) domain.Poll {
	return domain.Poll{At: at(sec), Dir: dirA, Session: id, Kind: domain.KindInteractive, PID: pidA, Status: "busy"}
}

// backgroundRecord is the entry `claude agents --all` keeps for a background
// session whose process has gone.
func backgroundRecord(sec int, id domain.SessionID, bgState string, startedAt time.Time) domain.Poll {
	return domain.Poll{At: at(sec), Dir: dirA, Session: id, Kind: domain.KindBackground, BgState: bgState, StartedAt: startedAt}
}

func request(when time.Time, id domain.SessionID, model string, costUSD float64) domain.APIRequest {
	return domain.APIRequest{
		At: when, Session: id, Model: model,
		Input: 100, Output: 50, CacheRead: 1000, CacheWrite: 10, CostUSD: costUSD,
	}
}

// modelCosts reduces totals to model and cost, which is what most usage
// tests compare.
type modelCost struct {
	model    string
	costUSD  float64
	requests int
}

func costsOf(totals []tracker.ModelTotal) []modelCost {
	out := make([]modelCost, len(totals))
	for i, m := range totals {
		out[i] = modelCost{m.Model, m.CostUSD, m.Requests}
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// Tests.

func TestNew_NilRecorderRecordsNothing(t *testing.T) {
	tr := tracker.New(func() time.Time { return t0 }, tracker.Config{Ticker: &fakeTicker{}}, nil)
	defer tr.Close()
	tr.SetAccount(domain.Account{Dir: dirA, Email: "dev@example.com"})
	tr.Apply(withWindows(statusLine(0, s1), nil, window(50, at(0).Add(48*time.Hour))))
	tr.RecordAPI(request(at(0), s1, "Opus 5.5", 1))
	if v := tr.Snapshot(); len(v.Sessions) != 1 || len(v.Models) != 1 {
		t.Errorf("sessions %v and models %v, want the session and its usage", sessionIDs(v), v.Models)
	}
}

func TestTracker_HookThenPollReconciles(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(hook(0, s1, "SessionStart"))
	h.tr.Apply(hook(1, s1, "PermissionRequest"))
	if got := h.session(s1); got.State != domain.StateNeedsYou || got.WaitReason != "permission prompt" {
		t.Fatalf("after PermissionRequest: %v (%q), want needs_you (permission prompt)", got.State, got.WaitReason)
	}

	// The user answers in the terminal. No hook marks that; the next poll does.
	h.tr.ApplyPoll(dirA, at(5), []domain.Poll{busyPoll(5, s1)})
	if got := h.session(s1); got.State != domain.StateWorking || got.WaitReason != "" {
		t.Fatalf("after a busy poll: %v (%q), want working with no reason", got.State, got.WaitReason)
	}

	h.tr.Apply(hook(6, s1, "Stop"))
	// A poll taken before the Stop hook, applied late, is older news.
	h.tr.ApplyPoll(dirA, at(4), []domain.Poll{busyPoll(4, s1)})
	if got := h.session(s1).State; got != domain.StateIdle {
		t.Errorf("after Stop and a late poll: %v, want idle", got)
	}
}

func TestApplyPoll_AbsentEndsAfterGrace(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(statusLine(0, s1))
	h.tr.Apply(domain.Status{At: at(0), Dir: dirB, Session: s2})

	h.tr.ApplyPoll(dirA, at(30), nil)
	if got := h.session(s1).State; got != domain.StateIdle {
		t.Fatalf("missing from the poll 30 s after its status line: %v, want idle (within the grace)", got)
	}

	h.tr.ApplyPoll(dirA, at(31), nil)
	if got := h.session(s1); got.State != domain.StateEnded || !got.Lost {
		t.Errorf("missing 31 s after its status line: %v (lost %v), want ended and lost", got.State, got.Lost)
	}
	if got := h.session(s2).State; got != domain.StateIdle {
		t.Errorf("a session of another account: %v, want idle; only its own account's poll can miss it", got)
	}
}

func TestApplyPoll_LiveEntryWinsOverItsRecord(t *testing.T) {
	// After an in-place resume, `claude agents --all` can list a background
	// session twice: the record of its earlier run and its live process.
	record := backgroundRecord(0, s1, "done", time.Time{})
	live := domain.Poll{At: at(0), Dir: dirA, Session: s1, Kind: domain.KindBackground, PID: pidA, Status: "busy", BgState: "working"}

	for _, tc := range []struct {
		name  string
		polls []domain.Poll
	}{
		{"record listed first", []domain.Poll{record, live}},
		{"live entry listed first", []domain.Poll{live, record}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tracker.Config{})
			h.tr.ApplyPoll(dirA, at(0), tc.polls)
			if got := h.session(s1); got.State != domain.StateWorking || got.PID != pidA {
				t.Errorf("state %v, pid %d; want working in process %d", got.State, got.PID, pidA)
			}
		})
	}
}

func TestApplyPoll_LongFinishedRecordsCreateNoSession(t *testing.T) {
	dayAndAnHourAgo := at(0).Add(-25 * time.Hour)
	for _, bgState := range []string{"done", "blocked", "stopped", "failed"} {
		t.Run(bgState, func(t *testing.T) {
			h := newHarness(t, tracker.Config{})
			h.tr.ApplyPoll(dirA, at(0), []domain.Poll{
				backgroundRecord(0, s1, bgState, dayAndAnHourAgo),
				backgroundRecord(0, s2, bgState, at(0).Add(-23*time.Hour)),
			})
			v := h.tr.Snapshot()
			if _, ok := findSession(v, s1); ok {
				t.Errorf("a record of a session started 25 h ago became a session")
			}
			if _, ok := findSession(v, s2); !ok {
				t.Errorf("a record of a session started 23 h ago is missing")
			}
		})
	}
}

func TestApplyPoll_WorkingRecordIsShownHowEverOld(t *testing.T) {
	// A background session still at work is no finished record.
	h := newHarness(t, tracker.Config{})
	h.tr.ApplyPoll(dirA, at(0), []domain.Poll{backgroundRecord(0, s1, "working", at(0).Add(-72*time.Hour))})
	if _, ok := findSession(h.tr.Snapshot(), s1); !ok {
		t.Error("a background session started three days ago and still working is not shown")
	}
}

func TestApplyPoll_ExpiredRecordIsNotRecreated(t *testing.T) {
	// `claude agents --all` keeps listing a finished background session after
	// it has left the view.
	for _, bgState := range []string{"done", "blocked"} {
		t.Run(bgState, func(t *testing.T) {
			h := newHarness(t, tracker.Config{})
			startedAt := at(0).Add(-time.Hour)
			pollAt := func(d time.Duration) tracker.View {
				h.clock.Set(at(0).Add(d))
				record := backgroundRecord(0, s1, bgState, startedAt)
				record.At = h.clock.Now()
				h.tr.ApplyPoll(dirA, record.At, []domain.Poll{record})
				return h.tr.Snapshot()
			}
			if _, ok := findSession(pollAt(0), s1); !ok {
				t.Fatal("the record of a session that just finished is not shown")
			}
			if _, ok := findSession(pollAt(24*time.Hour), s1); ok {
				t.Fatal("the session is still shown a day after it was parked")
			}
			if _, ok := findSession(pollAt(24*time.Hour+5*time.Second), s1); ok {
				t.Error("the next poll brought the expired session back")
			}
		})
	}
}

func TestTracker_FinishedSessionsLeaveADayAfterTheyFinish(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	startedAt := at(0).Add(-time.Hour)
	h.tr.ApplyPoll(dirA, at(0), []domain.Poll{backgroundRecord(0, s1, "done", startedAt)})
	h.tr.Apply(hook(0, s2, "SessionStart"))
	h.tr.Apply(sessionEnd(10, s2, "prompt_input_exit"))
	h.tr.Apply(onDir(hook(0, s3, "Stop"), dirB))

	// The poll keeps listing the parked session, so its LastSeenAt stays
	// fresh; only the time it finished counts.
	pollAt := func(d time.Duration) {
		h.clock.Set(at(0).Add(d))
		record := backgroundRecord(0, s1, "done", startedAt)
		record.At = h.clock.Now()
		h.tr.ApplyPoll(dirA, record.At, []domain.Poll{record})
	}

	pollAt(24*time.Hour - time.Second)
	if got := sessionIDs(h.tr.Snapshot()); len(got) != 3 {
		t.Fatalf("just under a day later: sessions %v, want all three", got)
	}
	pollAt(24 * time.Hour)
	if got := sessionIDs(h.tr.Snapshot()); !slices.Equal(got, []domain.SessionID{s3, s2}) {
		t.Fatalf("a day after it was parked: sessions %v, want [s3 s2]", got)
	}
	pollAt(24*time.Hour + 10*time.Second)
	if got := sessionIDs(h.tr.Snapshot()); !slices.Equal(got, []domain.SessionID{s3}) {
		t.Errorf("a day after it ended: sessions %v, want only the idle s3", got)
	}
}

func TestTracker_LimitsNewestObservationWins(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	resets5h, resets7d := at(0).Add(3*time.Hour), at(0).Add(48*time.Hour)

	h.tr.Apply(withWindows(statusLine(10, s1), window(20, resets5h), window(50, resets7d)))
	// Another session's line, observed earlier but delivered later, is older
	// news.
	h.tr.Apply(withWindows(statusLine(5, s2), window(10, resets5h), window(40, resets7d)))
	a := h.account(dirA)
	if a.SevenDay == nil || a.SevenDay.UsedPct != 50 || !time.Time(a.SevenDay.ObservedAt).Equal(at(10)) {
		t.Fatalf("seven-day window %+v, want 50%% observed at +10s", a.SevenDay)
	}
	if a.FiveHour == nil || a.FiveHour.UsedPct != 20 || !time.Time(a.FiveHour.ResetsAt).Equal(resets5h) {
		t.Fatalf("five-hour window %+v, want 20%% resetting at %v", a.FiveHour, resets5h)
	}

	// A line without rate limits says nothing about them.
	h.tr.Apply(statusLine(15, s2))
	if a := h.account(dirA); a.SevenDay == nil || !time.Time(a.SevenDay.ObservedAt).Equal(at(10)) {
		t.Fatalf("after a line without limits: seven-day %+v, want the one observed at +10s", a.SevenDay)
	}

	// A newer line replaces both windows: one it lacks is gone.
	h.tr.Apply(withWindows(statusLine(20, s2), nil, window(55, resets7d)))
	a = h.account(dirA)
	if a.SevenDay == nil || a.SevenDay.UsedPct != 55 {
		t.Errorf("seven-day window %+v, want 55%%", a.SevenDay)
	}
	if a.FiveHour != nil {
		t.Errorf("five-hour window %+v, want none: the newest line has none", a.FiveHour)
	}
}

func TestTracker_ExpiredWindowsAreHidden(t *testing.T) {
	h := newHarness(t, tracker.Config{Alerts: defaultAlerts})
	h.tr.Apply(withWindows(statusLine(0, s1), window(30, at(3600)), window(95, at(60))))
	if got := h.tr.Snapshot().Alerts; len(got) != 1 || got[0].Kind != domain.AlertWeeklyThreshold {
		t.Fatalf("alerts %+v, want the weekly threshold", got)
	}

	h.clock.Set(at(60))
	v := h.tr.Snapshot()
	if a := h.account(dirA); a.SevenDay != nil || a.FiveHour == nil {
		t.Fatalf("once the seven-day window reset: seven-day %+v, five-hour %+v; want only the five-hour", a.SevenDay, a.FiveHour)
	}
	if len(v.Alerts) != 0 {
		t.Fatalf("alerts %+v after the window reset, want none", v.Alerts)
	}

	// A newer line that reports the window again brings it back.
	h.tr.Apply(withWindows(statusLine(70, s1), nil, window(3, at(0).Add(7*24*time.Hour))))
	if a := h.account(dirA); a.SevenDay == nil || a.SevenDay.UsedPct != 3 {
		t.Errorf("seven-day window %+v, want 3%%", a.SevenDay)
	}
}

func TestTracker_WindowWithoutResetTimeHasNoProjection(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	for i, pct := range []float64{10, 20, 30, 40} {
		sec := i * 300
		h.clock.Set(at(sec))
		h.tr.Apply(withWindows(statusLine(sec, s1), nil, window(pct, time.Time{})))
	}

	h.clock.Set(at(0).Add(30 * 24 * time.Hour))
	got := h.account(dirA).SevenDay
	if got == nil {
		t.Fatal("a window with an unknown reset time was hidden")
	}
	if !time.Time(got.ResetsAt).IsZero() || got.Projection != nil {
		t.Errorf("window %+v, want an unknown reset and no projection", got)
	}
}

func TestTracker_ProjectionFromSamples(t *testing.T) {
	h := newHarness(t, tracker.Config{Alerts: domain.AlertConfig{WeeklyPct: 100, ContextPct: 100, NeedsYouAfter: time.Hour}})
	resets := at(0).Add(48 * time.Hour)
	// One point every five minutes is 12 points an hour.
	for i, pct := range []float64{80, 81, 82, 83} {
		sec := i * 300
		h.clock.Set(at(sec))
		h.tr.Apply(withWindows(statusLine(sec, s1), nil, window(pct, resets)))
	}

	v := h.tr.Snapshot()
	p := h.account(dirA).SevenDay.Projection
	if p == nil {
		t.Fatal("no projection from four samples over 15 minutes")
	}
	wantETA := at(900).Add(time.Duration(17.0 / 12 * float64(time.Hour)))
	if !near(p.RatePerHour, 12) || !near(p.AtReset, 100) || !p.RunsOutBeforeReset ||
		time.Time(p.ETA).Sub(wantETA).Abs() > time.Second {
		t.Errorf("projection %+v, want 12/h reaching 100 at %v, before the reset", p, wantETA)
	}
	if len(v.Alerts) != 1 || v.Alerts[0].Kind != domain.AlertRunsOut || v.Alerts[0].Dir != dirA {
		t.Errorf("alerts %+v, want runs_out for %s", v.Alerts, dirA)
	}
}

func TestTracker_LimitSamplesOnChangeOrEveryMinute(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	resets5h, resets7d := at(0).Add(3*time.Hour), at(0).Add(48*time.Hour)
	h.tr.Apply(withWindows(statusLine(0, s1), window(20, resets5h), window(50, resets7d)))
	h.tr.Apply(withWindows(statusLine(30, s1), window(20, resets5h), window(50, resets7d)))
	h.tr.Apply(withWindows(statusLine(40, s1), window(20, resets5h), window(51, resets7d)))
	h.tr.Apply(withWindows(statusLine(60, s1), window(20, resets5h), window(51, resets7d)))

	type sample struct {
		at                 time.Time
		fiveHour, sevenDay float64 // -1 when the window gained no sample
	}
	pct := func(w *domain.Window) float64 {
		if w == nil {
			return -1
		}
		return w.UsedPct
	}
	_, _, recorded, _ := h.rec.recorded()
	got := make([]sample, len(recorded))
	for i, r := range recorded {
		if r.dir != dirA {
			t.Errorf("sample %d is for %s, want %s", i, r.dir, dirA)
		}
		got[i] = sample{r.limits.ObservedAt, pct(r.limits.FiveHour), pct(r.limits.SevenDay)}
	}
	want := []sample{
		{at(0), 20, 50},  // the first line samples both windows
		{at(40), -1, 51}, // a change samples at once
		{at(60), 20, -1}, // an unchanged window a minute after its last sample
	}
	if !slices.Equal(got, want) {
		t.Errorf("samples\n got %v\nwant %v", got, want)
	}
}

func TestTracker_RecordAPIBeforeSessionAttachesLater(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.RecordAPI(request(at(0), s1, "Opus 5.5", 0.5))
	if v := h.tr.Snapshot(); len(v.Models) != 0 || len(v.Sessions) != 0 {
		t.Fatalf("before its session is known, the request shows: models %v, sessions %v", v.Models, v.Sessions)
	}
	if _, _, _, usage := h.rec.recorded(); len(usage) != 0 {
		t.Fatalf("recorded %v before the request has an account", usage)
	}

	h.clock.Set(at(60))
	h.tr.Apply(hook(60, s1, "SessionStart"))
	want := []tracker.ModelTotal{{Model: "Opus 5.5", Input: 100, Output: 50, CacheRead: 1000, CacheWrite: 10, CostUSD: 0.5, Requests: 1}}
	if got := h.session(s1).Models; !slices.Equal(got, want) {
		t.Errorf("session models %v, want %v", got, want)
	}
	if got := h.account(dirA).Models; !slices.Equal(got, want) {
		t.Errorf("account models today %v, want %v", got, want)
	}
	if _, _, _, usage := h.rec.recorded(); len(usage) != 1 || usage[0].dir != dirA || usage[0].request.Session != s1 {
		t.Errorf("recorded usage %v, want the request under %s", usage, dirA)
	}

	// A request waits ten minutes for its session, and no longer.
	h.tr.RecordAPI(request(at(60), s2, "Opus 5.5", 1))
	h.clock.Set(at(61))
	h.tr.RecordAPI(request(at(61), s3, "Opus 5.5", 1))
	h.clock.Set(at(60).Add(10 * time.Minute))
	h.tr.Apply(hook(660, s2, "SessionStart"))
	h.clock.Set(at(61).Add(10*time.Minute + time.Second))
	h.tr.Apply(hook(662, s3, "SessionStart"))
	if got := h.session(s2).Models; len(got) != 1 {
		t.Errorf("a request that waited ten minutes: models %v, want it attached", got)
	}
	if got := h.session(s3).Models; len(got) != 0 {
		t.Errorf("a request that waited longer than ten minutes: models %v, want none", got)
	}
}

func TestTracker_PendingRequestsExpireOneByOne(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.RecordAPI(request(at(0), s1, "Opus 5.5", 1))
	h.clock.Set(at(300))
	h.tr.RecordAPI(request(at(300), s1, "Sonnet 5.5", 2))
	h.clock.Set(at(660))
	h.tr.Snapshot()
	h.tr.Apply(hook(660, s1, "SessionStart"))
	if got, want := costsOf(h.session(s1).Models), []modelCost{{"Sonnet 5.5", 2, 1}}; !slices.Equal(got, want) {
		t.Errorf("models %v, want only the request that waited under ten minutes %v", got, want)
	}
}

func TestTracker_RequestWaitingForItsSessionPublishesNothing(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	views, cancel := h.tr.Subscribe()
	defer cancel()
	h.tr.Apply(hook(0, s1, "SessionStart"))
	receive(t, views)

	h.clock.Set(at(1))
	h.tr.RecordAPI(request(at(1), s2, "Opus 5.5", 1))
	assertNoView(t, views)
	if got := h.ticker.pending(); len(got) != 0 {
		t.Fatalf("pending timers %v after a request that changes nothing shown, want none", got)
	}

	h.tr.RecordAPI(request(at(1), s1, "Opus 5.5", 1))
	if v := receive(t, views); len(v.Models) != 1 {
		t.Errorf("models %v after a request of a known session, want it shown", v.Models)
	}
}

func TestTracker_ExpiredRequestsFreeTheirPlace(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	const limit = 10000
	for range limit {
		h.tr.RecordAPI(request(at(0), s1, "Opus 5.5", 0.01))
	}
	h.clock.Set(at(660))
	h.tr.Snapshot()
	h.tr.RecordAPI(request(at(660), s2, "Opus 5.5", 1))
	h.tr.Apply(hook(661, s2, "SessionStart"))
	if got := h.session(s2).Models; len(got) != 1 {
		t.Errorf("models %v, want the request that arrived after the full queue expired", got)
	}
}

func TestTracker_PendingRequestsAreBounded(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	const limit = 10000
	for range limit + 5 {
		h.tr.RecordAPI(request(at(0), s1, "Opus 5.5", 0.01))
	}
	h.tr.Apply(hook(1, s1, "SessionStart"))
	if got := h.session(s1).Models; len(got) != 1 || got[0].Requests != limit {
		t.Errorf("models %v, want %d requests kept while the session was unknown", got, limit)
	}
}

func TestTracker_RecordAPIFillsMissingEmail(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(hook(0, s1, "SessionStart"))
	req := request(at(1), s1, "Opus 5.5", 0.1)
	req.Email = "dev@example.com"
	h.tr.RecordAPI(req)
	if got := h.account(dirA); got.Email != "dev@example.com" || got.Label != "dev" {
		t.Errorf("account %+v, want the request's email and the label it gives", got)
	}

	// The probe's email stands.
	h.tr.SetAccount(domain.Account{Dir: dirB, Email: "owner@example.com"})
	h.tr.Apply(onDir(hook(0, s2, "SessionStart"), dirB))
	req = request(at(2), s2, "Opus 5.5", 0.1)
	req.Email = "other@example.com"
	h.tr.RecordAPI(req)
	if got := h.account(dirB).Email; got != "owner@example.com" {
		t.Errorf("email %q, want the probed owner@example.com", got)
	}
}

func TestTracker_SharesAndModelTotals(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(hook(-7200, s1, "SessionStart"))
	h.tr.Apply(hook(-7200, s2, "SessionStart"))
	h.tr.Apply(onDir(hook(-7200, s3, "SessionStart"), dirB))

	h.tr.RecordAPI(request(at(-600), s1, "Opus 5.5", 3))
	h.tr.RecordAPI(request(at(-300), s2, "Sonnet 5.5", 1))
	// Outside the hour the share covers, but still today.
	h.tr.RecordAPI(request(at(-3660), s1, "Opus 5.5", 2))
	h.tr.RecordAPI(request(at(-60), s3, "Haiku 5.5", 0.5))

	v := h.tr.Snapshot()
	for id, want := range map[domain.SessionID]float64{s1: 0.75, s2: 0.25, s3: 1} {
		s, _ := findSession(v, id)
		if !near(s.Share, want) {
			t.Errorf("share of %s = %v, want %v", id, s.Share, want)
		}
	}

	a := h.account(dirA)
	if want := []modelCost{{"Opus 5.5", 5, 2}, {"Sonnet 5.5", 1, 1}}; !slices.Equal(costsOf(a.Models), want) {
		t.Errorf("account models today %v, want %v", costsOf(a.Models), want)
	}
	if !near(a.SpendTodayUSD, 6) {
		t.Errorf("spend today %v, want 6", a.SpendTodayUSD)
	}
	want := []modelCost{{"Opus 5.5", 5, 2}, {"Sonnet 5.5", 1, 1}, {"Haiku 5.5", 0.5, 1}}
	if got := costsOf(v.Models); !slices.Equal(got, want) {
		t.Errorf("models today %v, want %v", got, want)
	}
	if got := costsOf(h.session(s1).Models); !slices.Equal(got, []modelCost{{"Opus 5.5", 5, 2}}) {
		t.Errorf("session models %v, want Opus 5.5 at 5 over 2 requests", got)
	}
}

func TestTracker_ModelTotalsTieBreakOnName(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(hook(0, s1, "SessionStart"))
	for _, model := range []string{"Sonnet 5.5", "Haiku 5.5", "Opus 5.5"} {
		h.tr.RecordAPI(request(at(1), s1, model, 1))
	}
	got := costsOf(h.tr.Snapshot().Models)
	want := []modelCost{{"Haiku 5.5", 1, 1}, {"Opus 5.5", 1, 1}, {"Sonnet 5.5", 1, 1}}
	if !slices.Equal(got, want) {
		t.Errorf("models %v, want equal costs ordered by name %v", got, want)
	}
}

func TestTracker_UsageByLocalDay(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(hook(0, s1, "SessionStart"))
	utc := func(month time.Month, day, hour, minute int) time.Time {
		return time.Date(2026, month, day, hour, minute, 0, 0, time.UTC)
	}
	// It is 10:00 on 21 November in testZone, eight hours behind UTC.
	h.tr.RecordAPI(request(utc(11, 21, 8, 30), s1, "Opus 5.5", 1))   // 00:30 today
	h.tr.RecordAPI(request(utc(11, 21, 7, 30), s1, "Opus 5.5", 2))   // 23:30 yesterday, though today in UTC
	h.tr.RecordAPI(request(utc(11, 15, 9, 0), s1, "Sonnet 5.5", 4))  // six days ago
	h.tr.RecordAPI(request(utc(11, 14, 9, 0), s1, "Haiku 5.5", 8))   // seven days ago
	h.tr.RecordAPI(request(utc(10, 23, 9, 0), s1, "Sonnet 5.5", 16)) // 29 days ago
	h.tr.RecordAPI(request(utc(10, 22, 9, 0), s1, "Sonnet 5.5", 32)) // 30 days ago: forgotten

	v := h.tr.Snapshot()
	a := h.account(dirA)
	if !near(a.SpendTodayUSD, 1) || !near(a.SpendWeekUSD, 7) || !near(a.SpendMonthUSD, 31) {
		t.Errorf("spend today %v, week %v, month %v; want 1, 7 and 31", a.SpendTodayUSD, a.SpendWeekUSD, a.SpendMonthUSD)
	}
	if got, want := costsOf(v.Models), []modelCost{{"Opus 5.5", 1, 1}}; !slices.Equal(got, want) {
		t.Errorf("models today %v, want %v", got, want)
	}
	if got, want := costsOf(v.ModelsWeek), []modelCost{{"Sonnet 5.5", 4, 1}, {"Opus 5.5", 3, 2}}; !slices.Equal(got, want) {
		t.Errorf("models this week %v, want %v", got, want)
	}
	if got, want := costsOf(a.ModelsWeek), []modelCost{{"Sonnet 5.5", 4, 1}, {"Opus 5.5", 3, 2}}; !slices.Equal(got, want) {
		t.Errorf("account models this week %v, want %v", got, want)
	}
	want := []modelCost{{"Sonnet 5.5", 20, 2}, {"Haiku 5.5", 8, 1}, {"Opus 5.5", 3, 2}}
	if got := costsOf(h.session(s1).Models); !slices.Equal(got, want) {
		t.Errorf("session lifetime models %v, want %v", got, want)
	}
}

func TestTracker_AlertsInView(t *testing.T) {
	h := newHarness(t, tracker.Config{Alerts: defaultAlerts, Budgets: map[domain.ConfigDir]float64{dirA: 1}})
	views, cancel := h.tr.Subscribe()
	defer cancel()

	h.tr.Apply(hook(0, s1, "PermissionRequest"))
	if v := receive(t, views); len(v.Alerts) != 0 {
		t.Fatalf("alerts %+v as the wait begins, want none", v.Alerts)
	}

	// While a session waits, the tracker looks again every second, so the
	// alert appears on time without another event.
	if got := h.ticker.pending(); !slices.Contains(got, time.Second) {
		t.Fatalf("pending timers %v, want a one-second recheck", got)
	}
	h.clock.Set(at(19))
	h.ticker.fire()
	assertNoView(t, views)
	h.clock.Set(at(20))
	h.ticker.fire()
	v := receive(t, views)
	want := tracker.AlertView{Kind: domain.AlertNeedsYou, Dir: dirA, Session: s1, Since: at(0), Message: "s1 needs you: permission prompt"}
	if len(v.Alerts) != 1 || v.Alerts[0] != want {
		t.Fatalf("alerts %+v after 20 s, want %+v", v.Alerts, want)
	}

	// Other alerts are dated by the first view that shows them.
	h.clock.Set(at(30))
	h.tr.Apply(withContext(statusLine(30, s2), 90))
	h.tr.RecordAPI(request(at(30), s2, "Opus 5.5", 1))
	h.tr.Snapshot()
	h.clock.Set(at(45))
	type alertAt struct {
		kind    domain.AlertKind
		session domain.SessionID
		since   time.Time
	}
	alertsOf := func(v tracker.View) []alertAt {
		out := make([]alertAt, len(v.Alerts))
		for i, a := range v.Alerts {
			out[i] = alertAt{a.Kind, a.Session, a.Since}
		}
		return out
	}
	wantAlerts := []alertAt{
		{domain.AlertNeedsYou, s1, at(0)},
		{domain.AlertBudget, "", at(30)},
		{domain.AlertContextHigh, s2, at(30)},
	}
	if got := alertsOf(h.tr.Snapshot()); !slices.Equal(got, wantAlerts) {
		t.Fatalf("alerts at +45s\n got %v\nwant %v", got, wantAlerts)
	}

	// An alert that stops firing is forgotten, so its return is dated anew.
	h.clock.Set(at(50))
	h.tr.Apply(withContext(statusLine(50, s2), 40))
	h.tr.Snapshot()
	h.clock.Set(at(60))
	h.tr.Apply(withContext(statusLine(60, s2), 95))
	got := alertsOf(h.tr.Snapshot())
	if len(got) != 3 || got[2] != (alertAt{domain.AlertContextHigh, s2, at(60)}) {
		t.Errorf("alerts after the context alert returned: %v, want it dated +60s", got)
	}
}

func TestTracker_NeedsYouAlertDatesEachWait(t *testing.T) {
	h := newHarness(t, tracker.Config{Alerts: defaultAlerts})
	h.tr.Apply(hook(0, s1, "PermissionRequest"))
	h.clock.Set(at(25))
	h.tr.Apply(hook(1, s2, "SessionStart"))
	if got := h.tr.Snapshot().Alerts; len(got) != 1 || !got[0].Since.Equal(at(0)) {
		t.Fatalf("alerts %+v, want needs_you since +0s", got)
	}

	// The user answers and the session asks again before the next view, so
	// the alert never stopped showing; it still dates from the new wait.
	h.tr.Apply(hook(26, s1, "PostToolUse"))
	h.tr.Apply(hook(27, s1, "PermissionRequest"))
	h.clock.Set(at(50))
	if got := h.tr.Snapshot().Alerts; len(got) != 1 || !got[0].Since.Equal(at(27)) {
		t.Errorf("alerts %+v, want needs_you since +27s", got)
	}
}

func TestTracker_AlertsKeepFirstSeenOrder(t *testing.T) {
	h := newHarness(t, tracker.Config{Alerts: defaultAlerts})
	const later, earlier domain.SessionID = "a-later", "z-earlier"
	h.tr.Apply(withContext(statusLine(0, earlier), 90))
	h.tr.Snapshot()
	h.clock.Set(at(10))
	h.tr.Apply(withContext(statusLine(10, later), 90))
	h.tr.Snapshot()

	h.clock.Set(at(20))
	v := h.tr.Snapshot()
	if len(v.Alerts) != 2 || v.Alerts[0].Session != earlier || v.Alerts[1].Session != later {
		t.Errorf("alerts %+v, want the one first seen earlier first", v.Alerts)
	}
}

func TestTracker_NeedsYouRecheckPublishesOnlyNewAlerts(t *testing.T) {
	h := newHarness(t, tracker.Config{Alerts: defaultAlerts})
	views, cancel := h.tr.Subscribe()
	defer cancel()
	h.tr.Apply(hook(0, s1, "PermissionRequest"))
	receive(t, views)

	// A recheck that finds the same alerts publishes nothing: the page counts
	// the wait from stateAt itself.
	h.clock.Set(at(10))
	h.ticker.fire()
	assertNoView(t, views)
	h.clock.Set(at(20))
	h.ticker.fire()
	if v := receive(t, views); len(v.Alerts) != 1 || v.Alerts[0].Kind != domain.AlertNeedsYou {
		t.Fatalf("alerts %+v after 20 s, want needs_you", v.Alerts)
	}

	// Once every waiting session has alerted there is nothing to recheck,
	// until another session starts waiting.
	if got := h.ticker.pending(); len(got) != 0 {
		t.Fatalf("pending timers %v once the alert is up, want none", got)
	}
	h.clock.Set(at(30))
	h.tr.Apply(hook(30, s2, "PermissionRequest"))
	if got := h.ticker.pending(); !slices.Equal(got, []time.Duration{time.Second}) {
		t.Fatalf("pending timers %v once another session waits, want a one-second recheck", got)
	}
	receive(t, views)
	h.clock.Set(at(31))
	h.ticker.fire()
	assertNoView(t, views)
	h.clock.Set(at(50))
	h.ticker.fire()
	if v := receive(t, views); len(v.Alerts) != 2 {
		t.Errorf("alerts %+v once the second session has waited 20 s, want both", v.Alerts)
	}
}

func TestTracker_NeedsYouRecheckStopsWhenNothingWaits(t *testing.T) {
	h := newHarness(t, tracker.Config{Alerts: defaultAlerts})
	h.tr.Apply(hook(0, s1, "PermissionRequest"))
	h.clock.Set(at(1))
	h.tr.Apply(hook(1, s1, "PostToolUse"))

	h.clock.Set(at(2))
	h.ticker.fire()
	if got := h.ticker.pending(); len(got) != 0 {
		t.Errorf("pending timers %v once nothing waits, want none", got)
	}
}

func TestTracker_SubscribeCoalescesAndNeverBlocks(t *testing.T) {
	// Nothing here may take the tracker's lock after a failure: if Apply is
	// stuck holding it, the test must fail rather than hang.
	h := newUnclosedHarness(t, tracker.Config{})
	views, cancel := h.tr.Subscribe()

	addSubagent := hook(0, s1, "SubagentStart")
	addSubagent.AgentID = "agent-1"
	h.tr.Apply(hook(0, s1, "SessionStart"))
	for range 999 {
		h.tr.Apply(addSubagent)
	}
	// The first change goes out at once. The rest wait out the interval and
	// go out together.
	first := receive(t, views)
	if s, _ := findSession(first, s1); s.Subagents != 0 {
		t.Fatalf("first view shows %d subagents, want 0", s.Subagents)
	}
	assertNoView(t, views)
	if got := h.ticker.pending(); !slices.Equal(got, []time.Duration{250 * time.Millisecond}) {
		t.Fatalf("pending timers %v, want one of 250ms", got)
	}
	h.clock.Advance(250 * time.Millisecond)
	h.ticker.fire()
	if s, _ := findSession(receive(t, views), s1); s.Subagents != 999 {
		t.Fatalf("coalesced view shows %d subagents, want 999", s.Subagents)
	}

	// A subscriber that never reads holds only the latest view, and never
	// holds up Apply.
	idle, cancelIdle := h.tr.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			h.clock.Advance(250 * time.Millisecond)
			h.tr.Apply(addSubagent)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Apply blocked on a subscriber that never reads")
	}
	if s, _ := findSession(receive(t, idle), s1); s.Subagents != 1999 {
		t.Errorf("the waiting view shows %d subagents, want the latest 1999", s.Subagents)
	}
	cancelIdle()
	cancel()
	h.tr.Close()
}

func TestTracker_ViewsWaitNoLongerThanTheInterval(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(hook(0, s1, "SessionStart"))
	// A clock without a monotonic reading can step back.
	h.clock.Set(at(-3600))
	h.tr.Apply(hook(1, s1, "UserPromptSubmit"))
	if got := h.ticker.pending(); len(got) != 1 || got[0] > 250*time.Millisecond {
		t.Errorf("pending timers %v after the clock stepped back, want one of at most 250ms", got)
	}
}

func TestTracker_UnsubscribeClosesTheChannel(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	views, cancel := h.tr.Subscribe()
	cancel()
	cancel()
	assertClosed(t, views)
	h.tr.Apply(hook(0, s1, "SessionStart"))
}

func TestTracker_CloseStopsTimersAndSubscriptions(t *testing.T) {
	h := newHarness(t, tracker.Config{Alerts: defaultAlerts})
	views, _ := h.tr.Subscribe()
	h.tr.Apply(hook(0, s1, "PermissionRequest"))
	h.tr.Apply(hook(0, s2, "SessionStart"))
	if got := h.ticker.pending(); len(got) != 2 {
		t.Fatalf("pending timers %v, want the recheck and the coalesced view", got)
	}

	h.tr.Close()
	if got := h.ticker.pending(); len(got) != 0 {
		t.Errorf("pending timers %v after Close, want none", got)
	}
	receive(t, views) // the view published before Close
	assertClosed(t, views)

	// A timer that fired just as Close stopped it, events, and new
	// subscribers all find the tracker closed.
	h.ticker.runAll()
	h.tr.Apply(hook(5, s1, "PostToolUse"))
	if got := h.ticker.pending(); len(got) != 0 {
		t.Errorf("pending timers %v after an event on a closed tracker, want none", got)
	}
	late, cancel := h.tr.Subscribe()
	defer cancel()
	assertClosed(t, late)
	h.tr.Close()
}

func TestTracker_WallClockTimersPublishTheCoalescedView(t *testing.T) {
	tr := tracker.New(time.Now, tracker.Config{}, tracker.NopRecorder{})
	defer tr.Close()
	views, cancel := tr.Subscribe()
	defer cancel()

	start := domain.Hook{At: time.Now(), Dir: dirA, Session: s1, PID: pidA, Name: "SessionStart"}
	tr.Apply(start)
	sub := start
	sub.Name, sub.AgentID = "SubagentStart", "agent-1"
	tr.Apply(sub)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case v := <-views:
			if s, ok := findSession(v, s1); ok && s.Subagents == 1 {
				return
			}
		case <-deadline:
			t.Fatal("the coalesced view never arrived")
		}
	}
}

func TestTracker_RecorderCalled(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	identity := domain.Account{Dir: dirA, Email: "dev@example.com", Plan: "Max 20x", Auth: domain.AuthSubscription, ProbedAt: at(-60)}
	h.tr.SetAccount(identity)
	h.tr.Apply(hook(0, s1, "SessionStart"))
	resets := at(0).Add(48 * time.Hour)
	h.tr.Apply(withWindows(statusLine(5, s1), nil, window(50, resets)))
	req := request(at(6), s1, "Opus 5.5", 0.2)
	h.tr.RecordAPI(req)
	// A stale hook changes nothing, so nothing is reported.
	h.tr.Apply(hook(4, s1, "Stop"))
	h.tr.Apply(onDir(hook(7, s2, "SessionStart"), dirB))

	sessions, accounts, samples, usage := h.rec.recorded()
	type sessionAt struct {
		id    domain.SessionID
		state domain.State
	}
	gotSessions := make([]sessionAt, len(sessions))
	for i, s := range sessions {
		gotSessions[i] = sessionAt{s.ID, s.State}
	}
	wantSessions := []sessionAt{{s1, domain.StateStarting}, {s1, domain.StateIdle}, {s2, domain.StateStarting}}
	if !slices.Equal(gotSessions, wantSessions) {
		t.Errorf("sessions recorded %v, want %v", gotSessions, wantSessions)
	}

	if len(accounts) != 3 {
		t.Fatalf("accounts recorded %+v, want the probe, the new limits and the new account", accounts)
	}
	if accounts[0] != identity {
		t.Errorf("first account %+v, want the probe %+v", accounts[0], identity)
	}
	if got := accounts[1]; got.Email != identity.Email || got.Limits.SevenDay == nil || got.Limits.SevenDay.UsedPct != 50 || !got.Limits.ObservedAt.Equal(at(5)) {
		t.Errorf("second account %+v, want the probe with the 50%% window observed at +5s", got)
	}
	if got := accounts[2]; got.Dir != dirB || got.Email != "" {
		t.Errorf("third account %+v, want the bare %s", got, dirB)
	}

	if len(samples) != 1 || samples[0].dir != dirA || samples[0].limits.FiveHour != nil ||
		samples[0].limits.SevenDay == nil || samples[0].limits.SevenDay.UsedPct != 50 {
		t.Errorf("samples recorded %+v, want the seven-day window at 50%%", samples)
	}
	if len(usage) != 1 || usage[0] != (usageRecord{dirA, req}) {
		t.Errorf("usage recorded %+v, want the request under %s", usage, dirA)
	}
}

func restoredSession(id domain.SessionID, state domain.State, lastStatusAt time.Time) domain.Session {
	return domain.Session{
		ID: id, Dir: dirA, PID: pidA, State: state,
		StateAt: lastStatusAt, StartedAt: lastStatusAt.Add(-time.Hour),
		LastSeenAt: lastStatusAt, LastStatusAt: lastStatusAt,
	}
}

func TestTracker_RestoreThenPollMarksMissingEnded(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	gone := restoredSession(s1, domain.StateIdle, at(-3600))
	alive := restoredSession(s2, domain.StateWorking, at(-3600))
	h.tr.Restore(tracker.Restored{Sessions: []domain.Session{gone, alive}})
	if got := h.tr.KnownDirs(); !slices.Equal(got, []domain.ConfigDir{dirA}) {
		t.Fatalf("known dirs %v after restore, want [%s]", got, dirA)
	}

	h.tr.ApplyPoll(dirA, at(0), []domain.Poll{busyPoll(0, s2)})
	if got := h.session(s1); got.State != domain.StateEnded || !got.Lost {
		t.Errorf("a restored session missing from the poll: %v (lost %v), want ended and lost", got.State, got.Lost)
	}
	if got := h.session(s2).State; got != domain.StateWorking {
		t.Errorf("a restored session the poll lists: %v, want working", got)
	}
}

func TestTracker_RestoreRebuildsUsageSamplesAndTimelines(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	resets := at(0).Add(48 * time.Hour)
	h.tr.Restore(tracker.Restored{
		Accounts: []domain.Account{{
			Dir: dirA, Email: "dev@example.com", Auth: domain.AuthSubscription,
			Limits: domain.Limits{SevenDay: window(78, resets), ObservedAt: at(-600)},
		}},
		Sessions: []domain.Session{restoredSession(s1, domain.StateIdle, at(-60))},
		Samples: map[domain.ConfigDir]tracker.WindowSamples{dirA: {SevenDay: []domain.Sample{
			{At: at(-1800), UsedPct: 74}, {At: at(-1200), UsedPct: 76}, {At: at(-600), UsedPct: 78},
		}}},
		Usage: []tracker.UsageRow{
			{Minute: at(-300), Dir: dirA, Session: s1, Model: "Opus 5.5", CostUSD: 1, Requests: 2},
			// 23:59 yesterday in testZone, though today in UTC.
			{Minute: time.Date(2026, 11, 21, 7, 59, 0, 0, time.UTC), Dir: dirA, Session: s1, Model: "Opus 5.5", CostUSD: 4, Requests: 1},
		},
		Timelines: map[domain.SessionID][]domain.Transition{s1: {
			{At: at(-7200), State: domain.StateStarting},
			{At: at(-60), State: domain.StateIdle},
		}},
	})

	a := h.account(dirA)
	if a.Email != "dev@example.com" || a.SevenDay == nil || a.SevenDay.UsedPct != 78 {
		t.Fatalf("account %+v, want the restored email and window", a)
	}
	if p := a.SevenDay.Projection; p == nil || !near(p.RatePerHour, 12) {
		t.Errorf("projection %+v, want 12/h from the restored samples", p)
	}
	if !near(a.SpendTodayUSD, 1) || !near(a.SpendWeekUSD, 5) {
		t.Errorf("spend today %v and this week %v, want 1 and 5", a.SpendTodayUSD, a.SpendWeekUSD)
	}
	s := h.session(s1)
	if got := costsOf(s.Models); !slices.Equal(got, []modelCost{{"Opus 5.5", 5, 3}}) {
		t.Errorf("session models %v, want Opus 5.5 at 5 over 3 requests", got)
	}
	// A restored minute counts toward the share at its minute.
	if !near(s.Share, 1) {
		t.Errorf("share %v, want 1", s.Share)
	}

	h.tr.Apply(hook(0, s1, "UserPromptSubmit"))
	want := []tracker.TransitionView{
		{At: at(-7200), State: domain.StateStarting},
		{At: at(-60), State: domain.StateIdle},
		{At: at(0), State: domain.StateWorking},
	}
	if got := h.session(s1).Timeline; !slices.Equal(got, want) {
		t.Errorf("timeline\n got %v\nwant %v", got, want)
	}
}

func TestTracker_RestoreKeepsWhatIsAlreadyKnown(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.SetAccount(domain.Account{Dir: dirA, Email: "dev@example.com"})
	h.tr.Apply(hook(0, s1, "UserPromptSubmit"))
	h.tr.Restore(tracker.Restored{
		Accounts: []domain.Account{{Dir: dirA, Email: "stale@example.com"}},
		Sessions: []domain.Session{restoredSession(s1, domain.StateIdle, at(-60))},
	})
	if got := h.account(dirA).Email; got != "dev@example.com" {
		t.Errorf("email %q, want the live dev@example.com", got)
	}
	if got := h.session(s1).State; got != domain.StateWorking {
		t.Errorf("state %v, want the live working", got)
	}
}

func TestTracker_RestoreSkipsUndatedState(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	noStart := restoredSession(s1, domain.StateIdle, at(-60))
	noStart.StartedAt = time.Time{}
	noStateAt := restoredSession(s2, domain.StateIdle, at(-60))
	noStateAt.StateAt = time.Time{}
	neverSeen := restoredSession("s4", domain.StateIdle, at(-60))
	neverSeen.LastSeenAt = time.Time{}
	dated := restoredSession(s3, domain.StateIdle, at(-60))
	h.tr.Restore(tracker.Restored{
		Sessions: []domain.Session{noStart, noStateAt, neverSeen, dated},
		Timelines: map[domain.SessionID][]domain.Transition{s3: {
			{State: domain.StateStarting},
			{At: at(-60), State: domain.StateIdle},
		}},
	})

	v := h.tr.Snapshot()
	if got := sessionIDs(v); !slices.Equal(got, []domain.SessionID{s3}) {
		t.Fatalf("sessions %v, want only the dated s3", got)
	}
	want := []tracker.TransitionView{{At: at(-60), State: domain.StateIdle}}
	if got := v.Sessions[0].Timeline; !slices.Equal(got, want) {
		t.Errorf("timeline %v, want only the dated change %v", got, want)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("0001-01-01")) {
		t.Errorf("the view holds a zero time: %s", data)
	}
}

func TestTracker_RestoreAttachesWaitingRequests(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.RecordAPI(request(at(-30), s1, "Opus 5.5", 2))
	h.tr.Restore(tracker.Restored{Sessions: []domain.Session{restoredSession(s1, domain.StateIdle, at(-60))}})
	if got := costsOf(h.session(s1).Models); !slices.Equal(got, []modelCost{{"Opus 5.5", 2, 1}}) {
		t.Errorf("models %v, want the request that waited for the restored session", got)
	}
}

func TestTracker_TimelineRecordsStateChanges(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(hook(0, s1, "SessionStart"))
	h.tr.Apply(hook(1, s1, "PermissionRequest"))
	h.tr.Apply(hook(2, s1, "PermissionRequest")) // no change, no entry
	h.tr.Apply(sessionEnd(3, s1, "clear"))
	want := []tracker.TransitionView{
		{At: at(0), State: domain.StateStarting},
		{At: at(1), State: domain.StateNeedsYou, Reason: "permission prompt"},
		{At: at(3), State: domain.StateEnded, Reason: "clear"},
	}
	if got := h.session(s1).Timeline; !slices.Equal(got, want) {
		t.Errorf("timeline\n got %v\nwant %v", got, want)
	}

	// Only the last 50 changes are kept.
	h.tr.Apply(hook(0, s2, "SessionStart"))
	for i := range 60 {
		h.tr.Apply(hook(10+2*i, s2, "UserPromptSubmit"))
		h.tr.Apply(hook(11+2*i, s2, "Stop"))
	}
	got := h.session(s2).Timeline
	if len(got) != 50 {
		t.Fatalf("timeline holds %d changes, want 50", len(got))
	}
	if first, last := got[0], got[49]; first.At != at(80) || first.State != domain.StateWorking || last.At != at(129) || last.State != domain.StateIdle {
		t.Errorf("timeline runs from %+v to %+v, want working at +80s to idle at +129s", first, last)
	}
}

func TestTracker_ClearLinksSuccessor(t *testing.T) {
	const newer domain.SessionID = "s1-after-clear"
	cases := []struct {
		name   string
		events []domain.Event
		linked bool
	}{
		{"end then start", []domain.Event{sessionEnd(100, s1, "clear"), hook(101, newer, "SessionStart")}, true},
		{"start delivered first", []domain.Event{hook(101, newer, "SessionStart"), sessionEnd(100, s1, "clear")}, true},
		{"ten seconds apart", []domain.Event{sessionEnd(100, s1, "clear"), hook(110, newer, "SessionStart")}, true},
		{"process known from a later hook", []domain.Event{
			sessionEnd(100, s1, "clear"), statusLine(101, newer), hook(130, newer, "UserPromptSubmit"),
		}, true},
		{"over ten seconds apart", []domain.Event{sessionEnd(100, s1, "clear"), hook(111, newer, "SessionStart")}, false},
		{"another process", []domain.Event{sessionEnd(100, s1, "clear"), inProcessB(hook(101, newer, "SessionStart"))}, false},
		{"another account", []domain.Event{sessionEnd(100, s1, "clear"), onDir(hook(101, newer, "SessionStart"), dirB)}, false},
		{"ended for another reason", []domain.Event{sessionEnd(100, s1, "prompt_input_exit"), hook(101, newer, "SessionStart")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tracker.Config{})
			h.tr.Apply(hook(0, s1, "SessionStart"))
			for _, e := range tc.events {
				h.tr.Apply(e)
			}
			v := h.tr.Snapshot()
			successor, _ := findSession(v, newer)
			_, oldShown := findSession(v, s1)
			if tc.linked && (successor.Replaces != s1 || oldShown) {
				t.Errorf("replaces %q, cleared session shown %v; want it to replace %s, which is hidden", successor.Replaces, oldShown, s1)
			}
			if !tc.linked && (successor.Replaces != "" || !oldShown) {
				t.Errorf("replaces %q, cleared session shown %v; want no link and both shown", successor.Replaces, oldShown)
			}
		})
	}
}

func TestTracker_EventsThatOpenNoSession(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	// A status line without a session still reports its account's limits.
	h.tr.Apply(withWindows(statusLine(0, ""), nil, window(50, at(0).Add(48*time.Hour))))
	// An absence says nothing about a session the tracker never saw.
	h.tr.Apply(domain.Absent{At: at(1), Dir: dirA, Session: s1})

	v := h.tr.Snapshot()
	if len(v.Sessions) != 0 {
		t.Errorf("sessions %v, want none", sessionIDs(v))
	}
	if a := h.account(dirA); a.SevenDay == nil || a.SevenDay.UsedPct != 50 {
		t.Errorf("seven-day window %+v, want 50%%", a.SevenDay)
	}
}

func TestTracker_SessionOrder(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(hook(1, "ended", "SessionEnd"))
	h.tr.Apply(backgroundRecord(2, "parked", "done", at(0)))
	h.tr.Apply(hook(3, "idle", "Stop"))
	h.tr.Apply(hook(4, "starting", "SessionStart"))
	h.tr.Apply(hook(5, "error", "StopFailure"))
	h.tr.Apply(hook(6, "compacting", "PreCompact"))
	h.tr.Apply(hook(7, "working-older", "UserPromptSubmit"))
	h.tr.Apply(hook(8, "working-newer", "UserPromptSubmit"))
	h.tr.Apply(hook(9, "needs-you", "PermissionRequest"))

	want := []domain.SessionID{
		"needs-you", "working-newer", "working-older", "compacting", "error", "starting", "idle", "parked", "ended",
	}
	if got := sessionIDs(h.tr.Snapshot()); !slices.Equal(got, want) {
		t.Errorf("order\n got %v\nwant %v", got, want)
	}
}

func TestTracker_AccountLabels(t *testing.T) {
	const dirD domain.ConfigDir = "/home/dev/.claude-d"
	h := newHarness(t, tracker.Config{Labels: map[domain.ConfigDir]string{dirA: "Main"}})
	h.tr.SetAccount(domain.Account{Dir: dirA, Label: "Probed", Email: "dev@example.com"})
	h.tr.SetAccount(domain.Account{Dir: dirB, Email: "other@example.com"})
	h.tr.SetAccount(domain.Account{Dir: dirD, Label: "Given"})
	h.tr.Apply(onDir(hook(0, s1, "SessionStart"), dirB))
	h.tr.Apply(onDir(hook(0, s2, "SessionStart"), dirC))

	for dir, want := range map[domain.ConfigDir]string{dirA: "Main", dirB: "other", dirC: ".claude-work", dirD: "Given"} {
		if got := h.account(dir).Label; got != want {
			t.Errorf("label of %s = %q, want %q", dir, got, want)
		}
	}
	if got := h.session(s1).Account; got != "other" {
		t.Errorf("session account %q, want its account's label", got)
	}
}

func TestTracker_SessionFigures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		size, used int64
		want       int64
	}{
		{"room left", 200000, 50000, 150000},
		{"over full", 200000, 250000, 0},
		{"unknown size", 0, 10000, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tracker.Config{})
			st := statusLine(0, s1)
			st.Metrics.ContextSize, st.Metrics.ContextUsed = tc.size, tc.used
			h.tr.Apply(st)
			if got := h.session(s1).ContextLeft; got != tc.want {
				t.Errorf("context left %d, want %d", got, tc.want)
			}
		})
	}

	h := newHarness(t, tracker.Config{})
	st := statusLine(0, s1)
	st.Metrics.ModelID = "claude-opus-5-5"
	h.tr.Apply(st)
	if got := h.session(s1).Model; got != "claude-opus-5-5" {
		t.Errorf("model %q without a display name, want the model ID", got)
	}
}

func TestTracker_AccountChangesOnlyWithItsValues(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	views, cancel := h.tr.Subscribe()
	defer cancel()
	probe := domain.Account{Dir: dirA, Email: "dev@example.com", Plan: "Pro", Auth: domain.AuthSubscription, ProbedAt: at(0)}
	h.tr.SetAccount(probe)
	receive(t, views)

	// The probe six hours later finds the same account.
	h.clock.Set(at(1))
	probe.ProbedAt = at(1)
	h.tr.SetAccount(probe)
	assertNoView(t, views)
	// The organization is kept but not shown.
	probe.Org = "Example"
	h.tr.SetAccount(probe)
	assertNoView(t, views)

	resets5h, resets7d := at(0).Add(3*time.Hour), at(0).Add(48*time.Hour)
	h.tr.Apply(withWindows(statusLine(2, s1), nil, window(50, resets7d)))
	// The same windows observed again are only fresher.
	h.tr.Apply(withWindows(statusLine(3, s1), nil, window(50, resets7d)))
	if got := h.account(dirA).SevenDay; got == nil || !time.Time(got.ObservedAt).Equal(at(3)) {
		t.Errorf("seven-day window %+v, want it observed at +3s", got)
	}
	h.tr.Apply(withWindows(statusLine(4, s1), nil, window(51, resets7d)))
	h.tr.Apply(withWindows(statusLine(5, s1), window(10, resets5h), window(51, resets7d)))
	probe.Plan = "Max 20x"
	h.tr.SetAccount(probe)

	_, accounts, _, _ := h.rec.recorded()
	type change struct {
		plan               string
		fiveHour, sevenDay float64 // -1 when the window is absent
	}
	pct := func(w *domain.Window) float64 {
		if w == nil {
			return -1
		}
		return w.UsedPct
	}
	got := make([]change, len(accounts))
	for i, a := range accounts {
		got[i] = change{a.Plan, pct(a.Limits.FiveHour), pct(a.Limits.SevenDay)}
	}
	want := []change{
		{"Pro", -1, -1},     // the first probe
		{"Pro", -1, -1},     // the organization
		{"Pro", -1, 50},     // the first window
		{"Pro", -1, 51},     // its usage moved
		{"Pro", 10, 51},     // another window appeared
		{"Max 20x", 10, 51}, // the plan changed
	}
	if !slices.Equal(got, want) {
		t.Errorf("accounts recorded\n got %v\nwant %v", got, want)
	}
}

func TestTracker_SetAccountKeepsLimitsAndKnownEmail(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	h.tr.Apply(withWindows(statusLine(0, s1), nil, window(50, at(0).Add(48*time.Hour))))
	h.tr.SetAccount(domain.Account{Dir: dirA, Email: "dev@example.com", Plan: "Pro", Auth: domain.AuthSubscription})
	h.tr.SetAccount(domain.Account{Dir: dirA, Plan: "Max 20x", Auth: domain.AuthSubscription})

	a := h.account(dirA)
	if a.SevenDay == nil || a.SevenDay.UsedPct != 50 {
		t.Errorf("seven-day window %+v, want the status line's 50%%", a.SevenDay)
	}
	if a.Email != "dev@example.com" || a.Plan != "Max 20x" || a.Auth != domain.AuthSubscription {
		t.Errorf("account %+v, want the known email with the newest plan", a)
	}
}

func TestTracker_HasLiveAndKnownDirs(t *testing.T) {
	h := newHarness(t, tracker.Config{})
	if h.tr.HasLive() || len(h.tr.KnownDirs()) != 0 {
		t.Fatalf("a new tracker: live %v, dirs %v; want nothing", h.tr.HasLive(), h.tr.KnownDirs())
	}

	parked := backgroundRecord(0, s1, "done", at(-60))
	parked.Dir = dirB
	h.tr.ApplyPoll(dirB, at(0), []domain.Poll{parked})
	if h.tr.HasLive() {
		t.Error("a parked session counts as live")
	}
	h.tr.Apply(hook(1, s2, "SessionStart"))
	if !h.tr.HasLive() {
		t.Error("a starting session does not count as live")
	}
	if got := h.tr.KnownDirs(); !slices.Equal(got, []domain.ConfigDir{dirA, dirB}) {
		t.Errorf("known dirs %v, want [%s %s]", got, dirA, dirB)
	}
	h.tr.Apply(sessionEnd(2, s2, "prompt_input_exit"))
	if h.tr.HasLive() {
		t.Error("an ended session counts as live")
	}
}
