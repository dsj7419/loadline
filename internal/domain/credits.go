package domain

import (
	"slices"
	"time"
)

// limitPct is the usage, in percent, at which a window's plan limit is
// reached. Past it, a subscription that has usage credits keeps working on
// them.
const limitPct = 100

// creditsLowShare is the share of an account's credit balance whose spending
// raises a credits_low alert.
const creditsLowShare = 0.8

// CreditGrant is usage credit the owner added to an account, as the config
// records it.
type CreditGrant struct {
	USD     float64
	AddedAt time.Time
}

// OnCreditsSpan is a stretch of time an account spent at a plan limit, when
// every call it made was paid for with usage credits. It runs from From up
// to, but not including, To; To is zero while the span is still open.
type OnCreditsSpan struct{ From, To time.Time }

// Holds reports whether at falls inside the span.
func (s OnCreditsSpan) Holds(at time.Time) bool {
	return !at.Before(s.From) && (s.To.IsZero() || at.Before(s.To))
}

// SpanCursor finds the span that holds each of a run of times, in spans as
// OnCreditsSpans returns them: in time order, no two overlapping. Asked about
// times in time order, it walks the spans once however many it is asked
// about, so a month of spans and calls is read in one pass.
type SpanCursor struct {
	spans []OnCreditsSpan
	next  int
}

// NewSpanCursor returns a cursor at the start of spans.
func NewSpanCursor(spans []OnCreditsSpan) SpanCursor {
	return SpanCursor{spans: spans}
}

// Find returns the index of the span that holds at, and whether one does. at
// must be no earlier than the time the cursor was last asked about.
func (c *SpanCursor) Find(at time.Time) (int, bool) {
	for c.next < len(c.spans) && endsBy(c.spans[c.next], at) {
		c.next++
	}
	if c.next < len(c.spans) && c.spans[c.next].Holds(at) {
		return c.next, true
	}
	return -1, false
}

// endsBy reports whether the span has ended by at.
func endsBy(s OnCreditsSpan, at time.Time) bool {
	return !s.To.IsZero() && !at.Before(s.To)
}

// WindowSamples holds an account's usage samples for each rate-limit window.
type WindowSamples struct {
	FiveHour, SevenDay []Sample
}

// SpendLimit is a spend limit as a status line reports it: what has been
// spent against it and how much it allows, in US dollars.
type SpendLimit struct {
	UsedUSD, LimitUSD float64
}

// CreditSource says where a CreditEstimate's figures come from.
type CreditSource string

const (
	// CreditSourceEstimate figures are worked out from the cost of the calls
	// made at a plan limit, against the grants the owner recorded.
	CreditSourceEstimate CreditSource = "estimate"
	// CreditSourceReported figures are a spend limit's, as the status line
	// reported them.
	CreditSourceReported CreditSource = "reported"
)

// CreditEstimate is where an account's usage credits stand, in US dollars.
type CreditEstimate struct {
	BalanceUSD, SpentUSD, RemainingUSD float64
	// OnCreditsSince is when the open span began while Active, and zero
	// otherwise.
	OnCreditsSince time.Time
	// Active reports that the account is on credits now: a span is open and
	// a call was made inside it, which only credits would have paid for.
	Active bool
	Source CreditSource
}

// OnCreditsSpans returns the stretches of time the account spent at a limit,
// in time order: a span opens at the first sample at or over 100% of either
// window, and closes at the first sample of that window below 100%, or at
// the window's reset, whichever comes first. A reset that has passed by now
// closes the span even when no sample has come since, since Claude Code stops
// reporting a window once it resets. The windows' spans are merged, so no
// moment is in two spans. Samples may come in any order; the caller's slices
// are not changed.
func OnCreditsSpans(samples WindowSamples, now time.Time) []OnCreditsSpan {
	return mergeSpans(windowSpans(samples.FiveHour, now), windowSpans(samples.SevenDay, now))
}

// mergeSpans merges two windows' spans, each in time order, in one pass,
// joining those that overlap or meet.
func mergeSpans(a, b []OnCreditsSpan) []OnCreditsSpan {
	merged := make([]OnCreditsSpan, 0, len(a)+len(b))
	for len(a) > 0 || len(b) > 0 {
		var next OnCreditsSpan
		if len(b) == 0 || (len(a) > 0 && !b[0].From.Before(a[0].From)) {
			next, a = a[0], a[1:]
		} else {
			next, b = b[0], b[1:]
		}
		if n := len(merged); n > 0 && reachesInto(merged[n-1], next.From) {
			merged[n-1].To = laterEnd(merged[n-1].To, next.To)
			continue
		}
		merged = append(merged, next)
	}
	return merged
}

