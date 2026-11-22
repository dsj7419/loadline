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
	// sampleHorizon is how far back every sample is kept. Projections read
	// no further; beyond it, only the samples that mark a span on credits
	// stay.
	sampleHorizon = 2 * time.Hour
)

// accountEntry is an account, the usage samples of its windows and what its
// credits are worked out from.
type accountEntry struct {
	account domain.Account
	samples domain.WindowSamples
	credits accountCredits
}

// observe applies the rate limits a status line reports. A line that reports
// no window is no observation of them. A newer line replaces both windows,
// since Claude Code drops a window from the line once it resets. It returns
// whether a window's values moved, as opposed to being observed afresh, and
// the windows that gained a sample.
func (a *accountEntry) observe(l domain.Limits) (moved bool, sampled domain.Limits) {
	if l.FiveHour == nil && l.SevenDay == nil {
		return false, domain.Limits{}
	}
	current := a.account.Limits
	if !l.ObservedAt.After(current.ObservedAt) {
		return false, domain.Limits{}
	}
	moved = !sameWindow(current.FiveHour, l.FiveHour) || !sameWindow(current.SevenDay, l.SevenDay)
	a.account.Limits = domain.Limits{FiveHour: cloneWindow(l.FiveHour), SevenDay: cloneWindow(l.SevenDay), ObservedAt: l.ObservedAt}

	sampled.ObservedAt = l.ObservedAt
	a.samples.FiveHour, sampled.FiveHour = sampleWindow(a.samples.FiveHour, l.FiveHour, l.ObservedAt)
	a.samples.SevenDay, sampled.SevenDay = sampleWindow(a.samples.SevenDay, l.SevenDay, l.ObservedAt)
	return moved, sampled
}

// sampleWindow adds the sample a line at at gives of a window, w being nil
// when the line no longer reports it, and returns the window it sampled.
func sampleWindow(samples []domain.Sample, w *domain.Window, at time.Time) ([]domain.Sample, *domain.Window) {
	if w == nil {
		w = droppedAtLimit(samples)
	}
	samples, ok := appendSample(samples, w, at)
	if !ok {
		return samples, nil
	}
	return samples, cloneWindow(w)
}

// droppedAtLimit returns the window a line dropped while its last sample was
// at the limit with no known reset time, as back at 0%: Claude Code drops a
// window once it resets, and the reset time it never gave cannot end the
// span on credits by itself. A window dropped otherwise needs no sample.
func droppedAtLimit(samples []domain.Sample) *domain.Window {
	if n := len(samples); n > 0 && samples[n-1].AtLimit() && samples[n-1].ResetsAt.IsZero() {
		return &domain.Window{}
	}
	return nil
}

func sameWindow(a, b *domain.Window) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.UsedPct == b.UsedPct && a.ResetsAt.Equal(b.ResetsAt)
}

// appendSample adds w's usage at at to samples when it or the window's reset
// time differs from the last sample's, or when sampleInterval has passed
// since that one.
func appendSample(samples []domain.Sample, w *domain.Window, at time.Time) ([]domain.Sample, bool) {
	if w == nil {
		return samples, false
	}
	if n := len(samples); n > 0 {
		last := samples[n-1]
		if last.UsedPct == w.UsedPct && last.ResetsAt.Equal(w.ResetsAt) && at.Sub(last.At) < sampleInterval {
			return samples, false
		}
	}
	return append(samples, domain.Sample{At: at, UsedPct: w.UsedPct, ResetsAt: w.ResetsAt}), true
}

// restore merges samples kept from an earlier run into the account's.
func (a *accountEntry) restore(s domain.WindowSamples) {
	a.samples.FiveHour = mergeSamples(a.samples.FiveHour, s.FiveHour)
	a.samples.SevenDay = mergeSamples(a.samples.SevenDay, s.SevenDay)
}

func mergeSamples(current, restored []domain.Sample) []domain.Sample {
	merged := slices.Concat(restored, current)
	slices.SortStableFunc(merged, func(x, y domain.Sample) int { return x.At.Compare(y.At) })
	return merged
}

// pruneSamples keeps of the samples taken before horizon only the marks of
// the spans on credits, and of those none taken before oldest but one for the
// span still running then.
func (a *accountEntry) pruneSamples(horizon, oldest time.Time) {
	a.samples.FiveHour = pruneWindow(a.samples.FiveHour, horizon, oldest)
	a.samples.SevenDay = pruneWindow(a.samples.SevenDay, horizon, oldest)
}

// pruneWindow prunes one window's samples, which are in time order.
func pruneWindow(samples []domain.Sample, horizon, oldest time.Time) []domain.Sample {
	recent := slices.IndexFunc(samples, func(s domain.Sample) bool { return !s.At.Before(horizon) })
	if recent < 0 {
		recent = len(samples)
	}
	if recent == 0 {
		return samples
	}
	return slices.Concat(domain.ForgetOnCreditsBefore(domain.OnCreditsMarks(samples[:recent]), oldest), samples[recent:])
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
