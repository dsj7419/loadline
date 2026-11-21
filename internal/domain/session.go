package domain

import "time"

// HeartbeatGrace is how long a session may go without a status line, while
// missing from the poll, before it counts as ended.
const HeartbeatGrace = 30 * time.Second

// The wait reasons hooks give match the poll's waitingFor text, so a session
// reads the same whichever signal arrives first.
const (
	reasonPermission = "permission prompt"
	reasonInput      = "input needed"
)

// endReasonLost is the EndReason of a session that vanished.
const endReasonLost = "lost"

// NewSession returns a session first seen at at, in StateStarting.
func NewSession(id SessionID, dir ConfigDir, at time.Time) Session {
	return Session{ID: id, Dir: dir, State: StateStarting, StateAt: at, StartedAt: at, LastSeenAt: at}
}

// Apply returns s updated by e. It is pure, and total: an event no rule
// covers, such as an unknown hook name, notification type or poll status,
// changes nothing but LastSeenAt and, by the account rule below, Dir. A poll
// still records what it shows about the process. These rules hold for every
// event:
//
//   - A Hook, Poll or Status is a sighting: LastSeenAt moves forward to its
//     time, never back. An Absent is not.
//   - An event at or before EndedAt of an ended session changes nothing
//     else. Whatever it reports happened before the end, so a late event can
//     never bring a session back.
//   - Only an event at or after StateAt may change the state or move the
//     session to the event's account. An older one is stale: the session
//     has been observed since.
//   - An ended session comes back only on SessionStart; on a poll showing a
//     live process other than the one it ended in (any process, if it
//     ended lost); or on a background session's own verdict, which parks a
//     finished one.
func Apply(s Session, e Event) Session {
	switch e := e.(type) {
	case Hook:
		return applyHook(s, e)
	case Poll:
		return applyPoll(s, e)
	case Absent:
		return applyAbsent(s, e)
	case Status:
		return applyStatus(s, e)
	default:
		return s
	}
}

type hookRule func(Session, Hook) Session

// hookRules gives each hook's effect on a live session. A hook with no rule
// leaves the state alone: SessionStart, which Claude Code also fires mid-life
// for compact, resume and clear; the subagent hooks, which only count; and
// any hook added after this was written.
var hookRules = map[string]hookRule{
	"UserPromptSubmit":  enter(StateWorking, ""),
	"PermissionRequest": enter(StateNeedsYou, reasonPermission),
	"Notification":      onNotification,
	"PreToolUse":        onPreToolUse,
	"PostToolUse":       onPostToolUse,
	"Stop":              enter(StateIdle, ""),
	"StopFailure":       enter(StateError, ""),
	"PreCompact":        enter(StateCompacting, ""),
	"PostCompact":       onPostCompact,
	"SessionEnd":        onSessionEnd,
}

func applyHook(s Session, h Hook) Session {
	s.LastSeenAt = latest(s.LastSeenAt, h.At)
	if s.endedBy(h.At) {
		return s
	}
	s.Subagents = countSubagents(s.Subagents, h.Name)
	if h.At.Before(s.StateAt) {
		return s
	}
	// Subagent hooks run in the session's own process, so they fill it too.
	if h.PID > 0 {
		s.PID = h.PID
	}
	s.Dir = h.Dir
	if h.AgentID != "" {
		// A subagent's prompts still show as the session needing you: the
		// poll sees its process waiting.
		return s
	}
	if s.State == StateEnded {
		if h.Name == "SessionStart" {
			return moveTo(s, StateStarting, "", h.At)
		}
		return s
	}
	rule, ok := hookRules[h.Name]
	if !ok {
		return s
	}
	return rule(s, h)
}

// countSubagents applies a subagent start or stop to the running count. It
// runs even for a hook older than the current state: each hook is a delta,
// and dropping one would leave the count wrong for good.
func countSubagents(n int, hook string) int {
	switch hook {
	case "SubagentStart":
		return n + 1
	case "SubagentStop":
		return max(n-1, 0)
	default:
		return n
	}
}

func enter(state State, reason string) hookRule {
	return func(s Session, h Hook) Session { return moveTo(s, state, reason, h.At) }
}

func onNotification(s Session, h Hook) Session {
	switch h.NotificationType {
	case "permission_prompt":
		return moveTo(s, StateNeedsYou, reasonPermission, h.At)
	case "elicitation_dialog", "agent_needs_input":
		return moveTo(s, StateNeedsYou, reasonInput, h.At)
	case "idle_prompt":
		return moveTo(s, StateIdle, "", h.At)
	default:
		return s
	}
}

func onPreToolUse(s Session, h Hook) Session {
	if h.ToolName != "AskUserQuestion" {
		return s
	}
	return moveTo(s, StateNeedsYou, reasonInput, h.At)
}

func onPostToolUse(s Session, h Hook) Session {
	if s.State != StateNeedsYou {
		return s
	}
	return moveTo(s, StateWorking, "", h.At)
}

func onPostCompact(s Session, h Hook) Session {
	if s.State != StateCompacting {
		// A newer signal has already moved the session on.
		return s
	}
	return moveTo(s, s.prev, "", h.At)
}

func onSessionEnd(s Session, h Hook) Session {
	s = moveTo(s, StateEnded, "", h.At)
	s.EndReason = h.Reason
	return s
}