// spanWalk follows one window's samples in time order, opening and closing
// its spans.
type spanWalk struct {
	// keepsSpans says the walk keeps the spans it closes, in spans. A walk
	// that needs only where spans open and close leaves it unset, and so
	// allocates nothing.
	keepsSpans bool
	spans      []OnCreditsSpan
	// opened counts the spans the walk has opened.
	opened int
	open   bool
	from   time.Time
	// resetsAt is the newest reset time the open span's samples gave, or
	// zero when none did.
	resetsAt time.Time
}

// step takes the next sample, and reports whether it changed the walk: closed
// a span, opened one or moved the open one's reset time.
func (w *spanWalk) step(s Sample) bool {
	changed := false
	if w.open && reached(w.resetsAt, s.At) {
		w.close(w.resetsAt)
		changed = true
	}
	if !s.AtLimit() {
		if w.open {
			w.close(s.At)
			changed = true
		}
		return changed
	}
	if !w.open {
		w.open, w.from, w.resetsAt = true, s.At, time.Time{}
		w.opened++
		changed = true
	}
	if !s.ResetsAt.IsZero() && !s.ResetsAt.Equal(w.resetsAt) {
		w.resetsAt = s.ResetsAt
		changed = true
	}
	return changed
}

func (w *spanWalk) close(at time.Time) {
	if w.keepsSpans {
		w.spans = append(w.spans, OnCreditsSpan{From: w.from, To: at})
	}
	w.open = false
}

// windowSpans returns one window's spans, in time order. A first walk counts
// them, so that the second keeps them in a slice of their size: one grown as
// the walk went would leave several times their size behind, in every view.
func windowSpans(samples []Sample, now time.Time) []OnCreditsSpan {
	ordered := inTimeOrder(samples)
	var counting spanWalk
	for _, s := range ordered {
		counting.step(s)
	}
	w := spanWalk{keepsSpans: true, spans: make([]OnCreditsSpan, 0, counting.opened)}
	for _, s := range ordered {
		w.step(s)
	}
	if w.open {
		var to time.Time
		if reached(w.resetsAt, now) {
			to = w.resetsAt
		}
		w.spans = append(w.spans, OnCreditsSpan{From: w.from, To: to})
	}
	return w.spans
}

// AtLimit reports whether the sample shows its window at the limit. A reading
// taken after the window's own reset time describes a window that is gone.
func (s Sample) AtLimit() bool {
	return s.UsedPct >= limitPct && !reached(s.ResetsAt, s.At)
}

// reached reports whether a known reset time has come by t.
func reached(resetsAt, t time.Time) bool {
	return !resetsAt.IsZero() && !t.Before(resetsAt)
}

// reachesInto reports whether a span runs on to at, or meets it.
func reachesInto(s OnCreditsSpan, at time.Time) bool {
	return s.To.IsZero() || !s.To.Before(at)
}

// laterEnd is the later of two span ends, an open end being the latest.
func laterEnd(a, b time.Time) time.Time {
	if a.IsZero() || b.IsZero() {
		return time.Time{}
	}
	if b.After(a) {
		return b
	}
	return a
}

// inTimeOrder returns samples in time order: themselves when they are, and a
// sorted copy otherwise.
func inTimeOrder(samples []Sample) []Sample {
	byTime := func(a, b Sample) int { return a.At.Compare(b.At) }
	if slices.IsSortedFunc(samples, byTime) {
		return samples
	}
	sorted := slices.Clone(samples)
	slices.SortStableFunc(sorted, byTime)
	return sorted
}

// OnCreditsMarks returns, in time order, the samples that OnCreditsSpans
// needs from samples: each one that opens or closes a span, or moves the
// reset time of the one open. The spans of the marks followed by any later
// samples are the spans of all the samples followed by them, so a keeper of
// samples can forget the rest once no projection reads them. Samples in time
// order that are all marks already are returned as they are, not copied.
func OnCreditsMarks(samples []Sample) []Sample {
	ordered := inTimeOrder(samples)
	var w spanWalk
	for i, s := range ordered {
		if w.step(s) {
			continue
		}
		// The first sample that is no mark: from here the marks are copied,
		// into room for every sample but this one.
		marks := make([]Sample, i, len(ordered)-1)
		copy(marks, ordered[:i])
		for _, s := range ordered[i+1:] {
			if w.step(s) {
				marks = append(marks, s)
			}
		}
		return marks
	}
	return ordered
}

