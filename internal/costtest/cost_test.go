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

// A run that stalls, as on a loaded machine, is not the fastest, so it
// decides nothing.
func TestAssertLinear_ALinearCostThatStallsOncePasses(t *testing.T) {
	var r recorder

	costtest.AssertLinear(&r, 100_000, "steps", func(size int) func() {
		stalls := size > 100_000
		return func() {
			if stalls {
				stalls = false
				time.Sleep(200 * time.Millisecond)
			}
			work(size)
		}
	})

	if len(r.errors) != 0 {
		t.Errorf("errors %q for a linear cost that stalled once at the larger size, want none", r.errors)
	}
}
