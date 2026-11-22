package tracker

import (
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

// responseClock follows when a session's newest API response came, for each
// account the session's calls and status lines came from. That dates the
// rate limits its line carries for that account, which Claude Code reports
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
//
// One session ID can belong to two live processes at once, as Claude Code
// keeps it when a session is resumed while its first terminal is still open,
// on the same account or another. Each process counts its own API time, so a
// line's counts as grown only past the most any line of the session has
// reported. A call does not say which process, or account, made it, so it
// waits for the line that shows the response it brought: the session's next
// line on which the API time grew. A session's first line takes the calls
// waiting for it, and so does every line of a session whose lines report no
// API time, as nothing else can place them.
type responseClock struct {
	// newest is, for each account by its config dir, when the newest response
	// known there came. An account with none known is absent.
	newest map[domain.ConfigDir]time.Time
	// unplaced is when the newest call no line has yet placed on an account
	// was made, zero when there is none.
	unplaced time.Time
	// mostAPIDurationMS is the most API time a line of the session has
	// reported, once one has.
	mostAPIDurationMS int64
	lineSeen          bool
}

// call notes a call the session made at at.
func (c *responseClock) call(at time.Time) {
	if at.After(c.unplaced) {
		c.unplaced = at
	}
}

// line notes a status line of the session, from the account in dir and
// observed at at, that reports its API time as apiDurationMS.
func (c *responseClock) line(dir domain.ConfigDir, apiDurationMS int64, at time.Time) {
	first := !c.lineSeen
	grew := !first && apiDurationMS > c.mostAPIDurationMS
	if first || grew {
		c.mostAPIDurationMS, c.lineSeen = apiDurationMS, true
	}
	if grew {
		c.date(dir, at)
	}
	if first || grew || c.mostAPIDurationMS == 0 {
		c.date(dir, c.unplaced)
		c.unplaced = time.Time{}
	}
}

// date notes a response that came at at on the account in dir.
func (c *responseClock) date(dir domain.ConfigDir, at time.Time) {
	if !at.After(c.newest[dir]) {
		return
	}
	if c.newest == nil {
		c.newest = make(map[domain.ConfigDir]time.Time)
	}
	c.newest[dir] = at
}

// ended notes that the session has ended. A process that resumes it counts
// its API time from nothing, so the next line starts the count afresh, as a
// first line does. Two processes that are both alive keep the most either
// has reported, as nothing ends between them. Calls still waiting for a line
// are dropped: the process that made them has gone, and the next line may
// come from another one, on another account.
func (c *responseClock) ended() {
	c.mostAPIDurationMS, c.lineSeen = 0, false
	c.unplaced = time.Time{}
}

// freshAs returns when the newest response known for the account in dir
// came, zero when none is known.
func (c *responseClock) freshAs(dir domain.ConfigDir) time.Time {
	return c.newest[dir]
}

func (c *responseClock) shift(d time.Duration) {
	for dir, at := range c.newest {
		c.newest[dir] = at.Add(d)
	}
	c.unplaced = shifted(c.unplaced, d)
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