// ForgetOnCreditsBefore returns samples, which must be in time order, without
// those taken before oldest, but for the span they leave open at oldest: one
// sample at the limit stands for it, dated at the span's start, which dates
// the account going on credits, and with the reset time the span had come to
// by then. The spans from oldest on are those of samples, their starts
// included, so a span never ends before the calls it holds have aged out.
// That sample depends on the samples before oldest alone, so forgetting
// before one time and then before a later one forgets what forgetting before
// the later one does. Unless a span runs across oldest, the samples kept are
// not copied: the result is the end of samples.
func ForgetOnCreditsBefore(samples []Sample, oldest time.Time) []Sample {
	cut, _ := slices.BinarySearchFunc(samples, oldest, func(s Sample, t time.Time) int { return s.At.Compare(t) })
	var w spanWalk
	for _, s := range samples[:cut] {
		w.step(s)
	}
	kept := samples[cut:]
	if !w.open || reached(w.resetsAt, oldest) {
		return kept
	}
	return slices.Concat([]Sample{{At: w.from, UsedPct: limitPct, ResetsAt: w.resetsAt}}, kept)
}

// EstimateCredits works out an account's credits from the grants the owner
// recorded and the calls it made while at a limit. The balance is the sum of
// the grants. The spend is the cost of the calls inside a span made at or
// after the earliest grant, as nothing before it was paid from the balance;
// a later grant only adds to the balance. Costs that are not positive and
// finite count for nothing, and sums stop at the largest finite amount, as
// Shares' do. The account is Active while a span holds at now and a call,
// whatever it cost, was made inside it.
//
// The spans are as OnCreditsSpans returns them. The requests may come in any
// order; in time order, as the tracker keeps them, they and the spans are
// read together in one pass.
func EstimateCredits(grants []CreditGrant, spans []OnCreditsSpan, requests []APIRequest, now time.Time) CreditEstimate {
	estimate := CreditEstimate{Source: CreditSourceEstimate}
	var earliest time.Time
	granted := false
	for _, g := range grants {
		if !isSpendable(g.USD) {
			continue
		}
		estimate.BalanceUSD = addSpend(estimate.BalanceUSD, g.USD)
		if !granted || g.AddedAt.Before(earliest) {
			earliest, granted = g.AddedAt, true
		}
	}

	current := slices.IndexFunc(spans, func(s OnCreditsSpan) bool { return s.To.IsZero() || s.To.After(now) })
	holding := NewSpanCursor(spans)
	for _, r := range inTimeOrderOfRequests(requests) {
		inside, held := holding.Find(r.At)
		if !held {
			continue
		}
		if inside == current {
			estimate.Active, estimate.OnCreditsSince = true, spans[current].From
		}
		if granted && !r.At.Before(earliest) && isSpendable(r.CostUSD) {
			estimate.SpentUSD = addSpend(estimate.SpentUSD, r.CostUSD)
		}
	}
	estimate.RemainingUSD = max(0, estimate.BalanceUSD-estimate.SpentUSD)
	return estimate
}

// inTimeOrderOfRequests returns requests in time order: themselves when they
// are, and a sorted copy otherwise.
func inTimeOrderOfRequests(requests []APIRequest) []APIRequest {
	byTime := func(a, b APIRequest) int { return a.At.Compare(b.At) }
	if slices.IsSortedFunc(requests, byTime) {
		return requests
	}
	sorted := slices.Clone(requests)
	slices.SortStableFunc(sorted, byTime)
	return sorted
}

// WithReported returns e with its figures replaced by those of a spend limit
// a status line reported, which are better than any estimate. Both figures
// come from the report, never one from each source. Whether the account is
// on credits still comes from its usage, which the report says nothing of.
func (e CreditEstimate) WithReported(limit SpendLimit) CreditEstimate {
	e.BalanceUSD, e.SpentUSD = limit.LimitUSD, limit.UsedUSD
	e.RemainingUSD = max(0, limit.LimitUSD-limit.UsedUSD)
	e.Source = CreditSourceReported
	return e
}

// creditsLow reports whether an account has spent creditsLowShare of a
// balance it has.
func creditsLow(e CreditEstimate) bool {
	return e.BalanceUSD > 0 && e.SpentUSD >= creditsLowShare*e.BalanceUSD
}
