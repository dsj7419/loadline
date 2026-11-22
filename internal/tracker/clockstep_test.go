package tracker_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/tracker"
)

// A time sync that corrects a clock running an hour fast sets it back by the
// hour. Everything stamped before the correction is then dated in the future,
// and would make all that is observed after it look stale. The tracker moves
// what it holds back by the step.

const hour = 3600

// logBuffer collects what a tracker logs.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newSteppingHarness(t *testing.T) (*harness, *logBuffer) {
	t.Helper()
	logs := &logBuffer{}
	h := newHarness(t, tracker.Config{Alerts: defaultAlerts, Log: slog.New(slog.NewTextHandler(logs, nil))})
	return h, logs
}

func TestTracker_ClockSetBackStillTakesWhatIsObservedAfter(t *testing.T) {
	h, logs := newSteppingHarness(t)
	h.clock.Set(at(hour))
	h.tr.Apply(hook(hour, s1, "PermissionRequest"))
	h.tr.Apply(withWindows(withContext(statusLine(hour, s1), 40), nil, window(50, at(48*hour))))

	h.clock.Set(at(5))
	h.tr.ApplyPoll(dirA, at(5), []domain.Poll{busyPoll(5, s1)})
	if got := h.session(s1); got.State != domain.StateWorking {
		t.Errorf("a busy poll after the step leaves the session %v, want working", got.State)
	}

	h.clock.Set(at(10))
	h.tr.Apply(withWindows(withContext(statusLine(10, s1), 90), nil, window(97, at(48*hour))))
	if got := h.session(s1); got.ContextPct != 90 {
		t.Errorf("a status line after the step leaves the context at %v%%, want 90%%", got.ContextPct)
	}
	if w := h.account(dirA).SevenDay; w == nil || w.UsedPct != 97 {
		t.Errorf("a status line after the step leaves the week at %+v, want 97%%", w)
	}

	h.clock.Set(at(60))
	h.tr.Apply(sessionEnd(60, s1, "prompt_input_exit"))
	got := h.session(s1)
	if got.State != domain.StateEnded || h.tr.HasLive() {
		t.Errorf("SessionEnd after the step leaves the session %v, live %t; want it ended", got.State, h.tr.HasLive())
	}
	for _, tr := range got.Timeline {
		if tr.At.After(at(60)) {
			t.Errorf("timeline entry %v dated %s, after the clock's %s", tr.State, tr.At, at(60))
		}
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Errorf("logged %d warnings, want one for the step:\n%s", n, logs.String())
	}
}

// Ten minutes back or less is taken as it is.
func TestTracker_ClockSetBackTenMinutesMovesNothing(t *testing.T) {
	h, logs := newSteppingHarness(t)
	h.clock.Set(at(600))
	h.tr.Apply(hook(600, s1, "PermissionRequest"))

	h.clock.Set(at(0))
	h.tr.ApplyPoll(dirA, at(0), []domain.Poll{busyPoll(0, s1)})

	if got := h.session(s1); got.State != domain.StateNeedsYou || !got.StateAt.Equal(at(600)) {
		t.Errorf("session %v since %s, want needs_you since %s as before", got.State, got.StateAt, at(600))
	}
	if logs.String() != "" {
		t.Errorf("logged %q, want nothing", logs.String())
	}
}

// A poll stamped before the step and applied after it is dated now, so that
// it does not date the session ahead of what follows.
func TestTracker_PollStampedBeforeTheStepIsDatedNow(t *testing.T) {
	h, _ := newSteppingHarness(t)
	h.clock.Set(at(hour))
	h.tr.Apply(hook(hour, s1, "UserPromptSubmit"))

	h.clock.Set(at(0))
	idle := busyPoll(hour+1, s1)
	idle.Status = "idle"
	h.tr.ApplyPoll(dirA, at(hour+1), []domain.Poll{idle})
	h.tr.ApplyPoll(dirA, at(5), []domain.Poll{busyPoll(5, s1)})

	if got := h.session(s1); got.State != domain.StateWorking {
		t.Errorf("session %v, want working: the poll after the late one is not stale", got.State)
	}
}

// So are the absences of a poll dated more than ten minutes ahead: a session
// it misses ends as of now, not ahead of everything that follows.
func TestTracker_PollDatedAheadDatesItsAbsencesNow(t *testing.T) {
	h, _ := newSteppingHarness(t)
	h.tr.Apply(statusLine(0, s1))

	h.clock.Set(at(60))
	h.tr.ApplyPoll(dirA, at(hour+60), nil)

	got := h.session(s1)
	if got.State != domain.StateEnded || !got.StateAt.Equal(at(60)) {
		t.Errorf("session %v since %s, want ended since %s", got.State, got.StateAt, at(60))
	}
}

