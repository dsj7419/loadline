package poll_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/dsj7419/loadline/internal/clocktest"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/poll"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const (
	dirA domain.ConfigDir = "/home/me/.claude"
	dirB domain.ConfigDir = "/home/me/.claude-b"
	dirC domain.ConfigDir = "/home/me/.claude-c"

	fast     = 5 * time.Second
	slow     = 30 * time.Second
	debounce = 500 * time.Millisecond
)

var t0 = time.Date(2026, time.November, 21, 18, 0, 0, 0, time.UTC)

// sink is the live state the poller under test feeds.
type sink struct {
	mu      sync.Mutex
	dirs    []domain.ConfigDir
	live    bool
	applied []applied
}

type applied struct {
	dir   domain.ConfigDir
	at    time.Time
	polls []domain.Poll
}

func newSink(dirs ...domain.ConfigDir) *sink { return &sink{dirs: dirs} }

func (s *sink) ApplyPoll(dir domain.ConfigDir, at time.Time, polls []domain.Poll) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applied = append(s.applied, applied{dir: dir, at: at, polls: polls})
}

func (s *sink) KnownDirs() []domain.ConfigDir {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.dirs)
}

func (s *sink) HasLive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

func (s *sink) setLive(live bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live = live
}

func (s *sink) addDir(dir domain.ConfigDir) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirs = append(s.dirs, dir)
}

func (s *sink) applications() []applied {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.applied)
}

// lister stands in for `claude agents --json`. It records each call, and
// during, when set, runs inside every call.
type lister struct {
	mu     sync.Mutex
	calls  []listCall
	fail   map[domain.ConfigDir]error
	during func(domain.ConfigDir)
	called chan struct{}
}

type listCall struct {
	dir domain.ConfigDir
	at  time.Time
}

func newLister() *lister {
	return &lister{fail: make(map[domain.ConfigDir]error), called: make(chan struct{}, 1000)}
}

func (l *lister) list(_ context.Context, dir domain.ConfigDir, at time.Time) ([]domain.Poll, error) {
	l.mu.Lock()
	l.calls = append(l.calls, listCall{dir: dir, at: at})
	during, err := l.during, l.fail[dir]
	l.mu.Unlock()
	if during != nil {
		during(dir)
	}
	l.called <- struct{}{}
	if err != nil {
		return nil, err
	}
	return []domain.Poll{{At: at, Dir: dir, Session: "s1", Status: "busy"}}, nil
}

func (l *lister) listed() []listCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

// waitCalls waits until List has been called n times in all.
func (l *lister) waitCalls(t *testing.T, n int) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for len(l.listed()) < n {
		select {
		case <-l.called:
		case <-timeout:
			t.Fatalf("List called %d times, want %d", len(l.listed()), n)
		}
	}
}

// harness is a poller of one sink, run on a manual clock.
type harness struct {
	clock  *clocktest.Clock
	sink   *sink
	list   *lister
	poller *poll.Poller

	mu     sync.Mutex
	onPoll []time.Time
}

func newHarness(dirs ...domain.ConfigDir) *harness {
	h := &harness{clock: clocktest.New(t0), sink: newSink(dirs...), list: newLister()}
	h.poller = &poll.Poller{
		List:      h.list.list,
		Sink:      h.sink,
		Fast:      fast,
		Slow:      slow,
		Debounce:  debounce,
		Clock:     h.clock.Now,
		AfterFunc: h.clock.AfterFunc,
		OnPoll:    h.notePoll,
	}
	return h
}

func (h *harness) notePoll(at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onPoll = append(h.onPoll, at)
}

func (h *harness) polledAt() []time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.onPoll)
}

// run starts the poller and stops it, waiting for Run to return, when the
// test ends.
func (h *harness) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.poller.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after its context ended")
		}
	})
}

// settle waits for the n-th call of List and then for the poller to arm
// its next timers, of which there are want.
func (h *harness) settle(t *testing.T, calls, want int) {
	t.Helper()
	h.list.waitCalls(t, calls)
	h.clock.WaitForTimers(t, want)
}

