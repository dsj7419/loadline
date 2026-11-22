package deadlinetest_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/deadlinetest"
)

// recorder is a deadlinetest.T that keeps the errors it is given.
type recorder struct{ errors []string }

func (*recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

const ms = time.Millisecond

func TestLimit_IsTheSumAndHalfTheLongest(t *testing.T) {
	for _, tt := range []struct {
		deadlines []time.Duration
		want      time.Duration
	}{
		{deadlines: []time.Duration{400 * ms}, want: 600 * ms},
		{deadlines: []time.Duration{300 * ms, 300 * ms}, want: 750 * ms},
		{deadlines: []time.Duration{100 * ms, 400 * ms}, want: 700 * ms},
	} {
		if got := deadlinetest.Limit(tt.deadlines...); got != tt.want {
			t.Errorf("Limit(%v) = %v, want %v", tt.deadlines, got, tt.want)
		}
	}
}

func TestAssertGaveUpAt(t *testing.T) {
	check, send := 300*ms, 300*ms
	for _, tt := range []struct {
		name    string
		elapsed time.Duration
		fails   bool
	}{
		{name: "before both deadlines have passed", elapsed: 600*ms - time.Nanosecond, fails: true},
		{name: "as they pass", elapsed: 600 * ms},
		{name: "just before the limit", elapsed: 750*ms - time.Nanosecond},
		{name: "at the limit", elapsed: 750 * ms, fails: true},
		{name: "with one deadline waited out twice", elapsed: 900 * ms, fails: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var r recorder

			deadlinetest.AssertGaveUpAt(&r, "the call", tt.elapsed, check, send)

			if !tt.fails {
				if len(r.errors) != 0 {
					t.Errorf("errors %q after %v, want none", r.errors, tt.elapsed)
				}
				return
			}
			if len(r.errors) != 1 || !strings.Contains(r.errors[0], "the call returned after "+tt.elapsed.String()) {
				t.Errorf("errors %q after %v, want one naming the call and how long it took", r.errors, tt.elapsed)
			}
		})
	}
}
