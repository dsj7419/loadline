package tracker

import (
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

// responseClock follows when the newest API response of each process of a
// session came, for each account the process's calls and status lines came
// from. That dates the rate limits its line carries for that account, which
// Claude Code reports from the process's own last response: a process that
// sits idle goes on reporting what it last heard.
//
// The newest response is the latest of the calls and of the status lines on
// which the process's API time grew, since such a line came with the response
// that grew it. The line's API time is the signal rather than its cost: it
// grows by a whole number of milliseconds with every response, whatever the
// call cost, where the cost stays put for a call priced at nothing. Until
// either is seen, and so for every session after a restart, as what the
// tracker knows of responses is kept in memory only, no response is known.
//
// One session ID can belong to two live processes at once, as Claude Code
// keeps it when a session is resumed while its first terminal is still open,
// on the same account or another. Each process counts its own API time, and
// one that resumes the session counts on from what the session saved when a
// process last left it, if anything, so a line's API time is judged against
// the most its own process has reported, and a process's first line dates no
// response, whatever it reports. A line names its process by the PID Claude
// Code runs the status line with. Lines that name none, from a Claude Code
// that sets no PID, are taken for one process: their API time counts as grown
// only past the most any of them has reported.
//
// A call does not say which process, or account, made it, so it waits for a
// line that shows the response it brought: the next on which a process's API
// time grew. A process's first line takes the calls waiting too, and so does
// every line of a process whose lines report no API time, as nothing else can
// place them.
type responseClock struct {
	// newest is, for each process on each account, when the newest response
	// known there came. A process with none known on an account is absent.
	newest map[processAccount]time.Time
	// unplaced is when the newest call no line has yet placed on an account
	// was made, zero when there is none.
	unplaced time.Time
	// mostAPIDurationMS is, for each process by its PID, the most API time a
	// line of it has reported since the session last ended. A process with no
	// line seen since is absent.
	mostAPIDurationMS map[int]int64
}

// processAccount is a process of a session, by its PID, zero when unknown,
// on the account in dir.
type processAccount struct {
	pid int
	dir domain.ConfigDir
}

// call notes a call the session made at at.
func (c *responseClock) call(at time.Time) {
	if at.After(c.unplaced) {
		c.unplaced = at
	}
}

// line notes a status line of the session, from the process pid on the
// account in dir and observed at at, that reports the process's API time as
// apiDurationMS.
func (c *responseClock) line(dir domain.ConfigDir, pid int, apiDurationMS int64, at time.Time) {
	most, seen := c.mostAPIDurationMS[pid]
	first, grew := !seen, seen && apiDurationMS > most
	if first || grew {
		if c.mostAPIDurationMS == nil {
			c.mostAPIDurationMS = make(map[int]int64)
		}
		c.mostAPIDurationMS[pid], most = apiDurationMS, apiDurationMS
	}
	process := processAccount{pid: pid, dir: dir}
	if grew {
		c.date(process, at)
	}
	if first || grew || most == 0 {
		c.date(process, c.unplaced)
		c.unplaced = time.Time{}
	}
}

// date notes a response that came at at to process, on its account.
func (c *responseClock) date(process processAccount, at time.Time) {
	if !at.After(c.newest[process]) {
		return
	}
	if c.newest == nil {
		c.newest = make(map[processAccount]time.Time)
	}
	c.newest[process] = at
}

// ended notes that the session has ended. The next line of every process
// starts its count afresh, as a first line does: a process that resumes the
// session counts on from the API time the session saved, or from nothing when
// it saved none, and may take the PID of one that has gone. Nothing says
// which processes outlive the end, so one that does loses its next line's
// news. Calls still waiting for a line are dropped: the process that made
// them has gone, and the next line may come from another one, on another
// account.
func (c *responseClock) ended() {
	clear(c.mostAPIDurationMS)
	c.unplaced = time.Time{}
}

// freshAs returns when the newest response known for the process pid on the
// account in dir came, zero when none is known.
func (c *responseClock) freshAs(dir domain.ConfigDir, pid int) time.Time {
	return c.newest[processAccount{pid: pid, dir: dir}]
}

func (c *responseClock) shift(d time.Duration) {
	for process, at := range c.newest {
		c.newest[process] = at.Add(d)
	}
	c.unplaced = shifted(c.unplaced, d)
}

// compareFreshness compares two readings by the newest response known of the
// process each came from, zero where none is known. It returns +1 when a is
// the fresher, -1 when b is, and 0 when they are as fresh or neither is
// known. A reading whose response is known is the fresher when the other's
// is not: the tracker learns of every response a process gets while it
// watches the process's session, so a process it knows of none from has had
// none since the tracker first saw its line.
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
