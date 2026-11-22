package accounts_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/dsj7419/loadline/internal/accounts"
	"github.com/dsj7419/loadline/internal/claudecli"
	"github.com/dsj7419/loadline/internal/clocktest"
	"github.com/dsj7419/loadline/internal/domain"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const (
	dirA domain.ConfigDir = "/home/me/.claude"
	dirB domain.ConfigDir = "/home/me/.claude-b"

	every = 6 * time.Hour
)

var t0 = time.Date(2026, time.November, 21, 18, 0, 0, 0, time.UTC)

var signedIn = claudecli.AuthStatus{
	LoggedIn:         true,
	AuthMethod:       "claude.ai",
	APIProvider:      "firstParty",
	Email:            "dev@example.com",
	OrgName:          "Example Org",
	SubscriptionType: "max",
	PlanTier:         "max_20x",
}

// authority stands in for `claude auth status`. Each call takes the next
// answer queued for its dir, or signedIn when none is.
type authority struct {
	mu      sync.Mutex
	calls   []domain.ConfigDir
	answers map[domain.ConfigDir][]answer
	// hold, when set for a dir, keeps its probes waiting until it closes or
	// their context ends.
	hold   map[domain.ConfigDir]chan struct{}
	called chan domain.ConfigDir
}

type answer struct {
	status claudecli.AuthStatus
	err    error
}

func newAuthority() *authority {
	return &authority{
		answers: make(map[domain.ConfigDir][]answer),
		hold:    make(map[domain.ConfigDir]chan struct{}),
		called:  make(chan domain.ConfigDir, 1000),
	}
}

func (a *authority) status(ctx context.Context, dir domain.ConfigDir) (claudecli.AuthStatus, error) {
	a.mu.Lock()
	a.calls = append(a.calls, dir)
	next := answer{status: signedIn}
	if queued := a.answers[dir]; len(queued) > 0 {
		next, a.answers[dir] = queued[0], queued[1:]
	}
	hold := a.hold[dir]
	a.mu.Unlock()
	a.called <- dir
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return claudecli.AuthStatus{}, ctx.Err()
		}
	}
	return next.status, next.err
}

func (a *authority) queue(dir domain.ConfigDir, answers ...answer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.answers[dir] = append(a.answers[dir], answers...)
}

func (a *authority) probes() []domain.ConfigDir {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

// waitProbes waits until n probes have started in all.
func (a *authority) waitProbes(t *testing.T, n int) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for len(a.probes()) < n {
		select {
		case <-a.called:
		case <-timeout:
			t.Fatalf("%d probes started, want %d", len(a.probes()), n)
		}
	}
}

// accountLog records the accounts the prober sets.
type accountLog struct {
	mu    sync.Mutex
	set   []domain.Account
	added chan struct{}
}

func newAccountLog() *accountLog { return &accountLog{added: make(chan struct{}, 1000)} }

func (l *accountLog) record(a domain.Account) {
	l.mu.Lock()
	l.set = append(l.set, a)
	l.mu.Unlock()
	l.added <- struct{}{}
}

func (l *accountLog) accounts() []domain.Account {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.set)
}

// waitAccounts waits until n accounts have been set in all.
func (l *accountLog) waitAccounts(t *testing.T, n int) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for len(l.accounts()) < n {
		select {
		case <-l.added:
		case <-timeout:
			t.Fatalf("%d accounts set, want %d", len(l.accounts()), n)
		}
	}
}

type harness struct {
	clock  *clocktest.Clock
	auth   *authority
	log    *accountLog
	prober *accounts.Prober
}

func newHarness() *harness {
	h := &harness{clock: clocktest.New(t0), auth: newAuthority(), log: newAccountLog()}
	h.prober = &accounts.Prober{
		Status:    h.auth.status,
		Set:       h.log.record,
		Labels:    map[domain.ConfigDir]string{dirA: "Main"},
		Every:     every,
		Clock:     h.clock.Now,
		AfterFunc: h.clock.AfterFunc,
	}
	return h
}

// run starts the prober and stops it, waiting for Run to return, when the
// test ends.
func (h *harness) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.prober.Run(ctx)
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

// settle waits for the n-th probe and then for the prober to schedule the
// next one.
func (h *harness) settle(t *testing.T, n int) {
	t.Helper()
	h.auth.waitProbes(t, n)
	h.clock.WaitForTimers(t, 1)
}

