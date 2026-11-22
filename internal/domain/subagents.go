package domain

// maxTrackedSubagents is how many subagent IDs a session holds of each kind:
// those running, and those stopped before their start was seen.
const maxTrackedSubagents = 32

// agentIDs is a bounded set of subagent IDs, oldest first. It is an array
// rather than a slice, so that a copy of a Session shares nothing with the
// original and Sessions stay comparable.
type agentIDs struct {
	ids [maxTrackedSubagents]string
	n   int
}

func (a agentIDs) has(id string) bool {
	for _, held := range a.ids[:a.n] {
		if held == id {
			return true
		}
	}
	return false
}

func (a agentIDs) full() bool { return a.n == maxTrackedSubagents }

// with returns a with id added as the newest, forgetting the oldest when a is
// full.
func (a agentIDs) with(id string) agentIDs {
	if a.full() {
		a = a.without(a.ids[0])
	}
	a.ids[a.n] = id
	a.n++
	return a
}

// without returns a with id taken out.
func (a agentIDs) without(id string) agentIDs {
	for i, held := range a.ids[:a.n] {
		if held == id {
			copy(a.ids[i:], a.ids[i+1:a.n])
			a.n--
			a.ids[a.n] = ""
			break
		}
	}
	return a
}

// countSubagents applies a subagent's start or stop to the running count. It
// runs even for a stale hook: each hook is a delta, and dropping one would
// leave the count wrong for good. Subagents are told apart by agent ID, so a
// stop that arrives before its start, as a start drained from the spool
// does, cancels that start and no other, and a start that comes twice counts
// once. A hook without an agent ID moves a count of its own kind instead.
func countSubagents(s Session, h Hook) Session {
	switch {
	case h.Name == "SubagentStart" && h.AgentID == "":
		s.Subagents++
	case h.Name == "SubagentStop" && h.AgentID == "":
		// Only the subagents that came without an ID are counted this way.
		if s.Subagents > s.running.n {
			s.Subagents--
		}
	case h.Name == "SubagentStart":
		s = startSubagent(s, h.AgentID)
	case h.Name == "SubagentStop":
		s = stopSubagent(s, h.AgentID)
	}
	return s
}

// startSubagent counts the subagent id as running, unless it already counts
// or its stop came first: an agent ID is a single subagent's, and one that
// has stopped never runs again. Beyond maxTrackedSubagents running at once a
// subagent is not counted.
func startSubagent(s Session, id string) Session {
	if s.stopped.has(id) || s.running.has(id) || s.running.full() {
		return s
	}
	s.running = s.running.with(id)
	s.Subagents++
	return s
}

// stopSubagent takes the subagent id off the running count, or, when its
// start has not been seen, remembers the stop for when the start comes.
func stopSubagent(s Session, id string) Session {
	if !s.running.has(id) {
		s.stopped = s.stopped.with(id)
		return s
	}
	s.running = s.running.without(id)
	s.Subagents--
	return s
}
