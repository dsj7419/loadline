package tracker

import (
	"slices"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

// creditKeep is how far back the tracker holds what credit estimates read:
// the thirty days the view's spend covers.
const creditKeep = keepDays * 24 * time.Hour

// accountCredits is what an account's credit estimate reads beyond its
// samples.
type accountCredits struct {
	// calls are the time and cost of the account's model calls: every recent
	// one, since a status line that arrives late can still show it was made
	// at a limit, and older ones only while a span on credits holds them.
	calls []domain.APIRequest
	// reported is the spend limit of the newest status line that reported
	// one, at reportedAt.
	reported   *domain.SpendLimit
	reportedAt time.Time
}

func (c *accountCredits) recordCall(at time.Time, costUSD float64) {
	c.calls = append(c.calls, domain.APIRequest{At: at, CostUSD: costUSD})
}

// observe takes the spend limit a status line at at reported. A line that
// reports none is no news of it, and one older than the last is stale.
func (c *accountCredits) observe(l *domain.SpendLimit, at time.Time) {
	if l == nil || !at.After(c.reportedAt) {
		return
	}
	reported := *l
	c.reported, c.reportedAt = &reported, at
}

// prune forgets the calls made before oldest, and those made before horizon
// that no span holds.
func (c *accountCredits) prune(spans []domain.OnCreditsSpan, horizon, oldest time.Time) {
	c.calls = slices.DeleteFunc(c.calls, func(r domain.APIRequest) bool {
		if r.At.Before(oldest) {
			return true
		}
		return r.At.Before(horizon) && !slices.ContainsFunc(spans, func(s domain.OnCreditsSpan) bool { return s.Holds(r.At) })
	})
}

// shift moves the times of the calls and the report by d.
func (c *accountCredits) shift(d time.Duration) {
	for i := range c.calls {
		c.calls[i].At = c.calls[i].At.Add(d)
	}
	c.reportedAt = shifted(c.reportedAt, d)
}

// creditEstimate returns the account's credits as of now, and whether the
// view shows them: only when the owner recorded grants or a status line
// reported a spend limit. The estimate still says whether the account is on
// credits when neither did, which alerts all the same.
func (a *accountEntry) creditEstimate(grants []domain.CreditGrant, now time.Time) (domain.CreditEstimate, bool) {
	spans := domain.OnCreditsSpans(a.samples, now)
	estimate := domain.EstimateCredits(grants, spans, a.credits.calls, now)
	if a.credits.reported != nil {
		estimate = estimate.WithReported(*a.credits.reported)
	}
	return estimate, len(grants) > 0 || a.credits.reported != nil
}

// cloneGrants copies the grants, so the tracker never shares a slice its
// caller can change.
func cloneGrants(grants map[domain.ConfigDir][]domain.CreditGrant) map[domain.ConfigDir][]domain.CreditGrant {
	cloned := make(map[domain.ConfigDir][]domain.CreditGrant, len(grants))
	for dir, g := range grants {
		cloned[dir] = slices.Clone(g)
	}
	return cloned
}
