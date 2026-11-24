package domain_test

import (
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/costtest"
	"github.com/dsj7419/loadline/internal/domain"
)

// creditsNow is when the credit tests look at the spans.
var creditsNow = at(10_000)

func sample(sec int, usedPct float64) domain.Sample {
	return domain.Sample{At: at(sec), UsedPct: usedPct}
}

// full is a sample at the limit of a window that resets at resetsSec.
func full(sec, resetsSec int) domain.Sample {
	return domain.Sample{At: at(sec), UsedPct: 100, ResetsAt: at(resetsSec)}
}

// span runs from fromSec to toSec; a toSec of -1 leaves it open.
func span(fromSec, toSec int) domain.OnCreditsSpan {
	s := domain.OnCreditsSpan{From: at(fromSec)}
	if toSec >= 0 {
		s.To = at(toSec)
	}
	return s
}

func TestOnCreditsSpans_OpenAndClosedOnDropOrReset(t *testing.T) {
	for name, tc := range map[string]struct {
		samples domain.WindowSamples
		want    []domain.OnCreditsSpan
	}{
		"no samples": {},
		"below the limit": {
			samples: domain.WindowSamples{SevenDay: []domain.Sample{sample(0, 97), sample(60, 99.9)}},
		},
		"open from the first sample at the limit": {
			samples: domain.WindowSamples{SevenDay: []domain.Sample{sample(0, 99), sample(60, 100), sample(120, 100)}},
			want:    []domain.OnCreditsSpan{span(60, -1)},
		},
		"over the limit counts": {
			samples: domain.WindowSamples{FiveHour: []domain.Sample{sample(60, 101.5)}},
			want:    []domain.OnCreditsSpan{span(60, -1)},
		},
		"closed by the first sample below the limit": {
			samples: domain.WindowSamples{SevenDay: []domain.Sample{sample(0, 100), sample(60, 100), sample(120, 40), sample(180, 45)}},
			want:    []domain.OnCreditsSpan{span(0, 120)},
		},
		"closed at the reset that came before the next sample": {
			samples: domain.WindowSamples{FiveHour: []domain.Sample{full(0, 300), sample(600, 3)}},
			want:    []domain.OnCreditsSpan{span(0, 300)},
		},
		"closed at a reset that has passed with no sample since": {
			samples: domain.WindowSamples{FiveHour: []domain.Sample{full(0, 300)}},
			want:    []domain.OnCreditsSpan{span(0, 300)},
		},
		"open while the reset is still ahead": {
			samples: domain.WindowSamples{SevenDay: []domain.Sample{full(0, 20_000)}},
			want:    []domain.OnCreditsSpan{span(0, -1)},
		},
		"the newest reset time a sample reports": {
			samples: domain.WindowSamples{FiveHour: []domain.Sample{
				full(0, 300), sample(60, 100), full(120, 900),
			}},
			want: []domain.OnCreditsSpan{span(0, 900)},
		},
		"a new window at the limit after a reset": {
			samples: domain.WindowSamples{FiveHour: []domain.Sample{
				full(0, 300), full(400, 20_000),
			}},
			want: []domain.OnCreditsSpan{span(0, 300), span(400, -1)},
		},
		"a reading from a window that had already reset": {
			samples: domain.WindowSamples{SevenDay: []domain.Sample{full(500, 300)}},
		},
		"two stretches at the limit": {
			samples: domain.WindowSamples{SevenDay: []domain.Sample{sample(0, 100), sample(100, 50), sample(200, 100)}},
			want:    []domain.OnCreditsSpan{span(0, 100), span(200, -1)},
		},
		"overlapping windows merge": {
			samples: domain.WindowSamples{
				FiveHour: []domain.Sample{sample(0, 100), sample(600, 30)},
				SevenDay: []domain.Sample{sample(300, 100), sample(900, 99)},
			},
			want: []domain.OnCreditsSpan{span(0, 900)},
		},
		"a window that meets another's end merges with it": {
			samples: domain.WindowSamples{
				FiveHour: []domain.Sample{sample(0, 100), sample(600, 30)},
				SevenDay: []domain.Sample{sample(600, 100)},
			},
			want: []domain.OnCreditsSpan{span(0, -1)},
		},
		"one window's span inside the other's": {
			samples: domain.WindowSamples{
				FiveHour: []domain.Sample{sample(100, 100), sample(200, 30)},
				SevenDay: []domain.Sample{sample(0, 100), sample(900, 20)},
			},
			want: []domain.OnCreditsSpan{span(0, 900)},
		},
		"windows apart stay apart, in time order": {
			samples: domain.WindowSamples{
				FiveHour: []domain.Sample{sample(1000, 100), sample(1200, 30)},
				SevenDay: []domain.Sample{sample(0, 100), sample(500, 20)},
			},
			want: []domain.OnCreditsSpan{span(0, 500), span(1000, 1200)},
		},
		"samples in any order": {
			samples: domain.WindowSamples{SevenDay: []domain.Sample{sample(120, 40), sample(0, 100), sample(60, 100)}},
			want:    []domain.OnCreditsSpan{span(0, 120)},
		},
		"a window that resets into a new one at the limit stays one span": {
			samples: domain.WindowSamples{FiveHour: []domain.Sample{full(0, 300), full(300, 20_000)}},
			want:    []domain.OnCreditsSpan{span(0, -1)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			before := slices.Clone(tc.samples.SevenDay)

			got := domain.OnCreditsSpans(tc.samples, creditsNow)

			if len(got) != 0 || len(tc.want) != 0 {
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("OnCreditsSpans() = %v, want %v", got, tc.want)
				}
			}
			if !slices.Equal(tc.samples.SevenDay, before) {
				t.Error("OnCreditsSpans changed the caller's samples")
			}
		})
	}
}