func TestTracker_ClockSetBackExpiresWaitingCallsOnTime(t *testing.T) {
	h, _ := newSteppingHarness(t)
	h.clock.Set(at(hour))
	h.tr.RecordAPI(request(at(hour), s1, "Opus 5.5", 1))

	h.clock.Set(at(0))
	h.tr.Snapshot()
	h.clock.Set(at(11 * 60))
	h.tr.Apply(hook(11*60, s1, "SessionStart"))

	if got := h.session(s1).Models; len(got) != 0 {
		t.Errorf("session models %+v, want none: the call waited over ten minutes for it", got)
	}
}

func TestTracker_ClockSetBackKeepsSharesToTheirWindow(t *testing.T) {
	h, _ := newSteppingHarness(t)
	h.clock.Set(at(hour))
	h.tr.Apply(hook(hour, s1, "SessionStart"))
	h.tr.Apply(hook(hour, s2, "SessionStart"))
	h.tr.RecordAPI(request(at(hour), s1, "Opus 5.5", 1))

	h.clock.Set(at(0))
	h.tr.Snapshot()
	h.clock.Set(at(61 * 60))
	h.tr.RecordAPI(request(at(61*60), s2, "Opus 5.5", 1))

	if got := h.session(s1).Share; got != 0 {
		t.Errorf("s1's share is %v, want 0: its spend is more than an hour old", got)
	}
	if got := h.session(s2).Share; got != 1 {
		t.Errorf("s2's share is %v, want 1", got)
	}
}

func TestTracker_ClockSetBackKeepsAlertsDatedInThePast(t *testing.T) {
	h, _ := newSteppingHarness(t)
	h.clock.Set(at(hour))
	h.tr.Apply(withWindows(statusLine(hour, s1), nil, window(95, at(48*hour))))
	h.tr.Snapshot()

	h.clock.Set(at(5))
	for _, a := range h.tr.Snapshot().Alerts {
		if a.Since.After(at(5)) {
			t.Errorf("%s alert dated %s, after the clock's %s", a.Kind, a.Since, at(5))
		}
	}
}

// The week's usage is sampled once a minute while it holds steady.
func TestTracker_ClockSetBackKeepsSamplingTheLimits(t *testing.T) {
	h, _ := newSteppingHarness(t)
	h.clock.Set(at(hour))
	h.tr.Apply(withWindows(statusLine(hour, s1), nil, window(50, at(48*hour))))

	h.clock.Set(at(0))
	h.tr.Snapshot()
	h.clock.Set(at(61))
	h.tr.Apply(withWindows(statusLine(61, s1), nil, window(50, at(48*hour))))

	if _, _, samples, _ := h.rec.recorded(); len(samples) != 2 {
		t.Errorf("recorded %d samples, want 2: a minute passed between the status lines", len(samples))
	}
}

// A window with no known reset takes a lower reading once the one held was
// first taken longer ago than the window lasts, as the step moved it.
func TestTracker_ClockSetBackKeepsAReadingsAge(t *testing.T) {
	h, _ := newSteppingHarness(t)
	h.clock.Set(at(hour))
	h.tr.Apply(withWindows(statusLine(hour, s1), window(100, time.Time{}), nil))

	h.clock.Set(at(0))
	h.tr.Snapshot()
	h.clock.Set(at(5*hour + 1))
	h.tr.Apply(withWindows(statusLine(5*hour+1, s2), window(20, time.Time{}), nil))

	if got := h.account(dirA).FiveHour; got == nil || got.UsedPct != 20 {
		t.Errorf("five-hour window %+v, want the 20%% reading: the 100%% held was taken over five hours before", got)
	}
}

// How fresh a reading is follows the step too: a response after it is
// fresher than one before, and a session that has had none since reports
// older news.
func TestTracker_ClockSetBackKeepsReadingsInOrderOfFreshness(t *testing.T) {
	h, _ := newSteppingHarness(t)
	busy, other := s1, s2
	h.clock.Set(at(hour))
	h.tr.RecordAPI(request(at(hour), busy, "Opus 5.5", 0.1))
	h.tr.Apply(withWindows(statusLine(hour+1, busy), nil, window(95, weekResets)))

	h.clock.Set(at(0))
	h.tr.Snapshot()
	h.tr.RecordAPI(request(at(5), other, "Opus 5.5", 0.1))
	h.tr.Apply(withWindows(statusLine(6, other), nil, window(0, weekResets)))
	if got := h.account(dirA).SevenDay; got == nil || got.UsedPct != 0 {
		t.Fatalf("weekly window %+v, want the 0%% of the response after the step", got)
	}
	h.clock.Set(at(11))
	h.tr.Apply(withWindows(statusLine(11, busy), nil, window(95, weekResets)))

	if got := h.account(dirA).SevenDay; got == nil || got.UsedPct != 0 {
		t.Errorf("weekly window %+v, want the 0%% still: the 95%% came with a response before the step", got)
	}
}