func (h *harness) assertPending(t *testing.T, want ...time.Duration) {
	t.Helper()
	if got := h.clock.Pending(); !slices.Equal(got, want) {
		t.Fatalf("pending timers %v, want %v", got, want)
	}
}

func (h *harness) assertCalls(t *testing.T, want int) {
	t.Helper()
	if got := len(h.list.listed()); got != want {
		t.Fatalf("List called %d times, want %d", got, want)
	}
}

func TestPoller_FastWhenLiveSlowWhenIdle(t *testing.T) {
	h := newHarness(dirA)
	h.sink.setLive(true)
	h.run(t)

	h.settle(t, 1, 1)
	h.assertPending(t, fast)

	h.sink.setLive(false)
	h.clock.Advance(fast)
	h.settle(t, 2, 1)
	h.assertPending(t, slow)

	h.clock.Advance(slow - time.Nanosecond)
	h.assertCalls(t, 2)
	h.clock.Advance(time.Nanosecond)
	h.list.waitCalls(t, 3)

	wantAt := []time.Time{t0, t0.Add(fast), t0.Add(fast + slow)}
	var gotAt []time.Time
	for _, c := range h.list.listed() {
		gotAt = append(gotAt, c.at)
	}
	if !slices.EqualFunc(gotAt, wantAt, time.Time.Equal) {
		t.Errorf("polled at %v, want %v", gotAt, wantAt)
	}
}

func TestPoller_KickDebounces(t *testing.T) {
	h := newHarness(dirA)
	h.sink.setLive(true)
	h.run(t)
	h.settle(t, 1, 1)

	// Five kicks within 100 ms.
	for i := range 5 {
		h.poller.Kick()
		if i == 0 {
			h.clock.WaitForTimers(t, 2)
		}
		h.clock.Advance(20 * time.Millisecond)
	}
	h.clock.Advance(debounce - 100*time.Millisecond - time.Nanosecond)
	h.assertCalls(t, 1)

	h.clock.Advance(time.Nanosecond)
	h.settle(t, 2, 1)
	if got := h.list.listed()[1].at; !got.Equal(t0.Add(debounce)) {
		t.Errorf("the kicked poll ran at %v, want 500 ms after the first kick, %v", got, t0.Add(debounce))
	}
	// The kicks are spent, and the schedule starts again from the poll.
	h.assertPending(t, fast)
	h.clock.Advance(fast - time.Nanosecond)
	h.assertCalls(t, 2)
}

func TestPoller_ErrorDoesNotMarkAbsent(t *testing.T) {
	h := newHarness(dirA, dirB)
	h.list.fail[dirA] = errors.New("claude: exit status 3")
	h.run(t)

	h.settle(t, 2, 1)

	got := h.sink.applications()
	if len(got) != 1 || got[0].dir != dirB {
		t.Fatalf("applied polls of %v, want only %s's: a failed poll must not report every session gone", got, dirB)
	}
	if at := h.polledAt(); len(at) != 1 || !at[0].Equal(t0) {
		t.Errorf("OnPoll got %v, want the one poll that succeeded, at %v", at, t0)
	}
}

// A hook that arrives while `claude agents` runs is newer than what the
// poll reports, so the poll is stamped with the time before it listed.
func TestPoller_StampsEachPollWithTheTimeBeforeListing(t *testing.T) {
	h := newHarness(dirA)
	h.list.during = func(domain.ConfigDir) { h.clock.Advance(175 * time.Millisecond) }
	h.run(t)

	h.settle(t, 1, 1)

	got := h.sink.applications()
	if len(got) != 1 || !got[0].at.Equal(t0) {
		t.Fatalf("applied %+v, want one poll at %v", got, t0)
	}
	if !got[0].polls[0].At.Equal(t0) {
		t.Errorf("listed entry stamped %v, want %v", got[0].polls[0].At, t0)
	}
	if at := h.polledAt(); len(at) != 1 || !at[0].Equal(t0) {
		t.Errorf("OnPoll got %v, want %v", at, t0)
	}
}