// Samples already in time order, as the tracker keeps them, are read where
// they lie: every view works out the spans of up to a month of samples, and
// copying them each time would make the month's garbage over again.
func TestOnCreditsSpans_ReadsSamplesInTimeOrderWithoutCopyingThem(t *testing.T) {
	samples := domain.WindowSamples{
		FiveHour: []domain.Sample{sample(0, 40), sample(60, 70), sample(120, 99)},
		SevenDay: []domain.Sample{sample(0, 20), sample(60, 21), sample(120, 22)},
	}

	allocs := testing.AllocsPerRun(10, func() { domain.OnCreditsSpans(samples, creditsNow) })

	if allocs != 0 {
		t.Errorf("OnCreditsSpans() over samples in time order, none at the limit, made %v allocations, want none", allocs)
	}
}

// A window's spans are kept in room made once, whatever their number: a slice
// grown span by span leaves several times their size behind, and a month of
// short stretches on credits holds tens of thousands of spans.
func TestOnCreditsSpans_AllocatesAsOftenForManySpansAsForFew(t *testing.T) {
	allocsFor := func(spanCount int) float64 {
		var samples []domain.Sample
		for i := range spanCount {
			samples = append(samples, sample(60*i, 100), sample(60*i+30, 0))
		}
		windows := domain.WindowSamples{SevenDay: samples}
		return testing.AllocsPerRun(5, func() { domain.OnCreditsSpans(windows, creditsNow) })
	}

	if few, many := allocsFor(10), allocsFor(10_000); many != few {
		t.Errorf("OnCreditsSpans() made %v allocations over 10 spans and %v over 10,000, want as many", few, many)
	}
}

// seeded returns a random source that gives the same numbers on every run, so
// a failure reproduces.
func seeded(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, 2026)) //nolint:gosec // G404: a fixed seed; nothing secret
}

// randomSamples returns up to 13 samples in time order, at or around the
// limit, each with one of resets (seconds after at(0), 0 for an unknown reset)
// as its window's reset time, and the second the last one was taken at.
func randomSamples(rng *rand.Rand, resets []int) ([]domain.Sample, int) {
	values := []float64{40, 99.5, 100, 103}
	var samples []domain.Sample
	sec := 0
	for range rng.IntN(14) {
		sec += 100 + rng.IntN(900)
		s := sample(sec, values[rng.IntN(len(values))])
		if r := resets[rng.IntN(len(resets))]; r > 0 {
			s.ResetsAt = at(r)
		}
		samples = append(samples, s)
	}
	return samples, sec
}