func TestProber_ProbesOnceThenEvery6h(t *testing.T) {
	h := newHarness()
	h.run(t)

	h.prober.Observe(dirA)
	h.settle(t, 1)
	h.prober.Observe(dirA)
	if got := h.clock.Pending(); !slices.Equal(got, []time.Duration{every}) {
		t.Fatalf("next probe in %v, want %v", got, every)
	}

	h.clock.Advance(every - time.Nanosecond)
	if got := len(h.auth.probes()); got != 1 {
		t.Fatalf("%d probes before six hours passed, want 1", got)
	}
	h.clock.Advance(time.Nanosecond)
	h.settle(t, 2)
	h.clock.Advance(every)
	h.settle(t, 3)

	want := domain.Account{
		Dir:      dirA,
		Label:    "Main",
		Email:    "dev@example.com",
		Plan:     "Max 20x",
		Org:      "Example Org",
		Auth:     domain.AuthSubscription,
		ProbedAt: t0,
	}
	got := h.log.accounts()
	if len(got) != 3 || got[0] != want {
		t.Fatalf("set %+v, want three accounts, the first %+v", got, want)
	}
	if !got[1].ProbedAt.Equal(t0.Add(every)) || !got[2].ProbedAt.Equal(t0.Add(2*every)) {
		t.Errorf("probed at %v and %v, want every six hours", got[1].ProbedAt, got[2].ProbedAt)
	}
}

func TestProber_FailureKeepsPreviousAccount(t *testing.T) {
	h := newHarness()
	h.auth.queue(dirA,
		answer{status: signedIn},
		answer{err: errors.New("auth status: context deadline exceeded")},
		answer{status: claudecli.AuthStatus{LoggedIn: true, AuthMethod: "api_key"}},
	)
	h.run(t)
	h.prober.Observe(dirA)
	h.settle(t, 1)

	h.clock.Advance(every)
	h.settle(t, 2)
	if got := h.log.accounts(); len(got) != 1 || got[0].Email != "dev@example.com" {
		t.Fatalf("set %+v after a failed probe, want only the first account", got)
	}

	// The failure does not stop the schedule: the retry comes sooner.
	h.clock.Advance(5 * time.Minute)
	h.settle(t, 3)
	if got := h.log.accounts(); len(got) != 2 || got[1].Auth != domain.AuthAPIKey {
		t.Errorf("set %+v, want the third probe's API-key account", got)
	}
}

// A probe can fail for a moment, as when claude is being updated. The retry
// comes five minutes later, then waits twice as long after each failure in
// a row, up to the six-hour schedule a success keeps.
func TestProber_RetriesAFailureSoonerBackingOffToThe6hSchedule(t *testing.T) {
	h := newHarness()
	failed := answer{err: errors.New("auth status: exit status 3")}
	for range 8 {
		h.auth.queue(dirA, failed)
	}
	h.auth.queue(dirA, answer{status: signedIn}, failed)
	h.run(t)
	h.prober.Observe(dirA)

	waits := []time.Duration{
		5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute,
		160 * time.Minute, 320 * time.Minute, every, // eight failures
		every,           // a success
		5 * time.Minute, // and a failure after it starts again
	}
	for i, want := range waits {
		h.settle(t, i+1)
		if got := h.clock.Pending(); !slices.Equal(got, []time.Duration{want}) {
			t.Fatalf("after probe %d, next probe in %v, want %v", i+1, got, want)
		}
		if i < len(waits)-1 {
			h.clock.Advance(want)
		}
	}
	if got := h.log.accounts(); len(got) != 1 {
		t.Errorf("set %d accounts, want the one success's", len(got))
	}
}

func TestProber_ConcurrentObservesCollapseIntoOneProbe(t *testing.T) {
	h := newHarness()
	h.run(t)

	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() { h.prober.Observe(dirB) })
	}
	wg.Wait()
	h.settle(t, 1)

	if got := h.auth.probes(); len(got) != 1 {
		t.Errorf("probed %v, want one probe of %s", got, dirB)
	}
}

func TestProber_ObserveBeforeRunProbesOnStart(t *testing.T) {
	h := newHarness()
	h.prober.Observe(dirA)
	h.prober.Observe(dirB)

	h.run(t)
	h.settle(t, 2)

	got := h.auth.probes()
	slices.Sort(got)
	if want := []domain.ConfigDir{dirA, dirB}; !slices.Equal(got, want) {
		t.Errorf("probed %v, want %v", got, want)
	}
}

func TestProber_LabelsComeFromTheConfig(t *testing.T) {
	h := newHarness()
	h.run(t)
	h.prober.Observe(dirA)
	h.prober.Observe(dirB)
	h.log.waitAccounts(t, 2)

	labels := make(map[domain.ConfigDir]string)
	for _, a := range h.log.accounts() {
		labels[a.Dir] = a.Label
	}
	if len(labels) != 2 || labels[dirA] != "Main" || labels[dirB] != "" {
		t.Errorf("labels %v, want Main for %s and none for %s", labels, dirA, dirB)
	}
}

