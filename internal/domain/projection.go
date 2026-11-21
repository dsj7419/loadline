package domain

import (
	"math"
	"slices"
	"time"
)

const (
	// projectionWindow is how far back samples still describe the current
	// burn rate.
	projectionWindow = 120 * time.Minute
	// minProjectionSamples and minProjectionSpan are the least evidence a
	// trend needs: a handful of points that are not all from one moment.
	minProjectionSamples = 3
	minProjectionSpan    = 10 * time.Minute
	// resetDropPoints is how far usage must fall between two samples for the
	// fall to be a window reset rather than noise.
	resetDropPoints = 5.0
)

// Sample is a rate-limit window's usage at one moment.
type Sample struct {
	At      time.Time
	UsedPct float64
}

// Projection is where a rate-limit window is heading. The zero value says the
// trend is unknown.
type Projection struct {
	// Known is false when there is too little evidence, or the window has no
	// future reset to project to. The other fields mean nothing then.
	Known bool
	// RatePerHour is the burn rate in percentage points per hour; zero for
	// usage that is flat or falling.
	RatePerHour float64
	// AtReset is the projected usage when the window resets, capped at 100.
	AtReset float64
	// ETA is when usage reaches 100 at the current rate. It is only meaningful
	// while the rate is positive.
	ETA time.Time
	// RunsOutBeforeReset is true when the limit would be hit before the window
	// resets.
	RunsOutBeforeReset bool
}

// WindowProjections holds an account's projection for each of its windows.
// The zero value is unknown for both.
type WindowProjections struct {
	FiveHour, SevenDay Projection
}

// Project fits a straight line to the usage samples of the last two hours and
// extends it to resetsAt. Only the samples since the window last reset count:
// a fall of resetDropPoints or more between two samples marks a reset, and
// everything before it describes the previous window. Samples may come in any
// order; the caller's slice is not changed.
//
// The result is unknown without minProjectionSamples samples spanning
// minProjectionSpan, or when resetsAt is not after now.
func Project(samples []Sample, resetsAt, now time.Time) Projection {
	if !resetsAt.After(now) {
		return Projection{}
	}
	recent := sinceLastReset(recentSamples(samples, now))
	if len(recent) < minProjectionSamples {
		return Projection{}
	}
	first, last := recent[0], recent[len(recent)-1]
	if last.At.Sub(first.At) < minProjectionSpan {
		return Projection{}
	}

	slope := slopePerHour(recent)
	if math.IsNaN(slope) || math.IsInf(slope, 0) {
		return Projection{}
	}
	if slope <= 0 {
		return Projection{Known: true, AtReset: last.UsedPct}
	}

	eta := now.Add(hoursToDuration(max(100-last.UsedPct, 0) / slope))
	return Projection{
		Known:              true,
		RatePerHour:        slope,
		AtReset:            min(100, last.UsedPct+slope*resetsAt.Sub(now).Hours()),
		ETA:                eta,
		RunsOutBeforeReset: eta.Before(resetsAt),
	}
}

// recentSamples returns the finite samples from the projection window, oldest
// first, in a new slice.
func recentSamples(samples []Sample, now time.Time) []Sample {
	oldest := now.Add(-projectionWindow)
	recent := make([]Sample, 0, len(samples))
	for _, s := range samples {
		if s.At.Before(oldest) || math.IsNaN(s.UsedPct) || math.IsInf(s.UsedPct, 0) {
			continue
		}
		recent = append(recent, s)
	}
	slices.SortStableFunc(recent, func(a, b Sample) int { return a.At.Compare(b.At) })
	return recent
}

// sinceLastReset drops the samples before the last reset drop in samples,
// which must be in time order.
func sinceLastReset(samples []Sample) []Sample {
	for i := len(samples) - 1; i > 0; i-- {
		if samples[i-1].UsedPct-samples[i].UsedPct >= resetDropPoints {
			return samples[i:]
		}
	}
	return samples
}

// slopePerHour is the least-squares slope of usage against hours since the
// first sample. It needs samples at two or more distinct times.
func slopePerHour(samples []Sample) float64 {
	n := float64(len(samples))
	origin := samples[0].At
	var sumX, sumY float64
	for _, s := range samples {
		sumX += s.At.Sub(origin).Hours()
		sumY += s.UsedPct
	}
	meanX, meanY := sumX/n, sumY/n

	var covariance, variance float64
	for _, s := range samples {
		dx := s.At.Sub(origin).Hours() - meanX
		covariance += dx * (s.UsedPct - meanY)
		variance += dx * dx
	}
	return covariance / variance
}

// hoursToDuration converts hours to a Duration, saturating where a Duration
// cannot hold the span (about 292 years) instead of wrapping around.
func hoursToDuration(hours float64) time.Duration {
	ns := hours * float64(time.Hour)
	if ns >= math.MaxInt64 {
		return math.MaxInt64
	}
	return time.Duration(ns)
}