// joined is spans in time order, those that overlap or meet joined into one,
// worked out the simple way: sorted, then each laid against the one before.
func joined(spans []domain.OnCreditsSpan) []domain.OnCreditsSpan {
	sorted := slices.SortedFunc(slices.Values(spans), func(a, b domain.OnCreditsSpan) int { return a.From.Compare(b.From) })
	var out []domain.OnCreditsSpan
	for _, s := range sorted {
		n := len(out)
		if n == 0 || (!out[n-1].To.IsZero() && out[n-1].To.Before(s.From)) {
			out = append(out, s)
			continue
		}
		if out[n-1].To.IsZero() || s.To.IsZero() {
			out[n-1].To = time.Time{}
		} else if s.To.After(out[n-1].To) {
			out[n-1].To = s.To
		}
	}
	return out
}

// The spans of both windows are the spans of each, joined where they overlap
// or meet, whatever order the samples come in.
func TestOnCreditsSpans_BothWindowsAreEachWindowsSpansJoined(t *testing.T) {
	rng := seeded(77)
	for range 5000 {
		fiveHour, fiveHourLast := randomSamples(rng, []int{0, 3_000, 6_000})
		sevenDay, sevenDayLast := randomSamples(rng, []int{0, 5_000, 12_000})
		now := at(rng.IntN(max(fiveHourLast, sevenDayLast) + 2_000))
		if rng.IntN(2) == 0 {
			rng.Shuffle(len(sevenDay), func(i, j int) { sevenDay[i], sevenDay[j] = sevenDay[j], sevenDay[i] })
		}

		got := domain.OnCreditsSpans(domain.WindowSamples{FiveHour: fiveHour, SevenDay: sevenDay}, now)

		want := joined(slices.Concat(
			domain.OnCreditsSpans(domain.WindowSamples{FiveHour: fiveHour}, now),
			domain.OnCreditsSpans(domain.WindowSamples{SevenDay: sevenDay}, now)))
		if len(got) != 0 || len(want) != 0 {
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("five-hour %v\nseven-day %v\nnow %v\nOnCreditsSpans() = %v, want %v", fiveHour, sevenDay, now, got, want)
			}
		}
	}
}

// OnCreditsMarks keeps only the samples where a run at the limit starts or
// ends, or changes its reset time, and the spans of what it keeps are the
// spans of all the samples, whatever samples come after.
func TestOnCreditsMarks_KeepsEverySpan(t *testing.T) {
	rng := seeded(22)
	for range 3000 {
		samples, _ := randomSamples(rng, []int{0, 2_000, 4_000})
		cut := rng.IntN(len(samples) + 1)
		older, recent := samples[:cut], samples[cut:]

		marks := domain.OnCreditsMarks(older)

		all := domain.OnCreditsSpans(domain.WindowSamples{SevenDay: samples}, creditsNow)
		kept := domain.OnCreditsSpans(domain.WindowSamples{SevenDay: slices.Concat(marks, recent)}, creditsNow)
		if !reflect.DeepEqual(kept, all) {
			t.Fatalf("samples %v\nmarks of the first %d %v\nspans %v, want %v", samples, cut, marks, kept, all)
		}
		if again := domain.OnCreditsMarks(marks); !slices.Equal(again, marks) {
			t.Fatalf("marks of the marks %v, want %v", again, marks)
		}
	}
}

// runningAt is spans without those that ended by oldest.
func runningAt(spans []domain.OnCreditsSpan, oldest time.Time) []domain.OnCreditsSpan {
	var running []domain.OnCreditsSpan
	for _, s := range spans {
		if s.To.IsZero() || s.To.After(oldest) {
			running = append(running, s)
		}
	}
	return running
}

// forgetSpans returns the spans of samples once those before oldest are
// forgotten.
func forgetSpans(samples []domain.Sample, oldest time.Time) []domain.OnCreditsSpan {
	return domain.OnCreditsSpans(domain.WindowSamples{SevenDay: domain.ForgetOnCreditsBefore(samples, oldest)}, creditsNow)
}

