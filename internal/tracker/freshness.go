package tracker

import "time"

// responseClock follows when a session's newest API response came. That
// dates the rate limits its status line carries, which Claude Code reports
// from the session's own last response: a session that sits idle goes on
// reporting what it last heard.
//
// The newest response is the latest of the session's calls and of the status
// lines on which its API time grew, since such a line came with the response
// that grew it. The line's API time is the signal rather than its cost: it
// grows by a whole number of milliseconds with every response, whatever the
// call cost, where the cost stays put for a call priced at nothing. Until
// either is seen, and so for every session after a restart, as what the
// tracker knows of responses is kept in memory only, no response is known.
type responseClock struct {
	// newest is when the newest response known came, zero while none is.
	newest time.Time
	// apiDurationMS is the API time the session's newest line, observed at
	// lineAt, reported.
	apiDurationMS int64
	lineAt        time.Time
}

// call notes a call the session made at at.
func (c *responseClock) call(at time.Time) {
	if at.After(c.newest) {
		c.newest = at
	}
}

// line notes a status line of the session, observed at at, that reports its
// API time as apiDurationMS. A line older than the newest, delivered late,
// says nothing new.
func (c *responseClock) line(apiDurationMS int64, at time.Time) {
	if c.lineAt.IsZero() {
		c.apiDurationMS, c.lineAt = apiDurationMS, at
		return
	}
	if at.Before(c.lineAt) {
		return
	}
	if apiDurationMS > c.apiDurationMS {
		c.call(at)
	}
	c.apiDurationMS, c.lineAt = apiDurationMS, at
}

func (c *responseClock) shift(d time.Duration) {
	c.newest, c.lineAt = shifted(c.newest, d), shifted(c.lineAt, d)
}

// compareFreshness compares two readings by the newest response known of the
// session each came from, zero where none is known. It returns +1 when a is
// the fresher, -1 when b is, and 0 when they are as fresh or neither is
// known. A reading whose response is known is the fresher when the other's
// is not: the tracker learns of every response a session gets while it
// watches it, so a session it knows of none from has had none since the
// tracker first saw its line.
func compareFreshness(a, b time.Time) int {
	switch {
	case a.IsZero() && b.IsZero():
		return 0
	case b.IsZero():
		return 1
	case a.IsZero():
		return -1
	default:
		return a.Compare(b)
	}
}
