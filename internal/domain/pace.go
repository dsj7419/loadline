package domain

import (
	"math"
	"time"
)

// The lengths of the rate-limit windows.
const (
	FiveHourLength = 5 * time.Hour
	SevenDayLength = 7 * 24 * time.Hour
)

// minPaceElapsed is how far into a window its pace starts to mean anything:
// before then, a few minutes of work would read as a runaway burn.
const minPaceElapsed = time.Hour

// Pace is how fast a window's usage has burned so far, against the steady
// burn that reaches 100% exactly at reset: 1 is on that pace, 2 twice as fast.
// The time gone is the window's length less the time left to resetsAt, held
// to within the window, so a reset that has passed counts the whole window.
//
// There is no pace, and ok is false, without a known reset time, a window of
// positive length, a usage that is a finite, non-negative number, and an hour
// of the window gone; nor when the pace is too large for a float64.
func Pace(usedPct float64, resetsAt time.Time, window time.Duration, now time.Time) (pace float64, ok bool) {
	if resetsAt.IsZero() || math.IsNaN(usedPct) || usedPct < 0 {
		return 0, false
	}
	// A window of no length or less has no time gone, so it stops here too.
	elapsed := elapsedOf(window, resetsAt.Sub(now))
	if elapsed < minPaceElapsed {
		return 0, false
	}
	pace = (usedPct / 100) / (float64(elapsed) / float64(window))
	// So does an infinite usage, along with any too large to divide.
	if math.IsInf(pace, 0) {
		return 0, false
	}
	return pace, true
}

// elapsedOf is how much of a window has gone with left still to go, within
// [0, window]. It compares before it subtracts: a reset far in the past makes
// left the most negative Duration, which window-left would overflow.
func elapsedOf(window, left time.Duration) time.Duration {
	switch {
	case left <= 0:
		return window
	case left >= window:
		return 0
	default:
		return window - left
	}
}
