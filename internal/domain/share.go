package domain

import (
	"math"
	"time"
)

// Spend is the cost of one model call, attributed to a session and the
// account it ran under.
type Spend struct {
	At      time.Time
	Session SessionID
	Dir     ConfigDir
	CostUSD float64
}

// Shares returns each session's fraction of its account's spend over the
// window ending at now. Sessions with no spend in the window are left out,
// and so are calls with a cost that is not a positive, finite number.
//
// A session billed to two accounts, because it moved, reports the larger of
// its two shares. Sums of spend stop at the largest finite cost, so a share
// is never NaN or infinite, and an account whose total is zero or not finite
// gives no share.
func Shares(spend []Spend, now time.Time, window time.Duration) map[SessionID]float64 {
	type sessionOnAccount struct {
		dir     ConfigDir
		session SessionID
	}
	oldest := now.Add(-window)
	bySession := make(map[sessionOnAccount]float64)
	byAccount := make(map[ConfigDir]float64)
	for _, s := range spend {
		if s.At.Before(oldest) || !isSpendable(s.CostUSD) {
			continue
		}
		key := sessionOnAccount{s.Dir, s.Session}
		bySession[key] = addSpend(bySession[key], s.CostUSD)
		byAccount[s.Dir] = addSpend(byAccount[s.Dir], s.CostUSD)
	}

	shares := make(map[SessionID]float64, len(bySession))
	for key, cost := range bySession {
		total := byAccount[key.dir]
		if !isSpendable(total) {
			continue
		}
		shares[key.session] = max(shares[key.session], cost/total)
	}
	return shares
}

// isSpendable reports whether a cost can take part in a share: positive and
// finite. NaN fails the comparison.
func isSpendable(costUSD float64) bool {
	return costUSD > 0 && !math.IsInf(costUSD, 1)
}

// addSpend adds costs, stopping at the largest finite cost: a total that
// reached infinity would make the shares of its sessions NaN or zero.
func addSpend(a, b float64) float64 {
	if sum := a + b; !math.IsInf(sum, 1) {
		return sum
	}
	return math.MaxFloat64
}
