// Package deadlinetest checks that a call bounded by deadlines gave up at
// them.
//
// Timed by its caller, a call waits out its deadlines in turn and then
// returns, so it takes no less than their sum, whatever the load. It may
// take a little longer, since a loaded machine wakes it late and does the
// work around each wait slowly. That suits a call that does little besides
// wait.
//
// A command that also reads files or starts processes around its waits is
// timed instead by the server it waits on, one wait at a time, so that
// none of that work counts. The server sees a wait begin when the request
// arrives, a little after its client set the deadline, and end when it
// notices the client has gone, a little after the deadline passed. The wait
// may therefore read a little shorter than its deadline as well as a little
// longer.
//
// The margin either way is half the shortest deadline: less than any one
// cap applied twice over would add, and not scaled for the race detector,
// which slows the code around a wait but not the timer that ends it.
package deadlinetest

import (
	"slices"
	"time"
)

// T is the part of testing.TB that the assertions report through.
type T interface {
	Helper()
	Errorf(format string, args ...any)
}

// Limit returns how long, in all, a call that waits out deadline and then
// each of more in turn may take to give up: their sum, and half the
// shortest of them.
func Limit(deadline time.Duration, more ...time.Duration) time.Duration {
	all := append([]time.Duration{deadline}, more...)
	return sum(all) + slices.Min(all)/2
}

// AssertGaveUpAt fails t unless elapsed, how long call took against a
// server that does not answer, shows that it waited out deadline and then
// each of more in turn, and then gave up: no sooner than their sum, and
// before Limit(deadline, more...).
func AssertGaveUpAt(t T, call string, elapsed, deadline time.Duration, more ...time.Duration) {
	t.Helper()
	all := append([]time.Duration{deadline}, more...)
	if least, limit := sum(all), Limit(deadline, more...); elapsed < least || elapsed >= limit {
		t.Errorf("%s returned after %v, want it to give up once its deadlines %v have passed: no sooner than %v, and before %v",
			call, elapsed, all, least, limit)
	}
}

// AssertHeldFor fails t unless held shows that the client of what, a
// request to a server that does not answer, waited out deadline and then
// gave up. held is how long the server held the request, from its arrival
// until it saw the client go, and must fall within half of deadline either
// side of it.
func AssertHeldFor(t T, what string, held, deadline time.Duration) {
	t.Helper()
	margin := deadline / 2
	if least, limit := deadline-margin, deadline+margin; held < least || held >= limit {
		t.Errorf("%s was held for %v before its client gave up, want the client to give up at its %v deadline: no sooner than %v, and before %v",
			what, held, deadline, least, limit)
	}
}

func sum(deadlines []time.Duration) time.Duration {
	var total time.Duration
	for _, d := range deadlines {
		total += d
	}
	return total
}
