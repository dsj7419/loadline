package tracker

import (
	"slices"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

const (
	// sampleInterval is how often a window that holds steady is sampled
	// anyway: a flat stretch is evidence of a flat trend.
	sampleInterval = 60 * time.Second
	// sampleHorizon is how far back samples are kept. Projections read no
	// further.
	sampleHorizon = 2 * time.Hour
)

// WindowSamples holds an account's usage samples for each rate-limit window.
type WindowSamples struct {
	FiveHour, SevenDay []domain.Sample
}

// accountEntry is an account and the usage samples of its windows.
type accountEntry struct {
	account domain.Account
	samples WindowSamples
}

// observe applies the rate limits a status line reports. A line that reports
// no window is no observation of them. A newer line replaces both windows,
// since Claude Code drops a window from the line once it resets. It returns
// whether the limits changed and the windows that gained a sample.
func (a *accountEntry) observe(l domain.Limits) (changed bool, sampled domain.Limits) {
	if l.FiveHour == nil && l.SevenDay == nil {
		return false, domain.Limits{}
	}
	if !l.ObservedAt.After(a.account.Limits.ObservedAt) {
		return false, domain.Limits{}
	}
	a.account.Limits = domain.Limits{FiveHour: cloneWindow(l.FiveHour), SevenDay: cloneWindow(l.SevenDay), ObservedAt: l.ObservedAt}

	sampled.ObservedAt = l.ObservedAt
	var ok bool
	if a.samples.FiveHour, ok = appendSample(a.samples.FiveHour, l.FiveHour, l.ObservedAt); ok {
		sampled.FiveHour = cloneWindow(l.FiveHour)
	}
	if a.samples.SevenDay, ok = appendSample(a.samples.SevenDay, l.SevenDay, l.ObservedAt); ok {
		sampled.SevenDay = cloneWindow(l.SevenDay)
	}
	return true, sampled
}

// appendSample adds w's usage at at to samples when it differs from the last
// sample, or when sampleInterval has passed since that one.
func appendSample(samples []domain.Sample, w *domain.Window, at time.Time) ([]domain.Sample, bool) {
	if w == nil {
		return samples, false
	}
	if n := len(samples); n > 0 {
		last := samples[n-1]
		if last.UsedPct == w.UsedPct && at.Sub(last.At) < sampleInterval {
			return samples, false
		}
	}
	return append(samples, domain.Sample{At: at, UsedPct: w.UsedPct}), true
}

// restore merges samples kept from an earlier run into the account's.
func (a *accountEntry) restore(s WindowSamples) {
	a.samples.FiveHour = mergeSamples(a.samples.FiveHour, s.FiveHour)
	a.samples.SevenDay = mergeSamples(a.samples.SevenDay, s.SevenDay)
}

func mergeSamples(current, restored []domain.Sample) []domain.Sample {
	merged := slices.Concat(restored, current)
	slices.SortStableFunc(merged, func(x, y domain.Sample) int { return x.At.Compare(y.At) })
	return merged
}

// pruneSamples forgets the samples taken before oldest.
func (a *accountEntry) pruneSamples(oldest time.Time) {
	before := func(s domain.Sample) bool { return s.At.Before(oldest) }
	a.samples.FiveHour = slices.DeleteFunc(a.samples.FiveHour, before)
	a.samples.SevenDay = slices.DeleteFunc(a.samples.SevenDay, before)
}

// currentLimits returns the account's limits as they stand at now. A window
// whose reset time has passed is gone until a status line reports it again;
// one with an unknown reset time stays.
func (a *accountEntry) currentLimits(now time.Time) domain.Limits {
	limits := a.account.Limits
	limits.FiveHour = unexpired(limits.FiveHour, now)
	limits.SevenDay = unexpired(limits.SevenDay, now)
	return limits
}

func unexpired(w *domain.Window, now time.Time) *domain.Window {
	if w == nil || (!w.ResetsAt.IsZero() && !w.ResetsAt.After(now)) {
		return nil
	}
	return w
}

// projections projects each of the windows in limits from its samples.
func (a *accountEntry) projections(limits domain.Limits, now time.Time) domain.WindowProjections {
	return domain.WindowProjections{
		FiveHour: project(a.samples.FiveHour, limits.FiveHour, now),
		SevenDay: project(a.samples.SevenDay, limits.SevenDay, now),
	}
}

// project returns the window's projection; an absent window, or one with an
// unknown reset time, has none.
func project(samples []domain.Sample, w *domain.Window, now time.Time) domain.Projection {
	if w == nil {
		return domain.Projection{}
	}
	return domain.Project(samples, w.ResetsAt, now)
}

// cloneWindow copies w, so the tracker never shares a window its caller can
// change.
func cloneWindow(w *domain.Window) *domain.Window {
	if w == nil {
		return nil
	}
	c := *w
	return &c
}