// ForgetOnCreditsBefore forgets the samples before a time, but never a span
// that runs on past it: the spans from that time on stay as they were, the
// start of one that runs across it included.
func TestForgetOnCreditsBefore_KeepsTheSpansFromThatTimeOn(t *testing.T) {
	rng := seeded(55)
	for range 3000 {
		samples, last := randomSamples(rng, []int{0, 2_000, 4_000, 9_000})
		oldest := at(rng.IntN(last + 2))

		kept := domain.ForgetOnCreditsBefore(samples, oldest)

		all := domain.OnCreditsSpans(domain.WindowSamples{SevenDay: samples}, creditsNow)
		left := domain.OnCreditsSpans(domain.WindowSamples{SevenDay: kept}, creditsNow)
		if !reflect.DeepEqual(runningAt(left, oldest), runningAt(all, oldest)) {
			t.Fatalf("samples %v\nkept from %v: %v\nspans %v, want %v", samples, oldest, kept, runningAt(left, oldest), runningAt(all, oldest))
		}
		for i, s := range kept {
			if s.At.Before(oldest) && i > 0 {
				t.Fatalf("kept %v from %v: only the first sample may come before it", kept, oldest)
			}
		}
	}
}

// Forgetting before one time and then before a later one leaves the spans
// that forgetting before the later one does. The history forgets at its
// retention and the tracker at its credits horizon, one after the other, and
// a restarted tracker must hold the spans one that ran on holds.
func TestForgetOnCreditsBefore_ForgettingTwiceIsForgettingOnce(t *testing.T) {
	rng := seeded(57)
	for range 3000 {
		samples, last := randomSamples(rng, []int{0, 2_000, 4_000, 9_000})
		first := rng.IntN(last + 2)
		then := first + rng.IntN(last+2-first+1)

		twice := forgetSpans(domain.ForgetOnCreditsBefore(samples, at(first)), at(then))

		if once := forgetSpans(samples, at(then)); !reflect.DeepEqual(twice, once) {
			t.Fatalf("samples %v\nforgotten before %v and then %v: spans %v, want %v", samples, at(first), at(then), twice, once)
		}
	}
}

