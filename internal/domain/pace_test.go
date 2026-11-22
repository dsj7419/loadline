package domain_test

import (
	"math"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

func TestPace(t *testing.T) {
	const day = 24 * time.Hour
	week, fiveHours := domain.SevenDayLength, domain.FiveHourLength
	for _, tc := range []struct {
		name     string
		usedPct  float64
		resetsAt time.Time
		window   time.Duration
		want     float64
		ok       bool
	}{
		{"50% used with 3.5 days of 7 left is on pace", 50, t0.Add(84 * time.Hour), week, 1, true},
		{"100% used with 4 days of 7 left", 100, t0.Add(4 * day), week, 7.0 / 3, true},
		{"38% used with 2 hours of 5 left", 38, t0.Add(2 * time.Hour), fiveHours, 0.38 / 0.6, true},
		{"nothing used is a pace of 0", 0, t0.Add(3 * day), week, 0, true},
		{"exactly an hour in", 10, t0.Add(4 * time.Hour), fiveHours, 0.5, true},
		{"a second short of an hour in", 10, t0.Add(4*time.Hour + time.Second), fiveHours, 0, false},
		{"30 minutes in", 10, t0.Add(week - 30*time.Minute), week, 0, false},
		{"more time left than the window holds", 10, t0.Add(8 * day), week, 0, false},
		{"a reset days past counts the whole window", 40, t0.Add(-3 * day), week, 0.4, true},
		{"a reset now counts the whole window", 40, t0, week, 0.4, true},
		{"a reset centuries ago counts the whole window", 40, time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC), week, 0.4, true},
		{"an unknown reset", 50, time.Time{}, week, 0, false},
		{"a window with no length", 50, t0.Add(time.Hour), 0, 0, false},
		{"a window of negative length", 50, t0.Add(-2 * time.Hour), -time.Hour, 0, false},
		{"usage that is not a number", math.NaN(), t0.Add(3 * day), week, 0, false},
		{"infinite usage", math.Inf(1), t0.Add(3 * day), week, 0, false},
		{"negative infinite usage", math.Inf(-1), t0.Add(3 * day), week, 0, false},
		{"negative usage", -1, t0.Add(3 * day), week, 0, false},
		{"usage so large its pace overflows", math.MaxFloat64, t0.Add(week - time.Hour), week, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := domain.Pace(tc.usedPct, tc.resetsAt, tc.window, t0)
			if ok != tc.ok || !near(got, tc.want) {
				t.Errorf("Pace(%v, %v, %v, %v) = %v, %v; want %v, %v", tc.usedPct, tc.resetsAt, tc.window, t0, got, ok, tc.want, tc.ok)
			}
		})
	}
}
