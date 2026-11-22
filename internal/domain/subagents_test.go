package domain_test

import (
	"fmt"
	"testing"

	"github.com/dsj7419/loadline/internal/domain"
)

func subagentHook(sec int, name, agent string) domain.Hook {
	h := hook(sec, name)
	h.AgentID = agent
	return h
}

func applyAll(s domain.Session, events ...domain.Event) domain.Session {
	for _, e := range events {
		s = domain.Apply(s, e)
	}
	return s
}

func working() domain.Session { return applyAll(fresh(), hook(1, "UserPromptSubmit")) }

// A subagent's start can reach the server after its stop: the start was
// spooled, because the server did not answer within the hook's 300 ms, and
// the stop was posted directly; the drain delivers the start up to ten
// seconds later. Each hook counts whatever its age, so the count must not
// depend on the order they arrive in.
func TestApply_SubagentCountDoesNotDependOnArrivalOrder(t *testing.T) {
	inOrder := applyAll(working(),
		subagentHook(10, "SubagentStart", "agent-1"),
		subagentHook(20, "SubagentStop", "agent-1"),
	)
	outOfOrder := applyAll(working(),
		subagentHook(20, "SubagentStop", "agent-1"),
		subagentHook(10, "SubagentStart", "agent-1"),
	)

	if inOrder.Subagents != 0 || outOfOrder.Subagents != 0 {
		t.Errorf("start then stop leaves %d subagents, stop then start %d; want 0 both ways", inOrder.Subagents, outOfOrder.Subagents)
	}
}

func TestApply_SubagentStopBeforeItsStartCancelsOnlyThatStart(t *testing.T) {
	got := applyAll(working(),
		subagentHook(20, "SubagentStop", "agent-1"),
		subagentHook(10, "SubagentStart", "agent-1"),
		subagentHook(30, "SubagentStart", "agent-2"),
	)

	if got.Subagents != 1 {
		t.Errorf("Subagents = %d, want 1: agent-2 is running", got.Subagents)
	}
}

func TestApply_SubagentStartedTwiceCountsOnce(t *testing.T) {
	got := applyAll(working(),
		subagentHook(10, "SubagentStart", "agent-1"),
		subagentHook(11, "SubagentStart", "agent-1"),
	)
	if got.Subagents != 1 {
		t.Fatalf("Subagents = %d, want 1", got.Subagents)
	}

	got = domain.Apply(got, subagentHook(20, "SubagentStop", "agent-1"))
	if got.Subagents != 0 {
		t.Errorf("Subagents = %d after its stop, want 0", got.Subagents)
	}
}

// A session tells up to 32 running subagents apart; more at once are not
// counted. The count then stays true to those it holds.
func TestApply_SubagentsAreCountedUpTo32(t *testing.T) {
	s := working()
	for i := range 33 {
		s = domain.Apply(s, subagentHook(10, "SubagentStart", fmt.Sprintf("agent-%d", i)))
	}
	if s.Subagents != 32 {
		t.Fatalf("33 started: Subagents = %d, want 32", s.Subagents)
	}

	s = domain.Apply(s, subagentHook(20, "SubagentStop", "agent-0"))
	s = domain.Apply(s, subagentHook(20, "SubagentStop", "agent-32"))
	if s.Subagents != 31 {
		t.Errorf("after the stops of a counted subagent and the uncounted one: Subagents = %d, want 31", s.Subagents)
	}
}

// A session remembers the last 32 stops whose start it has not seen; the
// start of an older one counts as a subagent that is running.
func TestApply_UnmatchedStopsAreRememberedUpTo32(t *testing.T) {
	s := working()
	for i := range 33 {
		s = domain.Apply(s, subagentHook(20, "SubagentStop", fmt.Sprintf("agent-%d", i)))
	}

	s = domain.Apply(s, subagentHook(10, "SubagentStart", "agent-32"))
	if s.Subagents != 0 {
		t.Fatalf("the late start of a remembered stop: Subagents = %d, want 0", s.Subagents)
	}
	s = domain.Apply(s, subagentHook(10, "SubagentStart", "agent-0"))
	if s.Subagents != 1 {
		t.Errorf("the late start of a forgotten stop: Subagents = %d, want 1", s.Subagents)
	}
}

// No subagent outlives its session: a stop that arrives after the session
// ended and was reopened is no subagent of the new run.
func TestApply_EndingForgetsTheSubagents(t *testing.T) {
	s := applyAll(working(), subagentHook(10, "SubagentStart", "agent-1"), sessionEnd(20, "other"))
	s = applyAll(s, sessionStart(30, "resume"), subagentHook(40, "SubagentStop", "agent-1"), subagentHook(41, "SubagentStart", "agent-2"))

	if s.Subagents != 1 {
		t.Errorf("Subagents = %d, want 1: agent-2 is running in the reopened session", s.Subagents)
	}
}