func TestProber_ASlowProbeHoldsUpNoOtherDir(t *testing.T) {
	h := newHarness()
	release := make(chan struct{})
	h.auth.hold[dirA] = release
	defer close(release)
	h.run(t)

	h.prober.Observe(dirA)
	h.auth.waitProbes(t, 1)
	h.prober.Observe(dirB)
	h.auth.waitProbes(t, 2)

	if got := h.auth.probes(); !slices.Equal(got, []domain.ConfigDir{dirA, dirB}) {
		t.Errorf("probed %v, want %s while %s still runs", got, dirB, dirA)
	}
}

func TestProber_AProbeStillRunningIsNotStartedAgain(t *testing.T) {
	h := newHarness()
	release := make(chan struct{})
	h.auth.hold[dirA] = release
	h.run(t)
	h.prober.Observe(dirA)
	h.auth.waitProbes(t, 1)

	h.clock.Advance(3 * every)
	h.prober.Observe(dirA)
	if got := len(h.auth.probes()); got != 1 {
		t.Fatalf("%d probes of a dir whose probe has not returned, want 1", got)
	}

	close(release)
	h.clock.WaitForTimers(t, 1)
	if got := h.clock.Pending(); !slices.Equal(got, []time.Duration{every}) {
		t.Errorf("next probe in %v, want six hours after the probe returned", got)
	}
}

func TestProber_StopsWithItsContextWhileProbing(t *testing.T) {
	h := newHarness()
	h.auth.hold[dirA] = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.prober.Run(ctx)
	}()
	h.prober.Observe(dirA)
	h.auth.waitProbes(t, 1)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	if got := h.log.accounts(); len(got) != 0 {
		t.Errorf("set %+v from a probe its context ended, want nothing", got)
	}
	if got := h.clock.Pending(); len(got) != 0 {
		t.Errorf("timers %v left pending after Run returned", got)
	}
}

func TestProber_ZeroEveryMeans6h(t *testing.T) {
	h := newHarness()
	h.prober.Every = 0
	h.run(t)
	h.prober.Observe(dirA)
	h.settle(t, 1)

	if got := h.clock.Pending(); !slices.Equal(got, []time.Duration{6 * time.Hour}) {
		t.Errorf("next probe in %v, want 6h", got)
	}
}

// A dir forgotten is probed no more, however long passes, until it is
// observed again, which probes it at once.
func TestProber_ForgetStopsProbingUntilObservedAgain(t *testing.T) {
	h := newHarness()
	h.run(t)
	h.prober.Observe(dirA)
	h.settle(t, 1)

	h.prober.Forget(dirA)
	h.clock.Advance(3 * every)
	time.Sleep(20 * time.Millisecond)
	if got := len(h.auth.probes()); got != 1 {
		t.Fatalf("%d probes of a forgotten dir, want only the one before", got)
	}

	h.prober.Observe(dirA)
	h.settle(t, 2)
	if got := h.clock.Pending(); !slices.Equal(got, []time.Duration{every}) {
		t.Errorf("next probe in %v, want the usual %v", got, every)
	}
}

// A probe running when its dir is forgotten finishes and schedules nothing.
func TestProber_ForgetDuringAProbeSchedulesNothing(t *testing.T) {
	h := newHarness()
	release := make(chan struct{})
	h.auth.hold[dirA] = release
	h.run(t)
	h.prober.Observe(dirA)
	h.auth.waitProbes(t, 1)

	h.prober.Forget(dirA)
	close(release)
	h.log.waitAccounts(t, 1)
	time.Sleep(20 * time.Millisecond)

	if got := h.clock.Pending(); len(got) != 0 {
		t.Errorf("timers %v after the forgotten dir's probe returned, want none", got)
	}
	h.prober.Forget(dirB) // a dir never observed
}

// Forgetting the dir whose probe is due next leaves Run waiting for the next
// of the others.
func TestProber_ForgetReschedulesTheWait(t *testing.T) {
	h := newHarness()
	h.auth.queue(dirB, answer{err: errors.New("auth status: exit status 1")})
	h.run(t)
	h.prober.Observe(dirA)
	h.prober.Observe(dirB)
	h.auth.waitProbes(t, 2)
	waitPending(t, h.clock, []time.Duration{5 * time.Minute})

	h.prober.Forget(dirB)

	waitPending(t, h.clock, []time.Duration{every})
}

// waitPending waits until the timers pending on clock are want.
func waitPending(t *testing.T, clock *clocktest.Clock, want []time.Duration) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Equal(clock.Pending(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("timers %v, want %v", clock.Pending(), want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