func TestForgetOnCreditsBefore_ASpanAcrossTheCut(t *testing.T) {
	for name, tc := range map[string]struct {
		samples []domain.Sample
		want    []domain.OnCreditsSpan
	}{
		// A span keeps its own start, which dates going on credits, even
		// once a later sample has closed it: dated at the cut instead, it
		// would move with every cut, and so would the start of an open span
		// it merges into. The calls before the cut are forgotten all the
		// same, so what the span counts does not change.
		"closed since: it keeps its start": {
			samples: []domain.Sample{full(0, 7000), sample(500, 100), sample(8000, 40)},
			want:    []domain.OnCreditsSpan{span(0, 7000)},
		},
		"still open with no known reset: it keeps its start": {
			samples: []domain.Sample{sample(0, 100), sample(2000, 100)},
			want:    []domain.OnCreditsSpan{span(0, -1)},
		},
		"the reset learned before the cut still closes it": {
			samples: []domain.Sample{sample(0, 100), full(500, 3000), sample(4000, 30)},
			want:    []domain.OnCreditsSpan{span(0, 3000)},
		},
		"ended before the cut: forgotten": {
			samples: []domain.Sample{full(0, 600), sample(2000, 30)},
		},
		"closed by a sample before the cut: forgotten": {
			samples: []domain.Sample{sample(0, 100), sample(800, 30), sample(2000, 40)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := domain.OnCreditsSpans(domain.WindowSamples{SevenDay: domain.ForgetOnCreditsBefore(tc.samples, at(1000))}, creditsNow)

			if len(got) != 0 || len(tc.want) != 0 {
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("spans %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// Samples that are all marks already, in time order, are returned as they
// are. Past the projections' horizon the tracker keeps only marks, and every
// view prunes its samples, so a view finds them as they were and copies
// nothing.
func TestOnCreditsMarks_ReturnsSamplesThatAreAllMarksWithoutCopyingThem(t *testing.T) {
	marks := []domain.Sample{sample(120, 100), full(300, 900), sample(420, 20), sample(480, 100), sample(540, 99)}

	got := domain.OnCreditsMarks(marks)
	allocs := testing.AllocsPerRun(10, func() { domain.OnCreditsMarks(marks) })

	if !slices.Equal(got, marks) {
		t.Errorf("OnCreditsMarks() = %v, want the marks as they are: %v", got, marks)
	}
	if allocs != 0 {
		t.Errorf("OnCreditsMarks() of samples that are all marks made %v allocations, want none", allocs)
	}
}

// Forgetting copies none of the samples it keeps when no span runs across the
// cut: each view forgets what has aged out of the credit history.
func TestForgetOnCreditsBefore_KeepsTheRestWithoutCopyingThem(t *testing.T) {
	for name, tc := range map[string]struct {
		samples []domain.Sample
		want    []domain.Sample
	}{
		"nothing before the cut": {
			samples: []domain.Sample{sample(1000, 100), sample(1500, 40)},
			want:    []domain.Sample{sample(1000, 100), sample(1500, 40)},
		},
		"a span that ended before the cut": {
			samples: []domain.Sample{sample(0, 100), sample(500, 30), sample(1200, 100), sample(1500, 40)},
			want:    []domain.Sample{sample(1200, 100), sample(1500, 40)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := domain.ForgetOnCreditsBefore(tc.samples, at(1000))
			allocs := testing.AllocsPerRun(10, func() { domain.ForgetOnCreditsBefore(tc.samples, at(1000)) })

			if !slices.Equal(got, tc.want) {
				t.Errorf("ForgetOnCreditsBefore() = %v, want %v", got, tc.want)
			}
			if allocs != 0 {
				t.Errorf("ForgetOnCreditsBefore() made %v allocations, want none", allocs)
			}
		})
	}
}

func TestOnCreditsMarks_DropsSteadyStretches(t *testing.T) {
	samples := []domain.Sample{
		sample(0, 40), sample(60, 70), sample(120, 100), sample(180, 100), sample(240, 100),
		full(300, 900), full(360, 900), sample(420, 20), sample(480, 30),
	}

	got := domain.OnCreditsMarks(samples)

	want := []domain.Sample{sample(120, 100), full(300, 900), sample(420, 20)}
	if !slices.Equal(got, want) {
		t.Errorf("OnCreditsMarks() = %v, want %v", got, want)
	}
}

func grant(usd float64, sec int) domain.CreditGrant {
	return domain.CreditGrant{USD: usd, AddedAt: at(sec)}
}

func callAt(sec int, costUSD float64) domain.APIRequest {
	return domain.APIRequest{At: at(sec), CostUSD: costUSD}
}

func TestEstimateCredits_SumsOnlyInsideSpansAfterGrant(t *testing.T) {
	spans := []domain.OnCreditsSpan{span(0, 1000)}
	requests := []domain.APIRequest{
		callAt(100, 1), // inside the span, before the grant
		callAt(600, 2), // inside the span, after the grant
		callAt(1500, 3),
	}

	got := domain.EstimateCredits([]domain.CreditGrant{grant(20, 500)}, spans, requests, creditsNow)

	want := domain.CreditEstimate{BalanceUSD: 20, SpentUSD: 2, RemainingUSD: 18, Source: domain.CreditSourceEstimate}
	if got != want {
		t.Errorf("EstimateCredits() = %+v, want %+v", got, want)
	}
}

func TestEstimateCredits_LaterGrantsOnlyAddToTheBalance(t *testing.T) {
	spans := []domain.OnCreditsSpan{span(0, 1000), span(2000, 3000)}
	requests := []domain.APIRequest{callAt(100, 0.5), callAt(600, 2), callAt(2500, 4)}

	got := domain.EstimateCredits([]domain.CreditGrant{grant(10, 2200), grant(20, 500)}, spans, requests, creditsNow)

	if got.BalanceUSD != 30 || got.SpentUSD != 6 || got.RemainingUSD != 24 {
		t.Errorf("EstimateCredits() = %+v, want $6 of $30 spent: the spend since the earliest grant", got)
	}
}

func TestEstimateCredits_SpanEdges(t *testing.T) {
	spans := []domain.OnCreditsSpan{span(100, 200)}
	requests := []domain.APIRequest{callAt(99, 1), callAt(100, 2), callAt(199, 4), callAt(200, 8)}

	got := domain.EstimateCredits([]domain.CreditGrant{grant(20, 0)}, spans, requests, creditsNow)

	if got.SpentUSD != 6 {
		t.Errorf("spent %v, want 6: a span takes in its start and leaves out its end", got.SpentUSD)
	}
}

// A span ends just before its To, where the next may begin.
func TestSpanCursor_FindsTheSpanThatHoldsEachTime(t *testing.T) {
	cursor := domain.NewSpanCursor([]domain.OnCreditsSpan{span(0, 10), span(10, 20), span(30, -1)})

	for _, tc := range []struct{ sec, want int }{
		{-1, -1}, {0, 0}, {9, 0}, {10, 1}, {19, 1}, {20, -1}, {29, -1}, {30, 2}, {10_000, 2},
	} {
		got, held := cursor.Find(at(tc.sec))
		if got != tc.want || held != (tc.want >= 0) {
			t.Errorf("Find(%d s) = %d, %v; want %d", tc.sec, got, held, tc.want)
		}
	}
}

func TestEstimateCredits_CountsACallMadeAsTheGrantIsAdded(t *testing.T) {
	spans := []domain.OnCreditsSpan{span(0, -1)}
	requests := []domain.APIRequest{callAt(499, 1), callAt(500, 2), callAt(501, 4)}

	got := domain.EstimateCredits([]domain.CreditGrant{grant(20, 500)}, spans, requests, creditsNow)

	if got.SpentUSD != 6 {
		t.Errorf("spent %v, want 6: a call made at or after the earliest grant is paid from it", got.SpentUSD)
	}
}

func TestEstimateCredits_ActiveNeedsRequestInsideOpenSpan(t *testing.T) {
	grants := []domain.CreditGrant{grant(20, 0)}
	for name, tc := range map[string]struct {
		spans    []domain.OnCreditsSpan
		requests []domain.APIRequest
		since    time.Time
	}{
		"no span": {
			requests: []domain.APIRequest{callAt(100, 1)},
		},
		"an open span with no request in it": {
			spans:    []domain.OnCreditsSpan{span(500, -1)},
			requests: []domain.APIRequest{callAt(100, 1)},
		},
		"a request in a span that has closed": {
			spans:    []domain.OnCreditsSpan{span(0, 400), span(500, -1)},
			requests: []domain.APIRequest{callAt(100, 1)},
		},
		"a request in the open span": {
			spans:    []domain.OnCreditsSpan{span(0, 400), span(500, -1)},
			requests: []domain.APIRequest{callAt(100, 1), callAt(700, 1)},
			since:    at(500),
		},
		"a request that cost nothing still shows credits are on": {
			spans:    []domain.OnCreditsSpan{span(500, -1)},
			requests: []domain.APIRequest{callAt(700, 0)},
			since:    at(500),
		},
		"a span that ends after now is open": {
			spans:    []domain.OnCreditsSpan{span(500, 20_000)},
			requests: []domain.APIRequest{callAt(700, 1)},
			since:    at(500),
		},
		"a span that ended at now is closed": {
			spans:    []domain.OnCreditsSpan{span(500, 10_000)},
			requests: []domain.APIRequest{callAt(700, 1)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := domain.EstimateCredits(grants, tc.spans, tc.requests, creditsNow)

			if got.Active != !tc.since.IsZero() || !got.OnCreditsSince.Equal(tc.since) {
				t.Errorf("active %v since %v, want active %v since %v", got.Active, got.OnCreditsSince, !tc.since.IsZero(), tc.since)
			}
		})
	}
}

func TestEstimateCredits_WithoutGrantsCountsNoSpend(t *testing.T) {
	got := domain.EstimateCredits(nil, []domain.OnCreditsSpan{span(0, -1)}, []domain.APIRequest{callAt(100, 3)}, creditsNow)

	want := domain.CreditEstimate{OnCreditsSince: at(0), Active: true, Source: domain.CreditSourceEstimate}
	if got != want {
		t.Errorf("EstimateCredits() = %+v, want %+v: on credits, with nothing recorded to spend from", got, want)
	}
}

func TestEstimateCredits_RemainingNeverBelowZero(t *testing.T) {
	got := domain.EstimateCredits([]domain.CreditGrant{grant(5, 0)}, []domain.OnCreditsSpan{span(0, -1)},
		[]domain.APIRequest{callAt(100, 4), callAt(200, 3)}, creditsNow)

	if got.SpentUSD != 7 || got.RemainingUSD != 0 {
		t.Errorf("spent %v, remaining %v; want 7 spent and none left", got.SpentUSD, got.RemainingUSD)
	}
}

func TestEstimateCredits_FiguresStayFinite(t *testing.T) {
	grants := []domain.CreditGrant{grant(math.MaxFloat64, 0), grant(math.MaxFloat64, 0), grant(-5, 0), grant(math.NaN(), 0)}
	requests := []domain.APIRequest{
		callAt(100, math.MaxFloat64), callAt(200, math.MaxFloat64),
		callAt(300, -2), callAt(400, math.NaN()), callAt(500, math.Inf(1)),
	}

	got := domain.EstimateCredits(grants, []domain.OnCreditsSpan{span(0, -1)}, requests, creditsNow)

	for name, v := range map[string]float64{"balance": got.BalanceUSD, "spent": got.SpentUSD, "remaining": got.RemainingUSD} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			t.Errorf("%s = %v, want a finite amount, not below zero", name, v)
		}
	}
	if got.BalanceUSD != math.MaxFloat64 || got.SpentUSD != math.MaxFloat64 {
		t.Errorf("balance %v and spent %v, want both to stop at the largest finite amount", got.BalanceUSD, got.SpentUSD)
	}
}

func TestEstimateCredits_ReportedOverridesEstimate(t *testing.T) {
	estimate := domain.EstimateCredits([]domain.CreditGrant{grant(20, 0)}, []domain.OnCreditsSpan{span(500, -1)},
		[]domain.APIRequest{callAt(700, 6.4)}, creditsNow)

	got := estimate.WithReported(domain.SpendLimit{UsedUSD: 314.12, LimitUSD: 500})

	want := domain.CreditEstimate{
		BalanceUSD: 500, SpentUSD: 314.12, RemainingUSD: 500 - 314.12,
		OnCreditsSince: at(500), Active: true, Source: domain.CreditSourceReported,
	}
	if got != want {
		t.Errorf("WithReported() = %+v, want %+v: the reported figures, and the usage that shows credits are on", got, want)
	}
	over := estimate.WithReported(domain.SpendLimit{UsedUSD: 520, LimitUSD: 500})
	if over.RemainingUSD != 0 {
		t.Errorf("remaining %v past the limit, want 0", over.RemainingUSD)
	}
}

// Many short stretches on credits, a call every 5 s through them: the
// estimate walks the calls and the spans together, where looking through the
// spans for each call would take minutes over a month of them. Its cost is
// held to one that grows linearly with the spans, which costtest checks on
// any machine; BenchmarkEstimateCredits_ManySpansAndCalls measures fifty
// thousand.
func TestEstimateCredits_OnePassOverManySpansAndCalls(t *testing.T) {
	grants := []domain.CreditGrant{grant(1e9, -1)}
	// Four thousand spans against sixteen thousand show a quadratic cost
	// plainly, in seconds where fifty thousand would take minutes. The race
	// detector slows the estimate several times over, so under it a
	// thousand against four thousand.
	spanCount := 4000
	if raceEnabled {
		spanCount = 1000
	}
	estimate := func(spanCount int) func() {
		spans, requests, now := shortStretches(spanCount)
		// Each closed span holds the calls at its start and 5 s in; the open
		// one holds the four from its start on.
		got := domain.EstimateCredits(grants, spans, requests, now)
		opened := at(20 * (spanCount - 1))
		if want := float64(2*(spanCount-1) + 4); got.SpentUSD != want || !got.Active || !got.OnCreditsSince.Equal(opened) {
			t.Errorf("EstimateCredits() over %d spans = %+v, want $%v spent, on credits since %v", spanCount, got, want, opened)
		}
		return func() { domain.EstimateCredits(grants, spans, requests, now) }
	}

	costtest.AssertLinear(t, spanCount, "spans", estimate)
	// Fifty thousand spans, untimed, for the outcome at full scale. A cost
	// found to grow faster than linearly would take minutes over them, so
	// they are estimated only once it is known to be linear.
	if !t.Failed() {
		estimate(50_000)
	}
}

// BenchmarkEstimateCredits_ManySpansAndCalls measures an estimate over fifty
// thousand short stretches on credits, as in
// TestEstimateCredits_OnePassOverManySpansAndCalls, and two hundred thousand
// calls through them.
func BenchmarkEstimateCredits_ManySpansAndCalls(b *testing.B) {
	grants := []domain.CreditGrant{grant(1e9, -1)}
	spans, requests, now := shortStretches(50_000)
	b.ReportAllocs()

	for b.Loop() {
		domain.EstimateCredits(grants, spans, requests, now)
	}
}

// shortStretches returns spanCount stretches on credits 10 s long, one every
// 20 s from t0, the last still open; a call every 5 s through them; and a
// time 20 s after the last opened.
func shortStretches(spanCount int) (spans []domain.OnCreditsSpan, requests []domain.APIRequest, now time.Time) {
	spans = make([]domain.OnCreditsSpan, spanCount)
	for i := range spans {
		spans[i] = span(20*i, 20*i+10)
	}
	last := 20 * (spanCount - 1)
	spans[spanCount-1] = span(last, -1)
	for sec := 0; sec <= last+15; sec += 5 {
		requests = append(requests, callAt(sec, 1))
	}
	return spans, requests, at(last + 20)
}

// quadraticEstimate is EstimateCredits worked out the simple way, looking
// through every span for each request, in the order given.
func quadraticEstimate(grants []domain.CreditGrant, spans []domain.OnCreditsSpan, requests []domain.APIRequest, now time.Time) domain.CreditEstimate {
	spendable := func(usd float64) bool { return usd > 0 && !math.IsInf(usd, 1) }
	add := func(a, b float64) float64 {
		if sum := a + b; !math.IsInf(sum, 1) {
			return sum
		}
		return math.MaxFloat64
	}
	estimate := domain.CreditEstimate{Source: domain.CreditSourceEstimate}
	var earliest time.Time
	granted := false
	for _, g := range grants {
		if !spendable(g.USD) {
			continue
		}
		estimate.BalanceUSD = add(estimate.BalanceUSD, g.USD)
		if !granted || g.AddedAt.Before(earliest) {
			earliest, granted = g.AddedAt, true
		}
	}
	current := slices.IndexFunc(spans, func(s domain.OnCreditsSpan) bool { return s.To.IsZero() || s.To.After(now) })
	for _, r := range requests {
		inside := slices.IndexFunc(spans, func(s domain.OnCreditsSpan) bool { return s.Holds(r.At) })
		if inside < 0 {
			continue
		}
		if inside == current {
			estimate.Active, estimate.OnCreditsSince = true, spans[current].From
		}
		if granted && !r.At.Before(earliest) && spendable(r.CostUSD) {
			estimate.SpentUSD = add(estimate.SpentUSD, r.CostUSD)
		}
	}
	estimate.RemainingUSD = max(0, estimate.BalanceUSD-estimate.SpentUSD)
	return estimate
}

// The one-pass estimate agrees with the simple one, whatever the spans, the
// grants and the calls, in time order or not. The costs add up exactly in any
// order, so the sums agree to the last bit.
func TestEstimateCredits_AgreesWithLookingThroughEverySpan(t *testing.T) {
	rng := seeded(241)
	costs := []float64{0, 0.25, 1.5, 3.75, -1, math.NaN(), math.Inf(1), math.MaxFloat64}
	for range 5000 {
		fiveHour, fiveHourLast := randomSamples(rng, []int{0, 3_000, 6_000})
		sevenDay, sevenDayLast := randomSamples(rng, []int{0, 5_000, 12_000})
		end := max(fiveHourLast, sevenDayLast) + 2_000
		now := at(rng.IntN(end))
		spans := domain.OnCreditsSpans(domain.WindowSamples{FiveHour: fiveHour, SevenDay: sevenDay}, now)
		var grants []domain.CreditGrant
		for range rng.IntN(3) {
			grants = append(grants, grant(costs[rng.IntN(len(costs))], rng.IntN(end)))
		}
		requests := make([]domain.APIRequest, rng.IntN(40))
		for i := range requests {
			requests[i] = callAt(rng.IntN(end), costs[rng.IntN(len(costs))])
		}
		if rng.IntN(2) == 0 {
			slices.SortStableFunc(requests, func(a, b domain.APIRequest) int { return a.At.Compare(b.At) })
		}

		got := domain.EstimateCredits(grants, spans, requests, now)

		want := quadraticEstimate(grants, spans, requests, now)
		if got.BalanceUSD != want.BalanceUSD || got.SpentUSD != want.SpentUSD || got.RemainingUSD != want.RemainingUSD ||
			got.Active != want.Active || !got.OnCreditsSince.Equal(want.OnCreditsSince) || got.Source != want.Source {
			t.Fatalf("spans %v\ngrants %v\nrequests %v\nnow %v\nEstimateCredits() = %+v, want %+v", spans, grants, requests, now, got, want)
		}
	}
}