// A reported spend limit stands for an hour after it was reported, as the
// step moved it.
func TestTracker_ClockSetBackKeepsAReportToItsHour(t *testing.T) {
	h, _ := newSteppingHarness(t)
	h.tr.SetAccount(domain.Account{Dir: dirA, Auth: domain.AuthSubscription})
	h.clock.Set(at(2 * hour))
	reporting := statusLine(2*hour, s1)
	reporting.SpendLimit = &domain.SpendLimit{UsedUSD: 12.5, LimitUSD: 50}
	h.tr.Apply(reporting)

	h.clock.Set(at(0))
	h.tr.Snapshot()

	h.clock.Set(at(hour - 1))
	if got := h.account(dirA).Credits; got == nil || got.Source != domain.CreditSourceReported {
		t.Fatalf("credits %+v just under an hour after the report, want the reported figures", got)
	}
	h.clock.Set(at(hour))
	if got := h.account(dirA).Credits; got != nil {
		t.Errorf("credits %+v an hour after the report, as the step moved it, want none", got)
	}
}

// A call that no status line has placed on an account by the step moves with
// it, so the line that places it after the step is not dated ahead.
func TestTracker_ClockSetBackMovesACallNoLineHasPlaced(t *testing.T) {
	h, _ := newSteppingHarness(t)
	busy, other := s1, s2
	h.clock.Set(at(hour))
	h.tr.RecordAPI(request(at(hour), busy, "Opus 5.5", 0.1))
	h.tr.Apply(withWindows(statusLine(hour, busy), nil, window(95, weekResets)))
	h.tr.RecordAPI(request(at(hour), busy, "Opus 5.5", 0.1))

	h.clock.Set(at(0))
	h.tr.Snapshot()
	h.tr.RecordAPI(request(at(5), other, "Opus 5.5", 0.1))
	shownWeekly(h, weeklyFrom(6, other, 0))

	if got := shownWeekly(h, weeklyFrom(11, busy, 95)); got != 0 {
		t.Errorf("shown %v%%, want the 0%% of the response after the step: the busy session's last call came before it", got)
	}
}

// The clock is set back two hours while a batch of call events is on its way:
// a call the idle session made just before the step arrives after it,
// stamped by the clock before the step. The busy session's calls after the
// step came later, so its readings are the fresher ones.
func TestTracker_ACallStampedBeforeTheStepDoesNotFreshenItsSessionForHours(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	busy, idle := s1, s2
	h.clock.Set(at(7000))
	h.tr.RecordAPI(request(at(7000), idle, "Opus 5.5", 0.1))
	shownWeekly(h, withAPITime(weeklyFrom(7001, idle, 97), 1000))
	h.tr.RecordAPI(request(at(7095), idle, "Opus 5.5", 0.1))
	shownWeekly(h, withAPITime(weeklyFrom(7096, idle, 97), 1100))
	h.tr.RecordAPI(request(at(7100), busy, "Opus 5.5", 0.1))
	if got := shownWeekly(h, withAPITime(weeklyFrom(7101, busy, 100), 500)); got != 100 {
		t.Fatalf("shown %v%% before the step, want 100%%", got)
	}

	h.clock.Set(at(-100))
	h.tr.RecordAPI(request(at(7098), idle, "Opus 5.5", 0.1))

	below, views := 0, 0
	for sec := 0; sec < 600; sec += 10 {
		if got := shownWeekly(h, withAPITime(weeklyFrom(sec, idle, 97), 1100)); got < 100 {
			below++
		}
		h.tr.RecordAPI(request(at(sec+3), busy, "Opus 5.5", 0.5))
		if got := shownWeekly(h, withAPITime(weeklyFrom(sec+4, busy, 100), 500+int64(sec+4))); got < 100 {
			below++
		}
		views += 2
	}
	if below > 0 {
		t.Errorf("weekly window shown below 100%% in %d of %d views over the 10 minutes after the step, want none", below, views)
	}
	h.clock.Set(at(600))
	if got := h.account(dirA).Credits; got == nil || !got.Active || got.SpentUSD < 30 {
		t.Errorf("credits %+v, want active, with the $30 the busy session spent at the limit after the step", got)
	}
}

