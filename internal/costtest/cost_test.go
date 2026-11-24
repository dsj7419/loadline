package costtest_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/costtest"
)

// recorder is a costtest.T that keeps the errors it is given.
type recorder struct{ errors []string }

func (*recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// sink keeps the work below from being optimized away.
var sink int

// work takes steps steps.
func work(steps int) {
	sum := 0
	for i := range steps {
		sum += i
	}
	sink += sum
}

func TestAssertLinear_PassesALinearCost(t *testing.T) {
	var r recorder

	costtest.AssertLinear(&r, 100_000, "steps", func(size int) func() {
		return func() { work(size) }
	})

	if len(r.errors) != 0 {
		t.Errorf("errors %q for a linear cost, want none", r.errors)
	}
}

func TestAssertLinear_FailsAQuadraticCost(t *testing.T) {
	var r recorder
	var built []int

	costtest.AssertLinear(&r, 1000, "items", func(size int) func() {
		built = append(built, size)
		return func() { work(size * size) }
	})

	if !slices.Equal(built, []int{1000, 4000}) {
		t.Errorf("built the operation at %v, want at n and 4n", built)
	}
	if len(r.errors) != 1 || !strings.Contains(r.errors[0], "at 1000 items") || !strings.Contains(r.errors[0], "at 4000") {
		t.Errorf("errors %q for a quadratic cost, want one naming both sizes", r.errors)
	}
}

// constant and cubic are how many steps an operation of a size takes: the
// one as many at either size, the other 64 times as many at 4 times the
// size, far below and far above the limit of 8 times as long.
func constant(int) int   { return 100_000 }
func cubic(size int) int { return size * size * size }

// roundCounter tells an operation which of AssertLinear's rounds it runs in,
// from nothing but the order AssertLinear runs the two sizes in: a round
// times the smaller and then the larger, so the smaller running after the
// larger begins the next round.
type roundCounter struct {
	round       int
	afterLarger bool
}

// ran notes a run at the larger size or the smaller, and returns the round
// it falls in, counting from 0.
func (c *roundCounter) ran(larger bool) int {
	if c.afterLarger && !larger {
		c.round++
	}
	c.afterLarger = larger
	return c.round
}

// A time that stalls, as on a loaded machine, puts its round's proportion at
// one end of the seven, whichever round it falls in, and the median ignores
// up to three such rounds. A stall at the larger size raises its round's
// proportion, so a cost that passes still passes; one at the smaller lowers
// it, so a cost that fails still fails. Each stalled round leaves the median
// less room for noise in the others, so the costs sit far from the limit,
// where noise in the rounds a stall leaves alone cannot take them across it.
// The three stalled rounds all come among the first five, so a call of five
// rounds, whose median three stalls decide, fails those rows.
func TestAssertLinear_StallsInUpToThreeRoundsDecideNothing(t *testing.T) {
	for _, tt := range []struct {
		name  string
		n     int
		steps func(size int) int
		// atLarger is whether the stalls come at the larger size or the
		// smaller, and rounds which rounds they come in.
		atLarger bool
		rounds   []int
		fails    bool
	}{
		{name: "constant, at the larger size in round 0", n: 100_000, steps: constant, atLarger: true, rounds: []int{0}},
		{name: "constant, at the larger size in round 3", n: 100_000, steps: constant, atLarger: true, rounds: []int{3}},
		{name: "constant, at the larger size in round 6", n: 100_000, steps: constant, atLarger: true, rounds: []int{6}},
		{name: "constant, at the larger size in rounds 0, 2 and 4", n: 100_000, steps: constant, atLarger: true, rounds: []int{0, 2, 4}},
		{name: "cubic, at the smaller size in round 0", n: 25, steps: cubic, rounds: []int{0}, fails: true},
		{name: "cubic, at the smaller size in round 3", n: 25, steps: cubic, rounds: []int{3}, fails: true},
		{name: "cubic, at the smaller size in round 6", n: 25, steps: cubic, rounds: []int{6}, fails: true},
		{name: "cubic, at the smaller size in rounds 0, 2 and 4", n: 25, steps: cubic, rounds: []int{0, 2, 4}, fails: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var r recorder
			var rounds roundCounter
			stalled := make(map[int]bool)

			costtest.AssertLinear(&r, tt.n, "items", func(size int) func() {
				larger := size > tt.n
				return func() {
					round := rounds.ran(larger)
					if larger == tt.atLarger && slices.Contains(tt.rounds, round) && !stalled[round] {
						stalled[round] = true
						time.Sleep(200 * time.Millisecond)
					}
					work(tt.steps(size))
				}
			})

			if failed := len(r.errors) != 0; failed != tt.fails {
				t.Errorf("errors %q for a cost that stalled (%s), want failed %t", r.errors, tt.name, tt.fails)
			}
		})
	}
}

// A machine that slows partway through, as when other work starts beside the
// test, slows both sizes from then on. Here it runs 64 times slower from the
// time at the larger size in one round on, so that round alone is taken at
// two speeds. That decides nothing, whichever round it begins in, while a
// cubic cost still fails. The fastest time at each size, or the median time
// at each size, would come from rounds at different speeds: from round 0 the
// one, and from the middle round the other, would fail the constant cost.
func TestAssertLinear_AMachineThatSlowsPartwayDecidesNothing(t *testing.T) {
	for _, tt := range []struct {
		name  string
		n     int
		steps func(size int) int
		// from is the round whose time at the larger size is the first taken
		// on the slower machine.
		from  int
		fails bool
	}{
		{name: "constant, from round 0", n: 100_000, steps: constant, from: 0},
		{name: "constant, from round 3", n: 100_000, steps: constant, from: 3},
		{name: "cubic, from round 0", n: 25, steps: cubic, from: 0, fails: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var r recorder
			var rounds roundCounter
			speed := 1

			costtest.AssertLinear(&r, tt.n, "items", func(size int) func() {
				larger := size > tt.n
				return func() {
					if rounds.ran(larger) == tt.from && larger {
						speed = 64
					}
					work(tt.steps(size) * speed)
				}
			})

			if failed := len(r.errors) != 0; failed != tt.fails {
				t.Errorf("errors %q for a cost on a machine that slowed partway (%s), want failed %t", r.errors, tt.name, tt.fails)
			}
		})
	}
}