func applyPoll(s Session, p Poll) Session {
	s.LastSeenAt = latest(s.LastSeenAt, p.At)
	if s.endedBy(p.At) || p.At.Before(s.StateAt) {
		return s
	}
	// Decide before filling: whether an ended session reopens depends on the
	// process ID known until now.
	state, reason, ok := pollState(s, p)
	s.Dir = p.Dir
	s = fillFromPoll(s, p)
	if !ok {
		return s
	}
	return moveTo(s, state, reason, p.At)
}

// fillFromPoll copies what the poll knows about the session's process. A
// missing value never erases a known one.
func fillFromPoll(s Session, p Poll) Session {
	s.Kind = p.Kind
	if p.PID > 0 {
		s.PID = p.PID
	}
	if p.Name != "" {
		s.Name = p.Name
	}
	if s.ProjectDir == "" {
		s.ProjectDir = p.Cwd
	}
	// The process may have started well before Loadline first saw it.
	if !p.StartedAt.IsZero() && p.StartedAt.Before(s.StartedAt) {
		s.StartedAt = p.StartedAt
	}
	return s
}

// pollState is the state a poll entry moves the session to. ok is false when
// the entry gives no reason to move it.
func pollState(s Session, p Poll) (state State, reason string, ok bool) {
	if s.State == StateEnded {
		return reopenState(s, p)
	}
	return reportedState(s.State, p)
}

// reopenState is the state a poll entry brings an ended session back to.
// Without a process, only a background session's own verdict counts, and a
// finished one is parked rather than ended because it can be resumed. A live
// process counts unless it is the one the session ended in: the poll that
// follows SessionEnd can still list the exiting process. A session that
// ended lost had no such exit, so any live process brings it back.
func reopenState(s Session, p Poll) (state State, reason string, ok bool) {
	if p.PID == 0 {
		state, ok = backgroundVerdict(p)
		return state, "", ok
	}
	if !s.Lost && p.PID == s.PID {
		return s.State, "", false
	}
	if state, reason, ok = reportedState(s.State, p); !ok {
		// The process is alive but doing something this version can't name.
		state, reason = StateStarting, ""
	}
	return state, reason, true
}

// reportedState is the state a poll entry reports for a session currently in
// current. ok is false when the entry reports nothing this version
// understands.
func reportedState(current State, p Poll) (state State, reason string, ok bool) {
	if verdict, settled := backgroundVerdict(p); settled {
		return verdict, "", true
	}
	switch p.Status {
	case "busy":
		// Compaction runs as a busy turn; PostCompact is what ends it.
		if current == StateCompacting {
			return current, "", true
		}
		return StateWorking, "", true
	case "waiting":
		return StateNeedsYou, p.WaitingFor, true
	case "idle":
		// A failed turn leaves the process idle. The error stands until the
		// next prompt or a busy poll.
		if current == StateError {
			return current, "", true
		}
		return StateIdle, "", true
	default:
		return current, "", false
	}
}

// backgroundVerdict is the state a background session's own state settles,
// if it settles one. A finished session is parked only once its process has
// gone; while the process lives, its status says what it is doing.
func backgroundVerdict(p Poll) (verdict State, ok bool) {
	if p.Kind != KindBackground {
		return verdict, false
	}
	switch p.BgState {
	case "failed":
		return StateError, true
	case "stopped":
		return StateEnded, true
	case "done", "blocked":
		if p.PID == 0 {
			return StateParked, true
		}
	}
	return verdict, false
}

func applyAbsent(s Session, a Absent) Session {
	// Only the poll of the session's own account can miss it. An absence is
	// not a sighting, so LastSeenAt stays.
	if a.Dir != s.Dir || a.At.Before(s.StateAt) || s.State == StateParked || s.State == StateEnded {
		return s
	}
	// Until its first status line, a session is measured from its start, so
	// a new one gets the full grace.
	if a.At.Sub(latest(s.LastStatusAt, s.StartedAt)) <= HeartbeatGrace {
		return s
	}
	s = moveTo(s, StateEnded, "", a.At)
	s.EndReason, s.Lost = endReasonLost, true
	return s
}

func applyStatus(s Session, st Status) Session {
	s.LastSeenAt = latest(s.LastSeenAt, st.At)
	if s.endedBy(st.At) {
		return s
	}
	if !st.At.Before(s.StateAt) {
		s.Dir = st.Dir
		if s.State == StateStarting {
			s = moveTo(s, StateIdle, "", st.At)
		}
	}
	// Each status line is a full snapshot, so only the newest counts. Its
	// rate limits belong to the account, which the tracker keeps.
	if st.At.Before(s.LastStatusAt) {
		return s
	}
	s.Metrics, s.LastStatusAt = st.Metrics, st.At
	if st.Name != "" {
		s.Name = st.Name
	}
	if st.ProjectDir != "" {
		s.ProjectDir = st.ProjectDir
	}
	return s
}

// moveTo puts the session in state, waiting for reason, as of at. StateAt
// and the previous state change only when the state does, so a repeated
// signal never resets the time in state. Leaving StateEnded reopens the
// session; entering it records the end and clears the subagent count, since
// no subagent outlives its session.
func moveTo(s Session, state State, reason string, at time.Time) Session {
	s.WaitReason = reason
	if state == s.State {
		return s
	}
	if s.State == StateEnded {
		s.EndedAt, s.EndReason, s.Lost = time.Time{}, "", false
	}
	if state == StateEnded {
		s.EndedAt, s.Subagents = at, 0
	}
	s.prev, s.State, s.StateAt = s.State, state, at
	return s
}

// endedBy reports whether the session had already ended at t.
func (s Session) endedBy(t time.Time) bool {
	return s.State == StateEnded && !t.After(s.EndedAt)
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
