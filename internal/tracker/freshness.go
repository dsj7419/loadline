package tracker

import (
	"maps"
	"math"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

// responseClock follows when the newest API response of each process of a
// session came, for each account the process's calls and status lines came
// from. That dates the rate limits its line carries for that account, which
// Claude Code reports from the process's own last response: a process that
// sits idle goes on reporting what it last heard.
//
// A response is dated when its call began. Claude Code takes the rate limits
// from the response's headers, which come as the call begins, but reports
// them on the process's lines only once the call has ended, when the
// process's API time grows by the whole call. So the newest response is the
// latest of the calls' starts, each call's end less its duration, and of the
// starts the status lines show, each line's time less the growth of its
// process's API time, since such a line came with the responses that grew
// it. A line whose API time grew by several calls is dated too early, since
// it reports what the last of them heard: calls made one after another date
// it at the first one's start, and calls made side by side, as subagents'
// are, earlier still, as they grow the API time by more than the time they
// took together. That errs toward the readings already held. A reading the
// process heard after another's, such as a reset's, then waits for the
// calls' records, which date the process's first line to grow once they have
// come at the latest of the calls' starts, or else for a line whose own
// growth dates it after the other reading. A line is never dated before the
// response its process last heard (see date). The line's API time
// is the signal rather than its cost: it grows by a whole number of
// milliseconds with every response, whatever the call cost, where the cost
// stays put for a call priced at nothing. Until either is seen, and so for
// every session after a restart, as what the tracker knows of responses is
// kept in memory only, no response is known.
//
// One session ID can belong to two live processes at once, as Claude Code
// keeps it when a session is resumed while its first terminal is still open,
// on the same account or another. Each process counts its own API time, and
// one that resumes the session counts on from what the session saved when a
// process last left it, if anything, so a line's API time is judged against
// the most its own process has reported, and a process's first line dates no
// response, whatever it reports. A line names its process by the PID Claude
// Code runs the status line with, and a later process may take the PID of one
// that has gone, so a process's start forgets what is known of the PID (see
// started). Lines that name none, from a Claude Code that sets no PID, are
// taken for one process: their API time counts as grown only past the most
// any of them has reported.
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
	// line of it has reported since the session last ended or the process
	// started. A process with no line seen since is absent.
	mostAPIDurationMS map[int]int64
}

// processAccount is a process of a session, by its PID, zero when unknown,
// on the account in dir.
type processAccount struct {
	pid int
	dir domain.ConfigDir
}

// call notes a call of the session that ended at at, after durationMS, or
// after a time unknown when durationMS is not positive.
func (c *responseClock) call(at time.Time, durationMS int64) {
	if began := startOf(at, durationMS); began.After(c.unplaced) {
		c.unplaced = began
	}
}

// line notes a status line of the session, from the process pid on the
// account in dir and observed at at, that reports the process's API time as
// apiDurationMS.
func (c *responseClock) line(dir domain.ConfigDir, pid int, apiDurationMS int64, at time.Time) {
	previous, seen := c.mostAPIDurationMS[pid]
	first, grew := !seen, seen && apiDurationMS > previous
	if first || grew {
		if c.mostAPIDurationMS == nil {
			c.mostAPIDurationMS = make(map[int]int64)
		}
		c.mostAPIDurationMS[pid] = apiDurationMS
	}
	process := processAccount{pid: pid, dir: dir}
	if grew {
		c.date(process, startOf(at, growth(previous, apiDurationMS)))
	}
	if first || grew || c.mostAPIDurationMS[pid] == 0 {
		c.date(process, c.unplaced)
		c.unplaced = time.Time{}
	}
}

// startOf returns when the calls that took durationMS in all, and ended at
// end, began. A garbled count dates them no later than end, since one that is
// not positive says nothing of when they began, and no earlier than the
// longest time.Duration before end.
func startOf(end time.Time, durationMS int64) time.Time {
	if durationMS <= 0 {
		return end
	}
	durationMS = min(durationMS, int64(math.MaxInt64/time.Millisecond))
	return end.Add(-time.Duration(durationMS) * time.Millisecond)
}

// growth returns how much a count grew from previous to latest, which is
// more than previous, or as much as an int64 holds when it grew by more.
func growth(previous, latest int64) int64 {
	if previous < 0 && latest > math.MaxInt64+previous {
		return math.MaxInt64
	}
	return latest - previous
}

// date notes a response that came at at to process, on its account. A
// process hears its responses in order, so one dated no later than the
// newest known of it changes nothing.
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

// started notes that the process pid has started on the session or resumed
// it. A process that had the PID before has gone, and may have left no end
// behind: one killed while another process of the session runs does not end
// it. What is known of the gone process is forgotten, so the new process's
// next line is a first line, and its count and responses are its own. Lines
// that name no process are taken for one, which no start replaces.
func (c *responseClock) started(pid int) {
	if pid <= 0 {
		return
	}
	delete(c.mostAPIDurationMS, pid)
	maps.DeleteFunc(c.newest, func(process processAccount, _ time.Time) bool {
		return process.pid == pid
	})
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
