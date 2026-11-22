package tracker

import (
	"slices"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

// reportedFor is how long a reported spend limit stands without a newer
// status line reporting it again.
const reportedFor = time.Hour

// creditHistory is how far back the tracker holds what credit estimates read.
func (t *Tracker) creditHistory() time.Duration {
	return time.Duration(t.cfg.CreditDays) * 24 * time.Hour
}

// creditCall is a model call as a credit estimate reads it.
type creditCall struct {
	at      time.Time
	costUSD float64
}

// accountCredits is what an account's credit estimate reads beyond its
// samples.
type accountCredits struct {
	// calls are the account's model calls: every recent one, since a status
	// line that arrives late can still show it was made at a limit, and
	// older ones only while a span on credits holds them.
	calls []creditCall
	// reported is the spend limit of the newest status line that reported
	// one, at reportedAt.
	reported   *domain.SpendLimit
	reportedAt time.Time
}

func (c *accountCredits) recordCall(at time.Time, costUSD float64) {
	c.calls = append(c.calls, creditCall{at: at, costUSD: costUSD})
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

// reportedAsOf returns the reported spend limit while it still stands at now.
func (c *accountCredits) reportedAsOf(now time.Time) (domain.SpendLimit, bool) {
	if c.reported == nil || !now.Before(c.reportedAt.Add(reportedFor)) {
		return domain.SpendLimit{}, false
	}
	return *c.reported, true
}

// prune forgets the calls made before oldest, and those made before horizon
// that no span holds.
func (c *accountCredits) prune(spans []domain.OnCreditsSpan, horizon, oldest time.Time) {
	c.calls = slices.DeleteFunc(c.calls, func(call creditCall) bool {
		if call.at.Before(oldest) {
			return true
		}
		return call.at.Before(horizon) && !inSpan(spans, call.at)
	})
}

// shift moves the times of the calls and the report by d.
func (c *accountCredits) shift(d time.Duration) {
	for i := range c.calls {
		c.calls[i].at = c.calls[i].at.Add(d)
	}
	c.reportedAt = shifted(c.reportedAt, d)
}

// creditEstimate returns the account's credits as of now, and whether the
// view shows them: only when the owner recorded grants or a status line
// reported a spend limit that still stands. The estimate still says whether
// the account is on credits when neither holds, which alerts all the same.
// Only a subscription has usage credits, so the caller asks for no other's.
func (a *accountEntry) creditEstimate(grants []domain.CreditGrant, now time.Time) (domain.CreditEstimate, bool) {
	spans := domain.OnCreditsSpans(a.samples, now)
	estimate := domain.EstimateCredits(grants, spans, a.credits.callsIn(spans), now)
	reported, standing := a.credits.reportedAsOf(now)
	if standing {
		estimate = estimate.WithReported(reported)
	}
	return estimate, len(grants) > 0 || standing
}

// callsIn returns the calls that spans hold, which are all a credit estimate
// counts, as the requests it reads.
func (c *accountCredits) callsIn(spans []domain.OnCreditsSpan) []domain.APIRequest {
	var requests []domain.APIRequest
	for _, call := range c.calls {
		if inSpan(spans, call.at) {
			requests = append(requests, domain.APIRequest{At: call.at, CostUSD: call.costUSD})
		}
	}
	return requests
}

func inSpan(spans []domain.OnCreditsSpan, at time.Time) bool {
	return slices.ContainsFunc(spans, func(s domain.OnCreditsSpan) bool { return s.Holds(at) })
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
