package clocktest_test

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/clocktest"
)

var t0 = time.Date(2026, time.November, 21, 18, 0, 0, 0, time.UTC)

// recorder notes the order in which timers run.
type recorder struct {
	mu  sync.Mutex
	ran []string
}

func (r *recorder) note(name string) func() {
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.ran = append(r.ran, name)
	}
}

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ran)
}

func TestClock_StandsStillUntilAdvanced(t *testing.T) {
	c := clocktest.New(t0)

	if got := c.Now(); !got.Equal(t0) {
		t.Fatalf("Now = %v, want %v", got, t0)
	}
	c.Advance(90 * time.Second)
	if got, want := c.Now(), t0.Add(90*time.Second); !got.Equal(want) {
		t.Errorf("Now after Advance = %v, want %v", got, want)
	}
}

func TestClock_RunsDueTimersInDeadlineOrder(t *testing.T) {
	c := clocktest.New(t0)
	var r recorder
	c.AfterFunc(3*time.Second, r.note("3s"))
	c.AfterFunc(time.Second, r.note("1s"))
	c.AfterFunc(10*time.Second, r.note("10s"))

	c.Advance(time.Second - time.Nanosecond)
	if got := r.got(); len(got) != 0 {
		t.Fatalf("ran %v before any deadline", got)
	}
	c.Advance(3 * time.Second)
	if got, want := r.got(), []string{"1s", "3s"}; !slices.Equal(got, want) {
		t.Errorf("ran %v, want %v", got, want)
	}
	if got, want := c.Pending(), []time.Duration{6*time.Second + time.Nanosecond}; !slices.Equal(got, want) {
		t.Errorf("Pending = %v, want %v", got, want)
	}
}

func TestClock_RunsATimerArmedByAnotherWhenItIsDue(t *testing.T) {
	c := clocktest.New(t0)
	var r recorder
	var secondRanAt time.Time
	c.AfterFunc(time.Second, func() {
		r.note("first")()
		c.AfterFunc(time.Second, func() {
			r.note("second")()
			secondRanAt = c.Now()
		})
	})

	c.Advance(5 * time.Second)

	if got, want := r.got(), []string{"first", "second"}; !slices.Equal(got, want) {
		t.Errorf("ran %v, want %v", got, want)
	}
	if want := t0.Add(2 * time.Second); !secondRanAt.Equal(want) {
		t.Errorf("the second timer ran at %v, want its deadline %v", secondRanAt, want)
	}
	if want := t0.Add(5 * time.Second); !c.Now().Equal(want) {
		t.Errorf("Now after Advance = %v, want %v", c.Now(), want)
	}
}

func TestClock_StoppedTimerNeverRuns(t *testing.T) {
	c := clocktest.New(t0)
	var r recorder
	stop := c.AfterFunc(time.Second, r.note("stopped"))

	if !stop() {
		t.Error("stop of a pending timer reported false")
	}
	if stop() {
		t.Error("a second stop reported true")
	}
	c.Advance(time.Hour)
	if got := r.got(); len(got) != 0 {
		t.Errorf("ran %v, want nothing", got)
	}
	if got := c.Pending(); len(got) != 0 {
		t.Errorf("Pending = %v, want none", got)
	}
}

func TestClock_StopAfterTheTimerRanReportsFalse(t *testing.T) {
	c := clocktest.New(t0)
	stop := c.AfterFunc(time.Second, func() {})

	c.Advance(time.Second)

	if stop() {
		t.Error("stop of a timer that ran reported true")
	}
}

func TestClock_WaitForTimersReturnsOnceEnoughAreArmed(t *testing.T) {
	c := clocktest.New(t0)
	armed := make(chan struct{})
	go func() {
		defer close(armed)
		c.AfterFunc(time.Second, func() {})
		c.AfterFunc(2*time.Second, func() {})
	}()

	c.WaitForTimers(t, 2)

	<-armed
	if got := len(c.Pending()); got != 2 {
		t.Errorf("%d timers pending, want 2", got)
	}
}
