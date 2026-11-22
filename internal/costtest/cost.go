// Package costtest checks that the cost of an operation grows linearly with
// the size of what it works on, by timing it at two sizes. A proportion of
// two times holds on any machine, with or without the race detector, where a
// bound in milliseconds holds only on a machine as fast as the one it was set
// on.
package costtest

import (
	"math"
	"runtime"
	"time"
)

const (
	// growth is how many times larger the second size AssertLinear times an
	// operation at is than the first.
	growth = 4
	// slack is how many times longer than a linear cost the operation may
	// take at the larger size: halfway, in proportion, to a quadratic one.
	slack = 2
	// runs is how many times AssertLinear times the operation at each size.
	runs = 5
	// batch is how long timeOf goes on running an operation. Go's clock on
	// Windows steps at the system timer's tick, which can be as coarse as
	// 15.6 ms, so a single run of a few milliseconds cannot be timed.
	batch = 100 * time.Millisecond
)

// T is the part of testing.TB that AssertLinear reports through.
type T interface {
	Helper()
	Errorf(format string, args ...any)
}

// AssertLinear fails t unless an operation's cost grows linearly with its
// size. build returns the operation at a size, counted in unit; AssertLinear
// builds it at n and at 4n, and fails unless it takes under 8 times as long
// at 4n as at n: a linear cost takes 4 times as long, and a quadratic one 16.
// Each time is the fastest of 5, taken in turn with the other size's, so a
// stall in one decides nothing.
func AssertLinear(t T, n int, unit string, build func(size int) (op func())) {
	t.Helper()
	sizes := [2]int{n, growth * n}
	ops := [2]func(){build(sizes[0]), build(sizes[1])}
	fastest := [2]time.Duration{math.MaxInt64, math.MaxInt64}
	for range runs {
		for i, op := range ops {
			fastest[i] = min(fastest[i], timeOf(op))
		}
	}
	if ratio := float64(fastest[1]) / float64(fastest[0]); ratio >= growth*slack {
		t.Errorf("took %v at %d %s and %v at %d: %.1f times as long, want under %d, as a cost linear in the %s takes %d times as long",
			fastest[0], sizes[0], unit, fastest[1], sizes[1], ratio, growth*slack, unit, growth)
	}
}

// timeOf returns how long op takes on average, run over and over for at
// least batch. It starts after a collection, so that the garbage of whatever
// ran before is not charged to op.
func timeOf(op func()) time.Duration {
	runtime.GC()
	start := time.Now()
	for count := 1; ; count++ {
		op()
		if elapsed := time.Since(start); elapsed >= batch {
			return elapsed / time.Duration(count)
		}
	}
}
