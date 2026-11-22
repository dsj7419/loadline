package tracker_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

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
