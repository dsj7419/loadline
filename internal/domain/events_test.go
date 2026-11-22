package domain_test

import (
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

func TestValidEventTime(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"an ordinary time", t0, true},
		{"the zero time", time.Time{}, false},
		{"the epoch", time.Unix(0, 0), false},
		{"the first instant after the epoch", time.Unix(0, 1), true},
		{"the last instant of year 9999", time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), true},
		{"year 10000", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"year 9999 where written, 10000 in UTC", time.Date(9999, 12, 31, 23, 30, 0, 0, time.FixedZone("", -3600)), false},
		{"1970 where written, 1969 in UTC", time.Date(1970, 1, 1, 0, 30, 0, 0, time.FixedZone("", 3600)), false},
	} {
		if got := domain.ValidEventTime(tc.at); got != tc.want {
			t.Errorf("ValidEventTime(%s) = %t, want %t", tc.name, got, tc.want)
		}
	}
}
