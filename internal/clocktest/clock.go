// Package clocktest is a clock for tests of code that reads the time and
// waits on timers. Its time stands still until the test moves it, and a
// timer runs only when the test moves the clock past the timer's deadline.
package clocktest

import (
	"slices"
	"sync"
	"testing"
	"time"
)

// waitLimit bounds WaitForTimers, so that a timer that never comes fails the
// test instead of hanging it.
const waitLimit = 5 * time.Second

// Clock is a manual clock. It is safe for concurrent use.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*timer
	// armed is closed, and replaced, each time a timer is armed.
	armed chan struct{}
}

type timer struct {
	at time.Time
	f  func()
}

// New returns a clock that reads now until it is advanced.
func New(now time.Time) *Clock {
	return &Clock{now: now, armed: make(chan struct{})}
}

// Now returns the clock's time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc arranges for f to run once the clock has moved d past its
// current time, and returns a function that cancels it, reporting whether it
// stopped f from running. Its signature is that of time.AfterFunc's timer
// and its Stop together.
func (c *Clock) AfterFunc(d time.Duration, f func()) (stop func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &timer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	close(c.armed)
	c.armed = make(chan struct{})
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		i := slices.Index(c.timers, t)
		if i < 0 {
			return false
		}
		c.timers = slices.Delete(c.timers, i, i+1)
		return true
	}
}

// Advance moves the clock d forward the way time passes: it steps to each
// deadline it reaches, in order, and runs that timer, including any timer
// the ones it runs arm. While a timer runs, the clock reads its deadline.
// Each runs on the caller's goroutine with the clock unlocked, so by the time
// Advance returns every one of them has returned too.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		t := c.takeDue(target)
		if t == nil {
			return
		}
		t.f()
	}
}

// takeDue removes the pending timer with the earliest deadline no later than
// target and moves the clock to that deadline. When there is none, it moves
// the clock to target and returns nil.
func (c *Clock) takeDue(target time.Time) *timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	var earliest *timer
	for _, t := range c.timers {
		if !t.at.After(target) && (earliest == nil || t.at.Before(earliest.at)) {
			earliest = t
		}
	}
	if earliest == nil {
		c.now = target
		return nil
	}
	c.timers = slices.DeleteFunc(c.timers, func(t *timer) bool { return t == earliest })
	if earliest.at.After(c.now) {
		c.now = earliest.at
	}
	return earliest
}

// Pending returns how far from now each pending timer's deadline is,
// soonest first.
func (c *Clock) Pending() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	left := make([]time.Duration, 0, len(c.timers))
	for _, t := range c.timers {
		left = append(left, t.at.Sub(c.now))
	}
	slices.Sort(left)
	return left
}

// WaitForTimers waits until at least n timers are pending, for code on
// another goroutine to arm them, and fails tb if they are not within five
// seconds.
func (c *Clock) WaitForTimers(tb testing.TB, n int) {
	tb.Helper()
	deadline := time.NewTimer(waitLimit)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		count, armed := len(c.timers), c.armed
		c.mu.Unlock()
		if count >= n {
			return
		}
		select {
		case <-armed:
		case <-deadline.C:
			tb.Fatalf("%d timers pending after %v, want %d", count, waitLimit, n)
		}
	}
}
