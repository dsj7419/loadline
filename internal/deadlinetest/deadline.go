// Package deadlinetest checks that a call bounded by deadlines gave up at
// them. A call waits out its deadlines in turn and then returns, so it takes
// no less than their sum, whatever the load. It may take a little longer,
// since a loaded machine wakes it late and does the work around each wait
// slowly. The margin for that is half the longest deadline: less than any
// cap applied twice over would add, and not scaled for the race detector,
// which slows the code around a wait but not the timer that ends it.
package deadlinetest

import (
	"slices"
	"time"
)

// T is the part of testing.TB that AssertGaveUpAt reports through.
type T interface {
	Helper()
	Errorf(format string, args ...any)
}

// Limit returns how long, in all, a call that waits out deadlines in turn
// may take to give up: their sum, and half the longest of them.
func Limit(deadlines ...time.Duration) time.Duration {
	return sum(deadlines) + slices.Max(deadlines)/2
}

// AssertGaveUpAt fails t unless elapsed, how long call took against a
// server that does not answer, shows that it waited out deadlines in turn
// and then gave up: no sooner than their sum, and before Limit(deadlines).
func AssertGaveUpAt(t T, call string, elapsed time.Duration, deadlines ...time.Duration) {
	t.Helper()
	if least, limit := sum(deadlines), Limit(deadlines...); elapsed < least || elapsed >= limit {
		t.Errorf("%s returned after %v, want it to give up once its deadlines %v have passed: no sooner than %v, and before %v",
			call, elapsed, deadlines, least, limit)
	}
}

func sum(deadlines []time.Duration) time.Duration {
	var total time.Duration
	for _, d := range deadlines {
		total += d
	}
	return total
}