// As above for sessions whose lines report no API time, whose next line takes
// a call that arrives: the call stamped before the step is dated at the step,
// as a poll stamped before it is, so it cannot keep the idle session ahead of
// the busy one's responses after the step.
func TestTracker_ACallStampedBeforeTheStepIsDatedAtTheStep(t *testing.T) {
	h := newSubscribedHarness(t, tracker.Config{Credits: twentyDollars, Alerts: defaultAlerts})
	busy, idle := s1, s2
	h.clock.Set(at(7000))
	h.tr.RecordAPI(request(at(7000), idle, "Opus 5.5", 0.1))
	shownWeekly(h, weeklyFrom(7001, idle, 97))
	h.tr.RecordAPI(request(at(7095), idle, "Opus 5.5", 0.1))
	shownWeekly(h, weeklyFrom(7096, idle, 97))
	h.tr.RecordAPI(request(at(7100), busy, "Opus 5.5", 0.1))
	shownWeekly(h, weeklyFrom(7101, busy, 100))

	h.clock.Set(at(-100))
	h.tr.RecordAPI(request(at(7098), idle, "Opus 5.5", 0))
	shownWeekly(h, weeklyFrom(0, idle, 97))

	below, views := 0, 0
	for sec := 0; sec < 600; sec += 10 {
		h.tr.RecordAPI(request(at(sec+3), busy, "Opus 5.5", 0.5))
		if got := shownWeekly(h, weeklyFrom(sec+4, busy, 100)); got < 100 {
			below++
		}
		if got := shownWeekly(h, weeklyFrom(sec+10, idle, 97)); got < 100 {
			below++
		}
		views += 2
	}
	if below > 0 {
		t.Errorf("weekly window shown below 100%% in %d of %d views from the busy session's first response after the step on, want none", below, views)
	}
	// Dated at the step, the idle session's reading there is the newest until
	// the busy one's next line, so the call made in between falls outside the
	// stretch on credits; every later one is counted.
	h.clock.Set(at(610))
	if got := h.account(dirA).Credits; got == nil || !got.Active || !near(got.SpentUSD, 29.5) {
		t.Errorf("credits %+v, want active, with the $29.50 the busy session spent at the limit from its first line after the step", got)
	}
}

// The two hooks /clear fires can arrive either side of the step.
func TestTracker_ClockSetBackStillLinksAClear(t *testing.T) {
	h, _ := newSteppingHarness(t)
	h.clock.Set(at(hour))
	h.tr.Apply(hook(hour, s1, "SessionStart"))
	h.tr.Apply(hook(hour, s2, "SessionStart"))

	h.clock.Set(at(1))
	h.tr.Apply(sessionEnd(1, s1, "clear"))

	if got := h.session(s2).Replaces; got != s1 {
		t.Errorf("s2 replaces %q, want %q", got, s1)
	}
}

// What is kept across restarts follows the step.
func TestTracker_ClockSetBackIsRecorded(t *testing.T) {
	h, _ := newSteppingHarness(t)
	h.clock.Set(at(hour))
	h.tr.Apply(hook(hour, s1, "PermissionRequest"))
	h.tr.SetAccount(domain.Account{Dir: dirA, Email: "dev@example.com", ProbedAt: at(hour)})

	h.clock.Set(at(5))
	h.tr.Snapshot()

	sessions, accounts, _, _ := h.rec.recorded()
	if last := sessions[len(sessions)-1]; !last.StateAt.Equal(at(5)) {
		t.Errorf("the session last recorded changed state at %s, want %s", last.StateAt, at(5))
	}
	if last := accounts[len(accounts)-1]; !last.ProbedAt.Equal(at(5)) {
		t.Errorf("the account last recorded was probed at %s, want %s", last.ProbedAt, at(5))
	}
}

// A call that waits for its session across the step is dated as the step
// moved it once the session appears.
func TestTracker_ClockSetBackKeepsWaitingCallsToTheirWindow(t *testing.T) {
	h, _ := newSteppingHarness(t)
	h.clock.Set(at(hour))
	h.tr.RecordAPI(request(at(hour), s1, "Opus 5.5", 1))

	h.clock.Set(at(0))
	h.tr.Apply(hook(0, s1, "SessionStart"))
	h.clock.Set(at(61 * 60))
	h.tr.Apply(hook(61*60, s2, "SessionStart"))
	h.tr.RecordAPI(request(at(61*60), s2, "Opus 5.5", 1))

	if got := h.session(s1).Share; got != 0 {
		t.Errorf("s1's share is %v, want 0: its call is more than an hour old", got)
	}
}
