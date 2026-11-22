// Package timer schedules work after a delay, on wall-clock timers or on
// whatever schedule a caller supplies, so that code that waits can be tested
// on a manual clock.
package timer

import (
	"context"
	"time"
)

// AfterFunc runs f once d has passed and returns a function that cancels it,
// reporting whether it stopped f from running. The nil AfterFunc uses
// wall-clock timers.
type AfterFunc func(d time.Duration, f func()) (stop func() bool)

// Start arranges for f to run once d has passed.
func (a AfterFunc) Start(d time.Duration, f func()) (stop func() bool) {
	if a == nil {
		return time.AfterFunc(d, f).Stop
	}
	return a(d, f)
}

// Sleep waits d, reporting false when ctx ended first.
func (a AfterFunc) Sleep(ctx context.Context, d time.Duration) bool {
	due := make(chan struct{})
	stop := a.Start(d, func() { close(due) })
	defer stop()
	select {
	case <-ctx.Done():
		return false
	case <-due:
		return true
	}
}

// Notify returns a function that signals ch without blocking: a signal
// already waiting in ch stands for both.
func Notify(ch chan<- struct{}) func() {
	return func() {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