func TestPoller_KickDuringAPollPollsAgain(t *testing.T) {
	h := newHarness(dirA)
	var once sync.Once
	h.list.during = func(domain.ConfigDir) { once.Do(h.poller.Kick) }
	h.run(t)

	h.settle(t, 1, 2)
	h.assertPending(t, debounce, slow)

	h.clock.Advance(debounce)
	h.list.waitCalls(t, 2)
}

func TestPoller_PollOnScheduleKeepsAPendingKick(t *testing.T) {
	h := newHarness(dirA)
	h.sink.setLive(true)
	h.run(t)
	h.settle(t, 1, 1)

	h.clock.Advance(fast - 200*time.Millisecond)
	h.poller.Kick()
	h.clock.WaitForTimers(t, 2)
	h.clock.Advance(200 * time.Millisecond)
	h.settle(t, 2, 2)

	h.assertPending(t, debounce-200*time.Millisecond, fast)
	h.clock.Advance(debounce - 200*time.Millisecond)
	h.list.waitCalls(t, 3)
}

func TestPoller_PollsEveryKnownDirAtOnce(t *testing.T) {
	h := newHarness(dirA, dirB)
	bStarted := make(chan struct{})
	var stuck sync.WaitGroup
	stuck.Add(1)
	h.list.during = func(dir domain.ConfigDir) {
		if dir == dirB {
			close(bStarted)
			return
		}
		defer stuck.Done()
		select {
		case <-bStarted:
		case <-time.After(5 * time.Second):
			t.Error("a slow poll of one dir held up the poll of the other")
		}
	}
	h.run(t)

	h.settle(t, 2, 1)
	stuck.Wait()
}

func TestPoller_PollsDirsLearnedSinceTheLastRound(t *testing.T) {
	h := newHarness(dirA)
	h.run(t)
	h.settle(t, 1, 1)

	h.sink.addDir(dirC)
	h.clock.Advance(slow)
	h.list.waitCalls(t, 3)

	var dirs []domain.ConfigDir
	for _, c := range h.list.listed()[1:] {
		dirs = append(dirs, c.dir)
	}
	slices.Sort(dirs)
	if want := []domain.ConfigDir{dirA, dirC}; !slices.Equal(dirs, want) {
		t.Errorf("second round polled %v, want %v", dirs, want)
	}
}

func TestPoller_ZeroIntervalsMeanTheDefaults(t *testing.T) {
	h := newHarness(dirA)
	h.poller.Fast, h.poller.Slow, h.poller.Debounce = 0, 0, 0
	h.sink.setLive(true)
	h.run(t)
	h.settle(t, 1, 1)
	h.assertPending(t, 5*time.Second)

	h.poller.Kick()
	h.clock.WaitForTimers(t, 2)
	h.assertPending(t, 500*time.Millisecond, 5*time.Second)

	h.sink.setLive(false)
	h.clock.Advance(500 * time.Millisecond)
	h.settle(t, 2, 1)
	h.assertPending(t, 30*time.Second)
}

func TestPoller_KickNeverBlocks(t *testing.T) {
	var p poll.Poller
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			p.Kick()
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Kick blocked with no poller running")
	}
}

func TestPoller_StopsWithItsContextWhileListing(t *testing.T) {
	clock := clocktest.New(t0)
	listing := make(chan struct{})
	p := &poll.Poller{
		List: func(ctx context.Context, _ domain.ConfigDir, _ time.Time) ([]domain.Poll, error) {
			close(listing)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Sink:      newSink(dirA),
		Clock:     clock.Now,
		AfterFunc: clock.AfterFunc,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()

	<-listing
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	if got := clock.Pending(); len(got) != 0 {
		t.Errorf("timers %v left pending after Run returned", got)
	}
}
