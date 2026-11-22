package domain_test

import (
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

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

// OnCreditsMarks keeps only the samples where a run at the limit starts or
// ends, or changes its reset time, and the spans of what it keeps are the
// spans of all the samples, whatever samples come after.
func TestOnCreditsMarks_KeepsEverySpan(t *testing.T) {
	values := []float64{40, 99.5, 100, 103}
	resets := []int{0, 2_000, 4_000}
	rng := rand.New(rand.NewPCG(22, 2026)) //nolint:gosec // G404: a fixed seed, so a failure reproduces; nothing secret
	for range 3000 {
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
