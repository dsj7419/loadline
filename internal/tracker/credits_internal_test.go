package tracker

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

// The calls an estimate reads, and those pruning keeps, are those found by
// looking through every span for each call, whatever order the calls were
// recorded in.
func TestAccountCredits_OnePassAgreesWithLookingThroughEverySpan(t *testing.T) {
	rng := rand.New(rand.NewPCG(243, 2026)) //nolint:gosec // G404: a fixed seed; nothing secret
	base := time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	byTime := func(a, b creditCall) int { return a.at.Compare(b.at) }
	held := func(spans []domain.OnCreditsSpan, call creditCall) bool {
		return slices.ContainsFunc(spans, func(s domain.OnCreditsSpan) bool { return s.Holds(call.at) })
	}
	for range 3000 {
		var spans []domain.OnCreditsSpan
		sec := 0
		for range rng.IntN(8) {
			from := sec + rng.IntN(300)
			sec = from + 1 + rng.IntN(300)
			spans = append(spans, domain.OnCreditsSpan{From: at(from), To: at(sec)})
		}
		if len(spans) > 0 && rng.IntN(2) == 0 {
			spans[len(spans)-1].To = time.Time{}
		}
		end := sec + 300
		var credits accountCredits
		var recorded []creditCall
		for range rng.IntN(40) {
			call := creditCall{at: at(rng.IntN(end)), costUSD: float64(rng.IntN(4))}
			credits.recordCall(call.at, call.costUSD)
			recorded = append(recorded, call)
		}
		slices.SortStableFunc(recorded, byTime)
		if !slices.Equal(credits.calls, recorded) {
			t.Fatalf("calls held %v, want those recorded in time order %v", credits.calls, recorded)
		}

		var want []domain.APIRequest
		for _, call := range recorded {
			if held(spans, call) {
				want = append(want, domain.APIRequest{At: call.at, CostUSD: call.costUSD})
			}
		}
		if got := credits.callsIn(spans); !slices.Equal(got, want) {
			t.Fatalf("spans %v\ncalls %v\ncallsIn() = %v, want %v", spans, recorded, got, want)
		}

		oldest := at(rng.IntN(end))
		horizon := oldest.Add(time.Duration(rng.IntN(end)) * time.Second)
		credits.prune(spans, horizon, oldest)
		kept := slices.DeleteFunc(slices.Clone(recorded), func(call creditCall) bool {
			return call.at.Before(oldest) || (call.at.Before(horizon) && !held(spans, call))
		})
		if !slices.Equal(credits.calls, kept) {
			t.Fatalf("spans %v\ncalls %v\npruned before %v, horizon %v: kept %v, want %v", spans, recorded, oldest, horizon, credits.calls, kept)
		}
	}
}
