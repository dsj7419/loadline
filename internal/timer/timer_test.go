package timer_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/clocktest"
	"github.com/dsj7419/loadline/internal/timer"
)

var t0 = time.Date(2026, time.November, 21, 18, 0, 0, 0, time.UTC)

func TestAfterFunc_NilRunsOnTheWallClock(t *testing.T) {
	var after timer.AfterFunc
	ran := make(chan struct{})

	after.Start(time.Millisecond, func() { close(ran) })

	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("a wall-clock timer did not run")
	}
}

func TestAfterFunc_NilStopCancels(t *testing.T) {
	var after timer.AfterFunc

	stop := after.Start(time.Hour, func() { t.Error("a stopped timer ran") })

	if !stop() {
		t.Error("stop of a pending timer reported false")
	}
}

func TestAfterFunc_SchedulesOnWhatItIsGiven(t *testing.T) {
	clock := clocktest.New(t0)
	after := timer.AfterFunc(clock.AfterFunc)
	ran := false

	after.Start(time.Second, func() { ran = true })

	if got := clock.Pending(); !slices.Equal(got, []time.Duration{time.Second}) {
		t.Fatalf("pending %v, want the one timer", got)
	}
	clock.Advance(time.Second)
	if !ran {
		t.Error("the timer did not run when its clock reached it")
	}
}

func TestSleep_ReturnsTrueOnceTheTimeHasPassed(t *testing.T) {
	clock := clocktest.New(t0)
	slept := make(chan bool, 1)
	go func() { slept <- timer.AfterFunc(clock.AfterFunc).Sleep(t.Context(), time.Minute) }()

	clock.WaitForTimers(t, 1)
	clock.Advance(time.Minute)

	select {
	case ok := <-slept:
		if !ok {
			t.Error("Sleep reported false after its time passed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Sleep did not return")
	}
}

func TestSleep_ReturnsFalseWhenTheContextEndsAndStopsItsTimer(t *testing.T) {
	clock := clocktest.New(t0)
	ctx, cancel := context.WithCancel(t.Context())
	slept := make(chan bool, 1)
	go func() { slept <- timer.AfterFunc(clock.AfterFunc).Sleep(ctx, time.Minute) }()
	clock.WaitForTimers(t, 1)

	cancel()

	select {
	case ok := <-slept:
		if ok {
			t.Error("Sleep reported true after its context ended")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Sleep did not return")
	}
	if got := clock.Pending(); len(got) != 0 {
		t.Errorf("timers %v left pending", got)
	}
}

func TestNotify_NeverBlocks(t *testing.T) {
	ch := make(chan struct{}, 1)
	notify := timer.Notify(ch)

	notify()
	notify()

	if len(ch) != 1 {
		t.Errorf("channel holds %d signals, want 1", len(ch))
	}
}
