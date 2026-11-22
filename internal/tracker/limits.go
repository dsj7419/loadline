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
	// readings are what the tracker knows of the readings behind the
	// windows the account's limits hold.
	readings windowReadings
	samples  domain.WindowSamples
	credits  accountCredits
}

// windowReadings holds a heldReading for each rate-limit window.
type windowReadings struct{ FiveHour, SevenDay heldReading }

// heldReading is what the tracker knows of the reading behind a window it
// holds, beyond the window itself.
type heldReading struct {
	// takenAt is when the reading was first taken. A line that reports it
	// again leaves it, so it dates how long the window has stood as it is.
	takenAt time.Time
	// freshAs is when the newest response came that the reading's session
	// was known to have got, zero when none was (see responseClock).
	freshAs time.Time
}

// restoredAccount is an account a store kept. Its windows' readings date
// from when it was last observed, the latest they can have been taken. How
// fresh they are is not kept, so it is unknown.
func restoredAccount(a domain.Account) *accountEntry {
	a.Limits.FiveHour, a.Limits.SevenDay = cloneWindow(a.Limits.FiveHour), cloneWindow(a.Limits.SevenDay)
	taken := heldReading{takenAt: a.Limits.ObservedAt}
	return &accountEntry{account: a, readings: windowReadings{FiveHour: taken, SevenDay: taken}}
}

// windowState is one of an account's windows as the tracker holds it.
type windowState struct {
	held    *domain.Window
	reading heldReading
	samples []domain.Sample
	// length is how long the window lasts.
	length time.Duration
}

func (a *accountEntry) fiveHour() windowState {
	return windowState{a.account.Limits.FiveHour, a.readings.FiveHour, a.samples.FiveHour, domain.FiveHourLength}
}

func (a *accountEntry) sevenDay() windowState {
	return windowState{a.account.Limits.SevenDay, a.readings.SevenDay, a.samples.SevenDay, domain.SevenDayLength}
}

// observe applies the rate limits a status line reports, as fresh as freshAs,
// when the newest response its session is known to have got came, zero when
// none is known. A line that reports no window is no observation of them,
// and one observed no later than the limits held is older news. Each window
// is judged on its own (see windowState.observe), and the limits are observed
// afresh when the line was news of either. It returns whether a window's
// values moved, as opposed to being observed afresh, and the windows that
// gained a sample.
func (a *accountEntry) observe(l domain.Limits, freshAs time.Time) (moved bool, sampled domain.Limits) {
	held := a.account.Limits
	if (l.FiveHour == nil && l.SevenDay == nil) || !l.ObservedAt.After(held.ObservedAt) {
		return false, domain.Limits{}
	}
	fiveHour, fiveHourNews, fiveHourSampled := a.fiveHour().observe(l.FiveHour, l.ObservedAt, freshAs)
	sevenDay, sevenDayNews, sevenDaySampled := a.sevenDay().observe(l.SevenDay, l.ObservedAt, freshAs)
	if !fiveHourNews && !sevenDayNews {
		return false, domain.Limits{}
	}
	a.account.Limits = domain.Limits{FiveHour: fiveHour.held, SevenDay: sevenDay.held, ObservedAt: l.ObservedAt}
	a.readings = windowReadings{FiveHour: fiveHour.reading, SevenDay: sevenDay.reading}
	a.samples = domain.WindowSamples{FiveHour: fiveHour.samples, SevenDay: sevenDay.samples}
	moved = !sameWindow(held.FiveHour, fiveHour.held) || !sameWindow(held.SevenDay, sevenDay.held)
	return moved, domain.Limits{FiveHour: fiveHourSampled, SevenDay: sevenDaySampled, ObservedAt: l.ObservedAt}
}

// observe returns the window as a line observed at at, as fresh as freshAs,
// leaves it, the line reading it as reading, or leaving it out when that is
// nil; whether the line was news of the window; and the window it sampled,
// if any.
//
// Each session's line carries the limits of that session's own last
// response, so an idle session goes on reporting an older reading while a
// busy one reports the current one. Against a window held that has not reset
// by at, a fresher line's reading is news whichever way it moves, which takes
// in a reset that kept its reset time, and a staler line is no news of the
// window at all. When neither line is the fresher, the window decides (see
// takes and standsUnreported).
func (w windowState) observe(reading *domain.Window, at, freshAs time.Time) (next windowState, news bool, sampled *domain.Window) {
	if unexpired(w.held, at) != nil {
		switch compareFreshness(freshAs, w.reading.freshAs) {
		case -1:
			return w, false, nil
		case 1:
			if reading != nil {
				return w.take(reading, at, freshAs)
			}
		}
	}
	if reading == nil {
		return w.leftOut(at)
	}
	if !w.takes(reading, at) {
		return w, false, nil
	}
	return w.take(reading, at, freshAs)
}

// take returns the window with reading, from a line observed at at, as
// fresh as freshAs, held in place of the window's last.
func (w windowState) take(reading *domain.Window, at, freshAs time.Time) (next windowState, news bool, sampled *domain.Window) {
	next = w
	next.held = cloneWindow(reading)
	next.reading.freshAs = freshAs
	if !sameWindow(w.held, reading) {
		next.reading.takenAt = at
	}
	next.samples, sampled = sampleWindow(w.samples, reading, at)
	return next, true, sampled
}

// leftOut returns the window as a line observed at at that leaves it out
// leaves it.
func (w windowState) leftOut(at time.Time) (next windowState, news bool, sampled *domain.Window) {
	if w.standsUnreported(at) {
		return w, false, nil
	}
	next = windowState{length: w.length}
	next.samples, sampled = sampleWindow(w.samples, nil, at)
	return next, w.held != nil || sampled != nil, sampled
}

// standsUnreported reports whether the window held stays when a line
// observed at at leaves it out. Claude Code drops a window from the line once
// it resets, so only one whose reset is known and still to come does: the
// session behind the line may not have learned of it yet.
func (w windowState) standsUnreported(at time.Time) bool {
	return w.held != nil && w.held.ResetsAt.After(at)
}

// takes reports whether reading, from a line observed at at that is no
// fresher than the one held, is news of the window. Within one window usage
// never goes down. So, unless the window held has reset by at, a reading of
// the same window, told by its reset time, is news unless it is lower; one
// with a later reset is of a window begun since, and one with an earlier
// reset of a window gone by. Without both reset times, a lower reading is
// news only once the one held was first taken longer ago than the window
// lasts, by when the window has reset.
func (w windowState) takes(reading *domain.Window, at time.Time) bool {
	held := unexpired(w.held, at)
	if held == nil {
		return true
	}
	if held.ResetsAt.IsZero() || reading.ResetsAt.IsZero() {
		return reading.UsedPct >= held.UsedPct || at.Sub(w.reading.takenAt) > w.length
	}
	if !reading.ResetsAt.Equal(held.ResetsAt) {
		return reading.ResetsAt.After(held.ResetsAt)
	}
	return reading.UsedPct >= held.UsedPct
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
