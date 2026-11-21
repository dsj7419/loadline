package domain_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

const (
	sid    domain.SessionID = "3f1c7a52-0d4e-4b8e-9a51-6c2f0e7d9b10"
	dirA   domain.ConfigDir = "/home/dev/.claude"
	dirB   domain.ConfigDir = "/home/dev/.claude-b"
	pid                     = 4242
	newPID                  = 5151
)

// t0 is when every test session is first seen.
var t0 = time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)

// at is sec seconds after t0.
func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

// Starting sessions.

func fresh() domain.Session { return domain.NewSession(sid, dirA, t0) }

func sessionIn(state domain.State, sinceSec int) domain.Session {
	s := fresh()
	s.State, s.StateAt = state, at(sinceSec)
	return s
}

func endedAt(sec int) domain.Session {
	s := sessionIn(domain.StateEnded, sec)
	s.EndedAt, s.EndReason = at(sec), "prompt_input_exit"
	return s
}

func lostAt(sec int) domain.Session {
	s := endedAt(sec)
	s.EndReason, s.Lost = "lost", true
	return s
}

func withPID(s domain.Session, processID int) domain.Session {
	s.PID = processID
	return s
}

// Events.

func hook(sec int, name string) domain.Hook {
	return domain.Hook{At: at(sec), Dir: dirA, Session: sid, PID: pid, Name: name}
}

func notification(sec int, notificationType string) domain.Hook {
	h := hook(sec, "Notification")
	h.NotificationType = notificationType
	return h
}

func preToolUse(sec int, tool string) domain.Hook {
	h := hook(sec, "PreToolUse")
	h.ToolName = tool
	return h
}

func sessionStart(sec int, source string) domain.Hook {
	h := hook(sec, "SessionStart")
	h.Source = source
	return h
}

func sessionEnd(sec int, reason string) domain.Hook {
	h := hook(sec, "SessionEnd")
	h.Reason = reason
	return h
}

func inSubagent(h domain.Hook) domain.Hook {
	h.AgentID = "agent-7"
	return h
}

func hookOnB(h domain.Hook) domain.Hook {
	h.Dir = dirB
	return h
}

func hookFrom(processID int, h domain.Hook) domain.Hook {
	h.PID = processID
	return h
}

// poll is an interactive session's entry while its process is alive.
func poll(sec int, status string) domain.Poll {
	return domain.Poll{
		At: at(sec), Dir: dirA, Session: sid,
		Kind: domain.KindInteractive, PID: pid, Status: status,
	}
}

func waiting(sec int, waitingFor string) domain.Poll {
	p := poll(sec, "waiting")
	p.WaitingFor = waitingFor
	return p
}

// exited is an interactive session's entry after its process has gone.
func exited(sec int) domain.Poll {
	p := poll(sec, "")
	p.PID = 0
	return p
}

// background is a background session's entry with no live process.
func background(sec int, bgState string) domain.Poll {
	return domain.Poll{
		At: at(sec), Dir: dirA, Session: sid,
		Kind: domain.KindBackground, BgState: bgState,
	}
}

func liveBackground(sec int, bgState, status string) domain.Poll {
	p := background(sec, bgState)
	p.PID, p.Status = pid, status
	return p
}

func pollOnB(p domain.Poll) domain.Poll {
	p.Dir = dirB
	return p
}

func pollFrom(processID int, p domain.Poll) domain.Poll {
	p.PID = processID
	return p
}

func absent(sec int) domain.Absent {
	return domain.Absent{At: at(sec), Dir: dirA, Session: sid}
}

func status(sec int) domain.Status {
	return domain.Status{At: at(sec), Dir: dirA, Session: sid}
}

// outcome is the part of a session the state rules decide.
type outcome struct {
	State      domain.State
	StateAt    time.Time
	WaitReason string
	Subagents  int
	Dir        domain.ConfigDir
	Lost       bool
	EndedAt    time.Time
	EndReason  string
}

func outcomeOf(s domain.Session) outcome {
	return outcome{
		State: s.State, StateAt: s.StateAt, WaitReason: s.WaitReason,
		Subagents: s.Subagents, Dir: s.Dir, Lost: s.Lost,
		EndedAt: s.EndedAt, EndReason: s.EndReason,
	}
}

func (o outcome) equal(p outcome) bool {
	return o.State == p.State && o.StateAt.Equal(p.StateAt) &&
		o.WaitReason == p.WaitReason && o.Subagents == p.Subagents &&
		o.Dir == p.Dir && o.Lost == p.Lost &&
		o.EndedAt.Equal(p.EndedAt) && o.EndReason == p.EndReason
}

func (o outcome) String() string {
	return fmt.Sprintf("{%v since %s, wait %q, subagents %d, dir %s, lost %t, ended %s, end reason %q}",
		o.State, sinceT0(o.StateAt), o.WaitReason, o.Subagents, o.Dir, o.Lost, sinceT0(o.EndedAt), o.EndReason)
}

func sinceT0(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Sub(t0).String()
}

func wantIn(state domain.State, sinceSec int) outcome {
	return outcome{State: state, StateAt: at(sinceSec), Dir: dirA}
}

func wantNeedsYou(sinceSec int, reason string) outcome {
	o := wantIn(domain.StateNeedsYou, sinceSec)
	o.WaitReason = reason
	return o
}

func wantEnded(sec int, reason string) outcome {
	o := wantIn(domain.StateEnded, sec)
	o.EndedAt, o.EndReason = at(sec), reason
	return o
}

func wantLost(sec int) outcome {
	o := wantEnded(sec, "lost")
	o.Lost = true
	return o
}

func TestApply(t *testing.T) {
	tests := []struct {
		name   string
		start  domain.Session
		events []domain.Event
		want   outcome
	}{
		// Hooks.
		{
			name:   "UserPromptSubmit starts work",
			start:  sessionIn(domain.StateIdle, 10),
			events: []domain.Event{hook(20, "UserPromptSubmit")},
			want:   wantIn(domain.StateWorking, 20),
		},
		{
			name:   "PermissionRequest needs you",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "PermissionRequest")},
			want:   wantNeedsYou(20, "permission prompt"),
		},
		{
			name:   "permission_prompt notification needs you",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{notification(20, "permission_prompt")},
			want:   wantNeedsYou(20, "permission prompt"),
		},
		{
			name:   "permission_prompt notification after PermissionRequest keeps StateAt",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "PermissionRequest"), notification(26, "permission_prompt")},
			want:   wantNeedsYou(20, "permission prompt"),
		},
		{
			name:   "elicitation_dialog notification needs input",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{notification(20, "elicitation_dialog")},
			want:   wantNeedsYou(20, "input needed"),
		},
		{
			name:   "agent_needs_input notification needs input",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{notification(20, "agent_needs_input")},
			want:   wantNeedsYou(20, "input needed"),
		},
		{
			name:   "AskUserQuestion needs input",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{preToolUse(20, "AskUserQuestion")},
			want:   wantNeedsYou(20, "input needed"),
		},
		{
			name:   "other PreToolUse keeps working",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{preToolUse(20, "Bash")},
			want:   wantIn(domain.StateWorking, 10),
		},
		{
			name:   "PostToolUse after a prompt resumes work",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "PermissionRequest"), hook(30, "PostToolUse")},
			want:   wantIn(domain.StateWorking, 30),
		},
		{
			name:   "PostToolUse outside a prompt changes nothing",
			start:  sessionIn(domain.StateIdle, 10),
			events: []domain.Event{hook(20, "PostToolUse")},
			want:   wantIn(domain.StateIdle, 10),
		},
		{
			name:   "Stop goes idle",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "Stop")},
			want:   wantIn(domain.StateIdle, 20),
		},
		{
			name:   "idle_prompt notification goes idle",
			start:  sessionIn(domain.StateNeedsYou, 10),
			events: []domain.Event{notification(70, "idle_prompt")},
			want:   wantIn(domain.StateIdle, 70),
		},
		{
			name:   "unknown notification type changes nothing",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{notification(20, "auth_success")},
			want:   wantIn(domain.StateWorking, 10),
		},
		{
			name:   "PreCompact compacts",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "PreCompact")},
			want:   wantIn(domain.StateCompacting, 20),
		},
		{
			name:   "PostCompact restores the state before compaction",
			start:  sessionIn(domain.StateIdle, 10),
			events: []domain.Event{hook(20, "PreCompact"), hook(30, "PostCompact")},
			want:   wantIn(domain.StateIdle, 30),
		},
		{
			name:   "repeated PreCompact keeps the state to restore",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "PreCompact"), hook(25, "PreCompact"), hook(30, "PostCompact")},
			want:   wantIn(domain.StateWorking, 30),
		},
		{
			name:   "PostCompact outside compaction changes nothing",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "PostCompact")},
			want:   wantIn(domain.StateWorking, 10),
		},
		{
			name:   "StopFailure is an error",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "StopFailure")},
			want:   wantIn(domain.StateError, 20),
		},
		{
			name:   "the next prompt clears an error",
			start:  sessionIn(domain.StateError, 10),
			events: []domain.Event{hook(20, "UserPromptSubmit")},
			want:   wantIn(domain.StateWorking, 20),
		},
		{
			name:   "SessionEnd ends with its reason",
			start:  sessionIn(domain.StateIdle, 10),
			events: []domain.Event{sessionEnd(20, "logout")},
			want:   wantEnded(20, "logout"),
		},
		{
			name:   "SessionStart keeps a new session starting",
			start:  fresh(),
			events: []domain.Event{sessionStart(5, "startup")},
			want:   wantIn(domain.StateStarting, 0),
		},
		{
			name:   "SessionStart compact on working session keeps working",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{sessionStart(20, "compact")},
			want:   wantIn(domain.StateWorking, 10),
		},
		{
			name:   "resume reopens",
			start:  endedAt(20),
			events: []domain.Event{sessionStart(30, "resume")},
			want:   wantIn(domain.StateStarting, 30),
		},
		{
			name:   "resume reopens a lost session",
			start:  lostAt(40),
			events: []domain.Event{sessionStart(50, "resume")},
			want:   wantIn(domain.StateStarting, 50),
		},
		{
			name:   "SessionStart at the end time does not reopen",
			start:  endedAt(20),
			events: []domain.Event{sessionStart(20, "resume")},
			want:   wantEnded(20, "prompt_input_exit"),
		},
		{
			name:   "hooks other than SessionStart do not reopen",
			start:  endedAt(20),
			events: []domain.Event{hook(30, "UserPromptSubmit"), hook(31, "Stop")},
			want:   wantEnded(20, "prompt_input_exit"),
		},
		{
			name:   "unknown hook changes nothing",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "TeammateIdle")},
			want:   wantIn(domain.StateWorking, 10),
		},
		{
			name:   "stale hook ignored",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(9, "Stop")},
			want:   wantIn(domain.StateWorking, 10),
		},

		// Subagents.
		{
			name:   "subagent permission hook does not change state",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{inSubagent(hook(20, "PermissionRequest"))},
			want:   wantIn(domain.StateWorking, 10),
		},
		{
			name:   "SubagentStart counts with or without an agent ID",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "SubagentStart"), inSubagent(hook(21, "SubagentStart"))},
			want:   outcome{State: domain.StateWorking, StateAt: at(10), Dir: dirA, Subagents: 2},
		},
		{
			name:  "SubagentStop counts down, floored at zero",
			start: sessionIn(domain.StateWorking, 10),
			events: []domain.Event{
				hook(20, "SubagentStart"), inSubagent(hook(21, "SubagentStop")), hook(22, "SubagentStop"),
			},
			want: wantIn(domain.StateWorking, 10),
		},
		{
			name:  "SubagentStop older than the state still counts",
			start: sessionIn(domain.StateWorking, 10),
			events: []domain.Event{
				hook(20, "SubagentStart"), waiting(30, "permission prompt"), inSubagent(hook(25, "SubagentStop")),
			},
			want: wantNeedsYou(30, "permission prompt"),
		},
		{
			name:   "ending clears the subagent count",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "SubagentStart"), sessionEnd(30, "other")},
			want:   wantEnded(30, "other"),
		},

		// Polls.
		{
			name:   "stale poll ignored",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{poll(9, "idle")},
			want:   wantIn(domain.StateWorking, 10),
		},
		{
			name:   "busy poll means working",
			start:  sessionIn(domain.StateIdle, 10),
			events: []domain.Event{poll(20, "busy")},
			want:   wantIn(domain.StateWorking, 20),
		},
		{
			name:   "repeated busy poll keeps StateAt",
			start:  sessionIn(domain.StateIdle, 10),
			events: []domain.Event{poll(20, "busy"), poll(25, "busy")},
			want:   wantIn(domain.StateWorking, 20),
		},
		{
			name:   "busy poll resolves a prompt",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "PermissionRequest"), poll(25, "busy")},
			want:   wantIn(domain.StateWorking, 25),
		},
		{
			name:   "waiting poll needs you for its reason",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{waiting(20, "sandbox request")},
			want:   wantNeedsYou(20, "sandbox request"),
		},
		{
			name:   "waiting poll updates the reason without moving StateAt",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "PermissionRequest"), waiting(25, "worker request")},
			want:   wantNeedsYou(20, "worker request"),
		},
		{
			name:   "idle poll goes idle",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{poll(20, "idle")},
			want:   wantIn(domain.StateIdle, 20),
		},
		{
			name:   "compacting survives busy poll",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(20, "PreCompact"), poll(25, "busy")},
			want:   wantIn(domain.StateCompacting, 20),
		},
		{
			name:   "error survives idle poll",
			start:  sessionIn(domain.StateError, 10),
			events: []domain.Event{poll(20, "idle")},
			want:   wantIn(domain.StateError, 10),
		},
		{
			name:   "error survives idle poll, cleared by busy poll",
			start:  sessionIn(domain.StateError, 10),
			events: []domain.Event{poll(20, "idle"), poll(25, "busy")},
			want:   wantIn(domain.StateWorking, 25),
		},
		{
			name:   "unknown poll status changes nothing",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{poll(20, "paused")},
			want:   wantIn(domain.StateWorking, 10),
		},
		{
			name:   "poll of an exited process changes nothing",
			start:  sessionIn(domain.StateIdle, 10),
			events: []domain.Event{exited(20)},
			want:   wantIn(domain.StateIdle, 10),
		},
		{
			name:   "background parked",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{background(20, "done")},
			want:   wantIn(domain.StateParked, 20),
		},
		{
			name:   "blocked background without a process is parked",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{background(20, "blocked")},
			want:   wantIn(domain.StateParked, 20),
		},
		{
			name:   "blocked background with a live process follows its status",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{liveBackground(20, "blocked", "waiting")},
			want:   wantNeedsYou(20, ""),
		},
		{
			name:   "background with a live process is not parked",
			start:  sessionIn(domain.StateIdle, 10),
			events: []domain.Event{liveBackground(20, "done", "busy")},
			want:   wantIn(domain.StateWorking, 20),
		},
		{
			name:   "failed background is an error",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{background(20, "failed")},
			want:   wantIn(domain.StateError, 20),
		},
		{
			name:   "stopped background ends",
			start:  sessionIn(domain.StateParked, 10),
			events: []domain.Event{background(20, "stopped")},
			want:   wantEnded(20, ""),
		},
		{
			name:   "poll right after SessionEnd with same pid does not reopen",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{poll(15, "busy"), sessionEnd(20, "prompt_input_exit"), poll(21, "idle")},
			want:   wantEnded(20, "prompt_input_exit"),
		},
		{
			name:   "SessionEnd records the pid that guards the reopen",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{sessionEnd(20, "prompt_input_exit"), poll(21, "idle")},
			want:   wantEnded(20, "prompt_input_exit"),
		},
		{
			name:  "poll with new pid reopens",
			start: sessionIn(domain.StateWorking, 10),
			events: []domain.Event{
				poll(15, "busy"), sessionEnd(20, "prompt_input_exit"), pollFrom(newPID, poll(30, "busy")),
			},
			want: wantIn(domain.StateWorking, 30),
		},
		{
			name:   "poll reopens a session whose pid was never known",
			start:  endedAt(20),
			events: []domain.Event{poll(30, "busy")},
			want:   wantIn(domain.StateWorking, 30),
		},
		{
			name:   "lost session reopened by poll",
			start:  withPID(lostAt(20), pid),
			events: []domain.Event{poll(30, "busy")},
			want:   wantIn(domain.StateWorking, 30),
		},
		{
			name:   "live process with an unknown status reopens as starting",
			start:  withPID(lostAt(20), pid),
			events: []domain.Event{poll(30, "paused")},
			want:   wantIn(domain.StateStarting, 30),
		},
		{
			name:   "poll of an exited process does not reopen",
			start:  endedAt(20),
			events: []domain.Event{exited(30)},
			want:   wantEnded(20, "prompt_input_exit"),
		},
		{
			name:   "status without a process does not reopen",
			start:  withPID(endedAt(20), pid),
			events: []domain.Event{pollFrom(0, poll(30, "busy"))},
			want:   wantEnded(20, "prompt_input_exit"),
		},
		{
			name:   "finished background session is parked after its end",
			start:  endedAt(20),
			events: []domain.Event{background(30, "done")},
			want:   wantIn(domain.StateParked, 30),
		},
		{
			name:   "blocked background session is parked after its end",
			start:  endedAt(20),
			events: []domain.Event{background(30, "blocked")},
			want:   wantIn(domain.StateParked, 30),
		},
		{
			name:   "failed background session is an error after its end",
			start:  endedAt(20),
			events: []domain.Event{background(30, "failed")},
			want:   wantIn(domain.StateError, 30),
		},
		{
			name:   "stopped background session stays ended",
			start:  endedAt(20),
			events: []domain.Event{background(30, "stopped")},
			want:   wantEnded(20, "prompt_input_exit"),
		},
		{
			name:   "background session without a verdict stays ended",
			start:  endedAt(20),
			events: []domain.Event{background(30, "working")},
			want:   wantEnded(20, "prompt_input_exit"),
		},
		{
			name:   "stopped background with a live process stays ended",
			start:  endedAt(20),
			events: []domain.Event{liveBackground(30, "stopped", "idle")},
			want:   wantEnded(20, "prompt_input_exit"),
		},

		// Absent.
		{
			name:   "absent within grace keeps the session",
			start:  fresh(),
			events: []domain.Event{status(0), absent(29)},
			want:   wantIn(domain.StateIdle, 0),
		},
		{
			name:   "absent at exactly the grace keeps the session",
			start:  fresh(),
			events: []domain.Event{status(0), absent(30)},
			want:   wantIn(domain.StateIdle, 0),
		},
		{
			name:   "absent ends only after grace",
			start:  fresh(),
			events: []domain.Event{status(0), absent(29), absent(31)},
			want:   wantLost(31),
		},
		{
			name:   "absent counts from the latest status line",
			start:  fresh(),
			events: []domain.Event{status(0), status(20), absent(45)},
			want:   wantIn(domain.StateIdle, 0),
		},
		{
			name:   "new session without status line survives first absent poll",
			start:  fresh(),
			events: []domain.Event{absent(5)},
			want:   wantIn(domain.StateStarting, 0),
		},
		{
			name:   "new session without status line ends after grace",
			start:  fresh(),
			events: []domain.Event{absent(31)},
			want:   wantLost(31),
		},
		{
			name:   "absent leaves a parked session alone",
			start:  sessionIn(domain.StateParked, 10),
			events: []domain.Event{absent(100)},
			want:   wantIn(domain.StateParked, 10),
		},
		{
			name:   "absent leaves an ended session alone",
			start:  endedAt(20),
			events: []domain.Event{absent(100)},
			want:   wantEnded(20, "prompt_input_exit"),
		},
		{
			name:   "absent reported by another account is ignored",
			start:  fresh(),
			events: []domain.Event{status(0), domain.Absent{At: at(100), Dir: dirB, Session: sid}},
			want:   wantIn(domain.StateIdle, 0),
		},
		{
			name:   "stale absent ignored",
			start:  fresh(),
			events: []domain.Event{status(0), hook(60, "UserPromptSubmit"), absent(59)},
			want:   wantIn(domain.StateWorking, 60),
		},

		// Status line.
		{
			name:   "status line moves starting to idle",
			start:  fresh(),
			events: []domain.Event{status(5)},
			want:   wantIn(domain.StateIdle, 5),
		},
		{
			name:   "status line leaves other states alone",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{status(20)},
			want:   wantIn(domain.StateWorking, 10),
		},
		{
			name:   "status line older than the state leaves it starting",
			start:  sessionIn(domain.StateStarting, 10),
			events: []domain.Event{status(9)},
			want:   wantIn(domain.StateStarting, 10),
		},

		// Account move.
		{
			name:   "account moves on resume under B",
			start:  endedAt(20),
			events: []domain.Event{hookOnB(sessionStart(30, "resume"))},
			want:   outcome{State: domain.StateStarting, StateAt: at(30), Dir: dirB},
		},
		{
			name:   "poll under another account moves the session",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{pollOnB(poll(20, "busy"))},
			want:   outcome{State: domain.StateWorking, StateAt: at(10), Dir: dirB},
		},
		{
			name:   "status line under another account moves the session",
			start:  sessionIn(domain.StateIdle, 10),
			events: []domain.Event{domain.Status{At: at(20), Dir: dirB, Session: sid}},
			want:   outcome{State: domain.StateIdle, StateAt: at(10), Dir: dirB},
		},
		{
			name:   "stale event does not move the account",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{pollOnB(poll(9, "busy")), hookOnB(hook(9, "Stop"))},
			want:   wantIn(domain.StateWorking, 10),
		},

		// No resurrection.
		{
			name:   "late PostToolUse after SessionEnd ignored",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{hook(12, "PermissionRequest"), sessionEnd(20, "other"), hook(19, "PostToolUse")},
			want:   wantEnded(20, "other"),
		},
		{
			name:   "old poll after SessionEnd ignored",
			start:  sessionIn(domain.StateWorking, 10),
			events: []domain.Event{sessionEnd(20, "other"), poll(18, "busy")},
			want:   wantEnded(20, "other"),
		},
		{
			name:   "event at the end time does not reopen",
			start:  endedAt(20),
			events: []domain.Event{poll(20, "busy"), hook(20, "UserPromptSubmit")},
			want:   wantEnded(20, "prompt_input_exit"),
		},
		{
			name:   "event at the end time does not move the account",
			start:  endedAt(20),
			events: []domain.Event{hookOnB(sessionStart(20, "resume")), domain.Status{At: at(20), Dir: dirB, Session: sid}},
			want:   wantEnded(20, "prompt_input_exit"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.start
			for _, e := range tc.events {
				s = domain.Apply(s, e)
			}
			if got := outcomeOf(s); !got.equal(tc.want) {
				t.Errorf("after %d events\n got %v\nwant %v", len(tc.events), got, tc.want)
			}
		})
	}
}

func TestNewSession(t *testing.T) {
	want := domain.Session{
		ID: sid, Dir: dirA, State: domain.StateStarting,
		StateAt: at(5), StartedAt: at(5), LastSeenAt: at(5),
	}
	if got := domain.NewSession(sid, dirA, at(5)); !reflect.DeepEqual(got, want) {
		t.Errorf("NewSession = %+v, want %+v", got, want)
	}
}

func TestApply_NilEventChangesNothing(t *testing.T) {
	s := sessionIn(domain.StateWorking, 10)
	if got := domain.Apply(s, nil); !reflect.DeepEqual(got, s) {
		t.Errorf("Apply(s, nil) = %+v, want %+v", got, s)
	}
}

func TestApply_LastSeenAt(t *testing.T) {
	seenAt := func(s domain.Session, sec int) domain.Session {
		s.LastSeenAt = at(sec)
		return s
	}
	tests := []struct {
		name  string
		start domain.Session
		event domain.Event
		want  time.Time
	}{
		{"hook moves it forward", fresh(), hook(20, "Stop"), at(20)},
		{"poll moves it forward", fresh(), poll(20, "busy"), at(20)},
		{"status line moves it forward", fresh(), status(20), at(20)},
		{"stale event still counts as a sighting", sessionIn(domain.StateWorking, 30), poll(20, "idle"), at(20)},
		{"event before the end still counts as a sighting", endedAt(30), hook(20, "PostToolUse"), at(20)},
		{"older event never moves it back", seenAt(fresh(), 40), hook(20, "Stop"), at(40)},
		{"absence is not a sighting", fresh(), absent(100), t0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.Apply(tc.start, tc.event).LastSeenAt; !got.Equal(tc.want) {
				t.Errorf("LastSeenAt = %s, want %s", sinceT0(got), sinceT0(tc.want))
			}
		})
	}
}

func TestApply_PreviousIsTheStateBeforeTheCurrentOne(t *testing.T) {
	s := domain.Apply(sessionIn(domain.StateWorking, 10), hook(20, "PreCompact"))
	if got := s.Previous(); got != domain.StateWorking {
		t.Errorf("after PreCompact, Previous() = %v, want working", got)
	}

	s = domain.Apply(s, hook(30, "PostCompact"))
	if got := s.Previous(); got != domain.StateCompacting {
		t.Errorf("after PostCompact, Previous() = %v, want compacting", got)
	}

	s = domain.Apply(s, poll(35, "busy"))
	if got := s.Previous(); got != domain.StateCompacting {
		t.Errorf("after a poll that changes nothing, Previous() = %v, want compacting", got)
	}
}

func metrics(costUSD float64) domain.Metrics {
	return domain.Metrics{
		Model: "Opus 5.5", ModelID: "claude-opus-5-5", Effort: "high",
		ContextPct: 42, ContextSize: 1_000_000, ContextUsed: 420_000,
		CostUSD: costUSD, DurationMS: 90_000, LinesAdded: 120, LinesRemoved: 30,
		CacheWarm: true, CacheHitRatio: 0.8,
	}
}

func TestApply_StatusLine(t *testing.T) {
	t.Run("records metrics, name, project and time", func(t *testing.T) {
		st := status(20)
		st.Name, st.ProjectDir, st.Metrics = "refactor", "/src/app", metrics(1.25)

		s := domain.Apply(fresh(), st)
		if s.Metrics != st.Metrics || s.Name != "refactor" || s.ProjectDir != "/src/app" || !s.LastStatusAt.Equal(at(20)) {
			t.Errorf("got metrics %+v, name %q, project %q, last status %s",
				s.Metrics, s.Name, s.ProjectDir, sinceT0(s.LastStatusAt))
		}
	})

	t.Run("keeps the known name and project when the line has none", func(t *testing.T) {
		first := status(10)
		first.Name, first.ProjectDir = "refactor", "/src/app"

		s := domain.Apply(domain.Apply(fresh(), first), status(20))
		if s.Name != "refactor" || s.ProjectDir != "/src/app" {
			t.Errorf("name %q, project %q; want refactor, /src/app", s.Name, s.ProjectDir)
		}
	})

	t.Run("an older status line does not replace a newer one", func(t *testing.T) {
		newer, older := status(20), status(10)
		newer.Metrics, older.Metrics = metrics(2), metrics(1)
		newer.Name, older.Name = "newer", "older"

		s := domain.Apply(domain.Apply(fresh(), newer), older)
		if s.Metrics != newer.Metrics || s.Name != "newer" || !s.LastStatusAt.Equal(at(20)) {
			t.Errorf("got cost %v, name %q, last status %s; want the newer line at 20s",
				s.Metrics.CostUSD, s.Name, sinceT0(s.LastStatusAt))
		}
	})

	t.Run("metrics update even when the state is newer", func(t *testing.T) {
		st := status(20)
		st.Metrics = metrics(1.25)

		s := domain.Apply(sessionIn(domain.StateWorking, 30), st)
		if s.Metrics != st.Metrics || !s.LastStatusAt.Equal(at(20)) {
			t.Errorf("got metrics %+v, last status %s", s.Metrics, sinceT0(s.LastStatusAt))
		}
	})

	t.Run("a status line from before the end changes nothing", func(t *testing.T) {
		st := status(20)
		st.Name, st.Metrics = "late", metrics(1.25)

		s := domain.Apply(endedAt(30), st)
		if s.Metrics != (domain.Metrics{}) || s.Name != "" || !s.LastStatusAt.IsZero() {
			t.Errorf("got metrics %+v, name %q, last status %s", s.Metrics, s.Name, sinceT0(s.LastStatusAt))
		}
	})
}

func TestApply_PollFills(t *testing.T) {
	t.Run("kind, process, name and project", func(t *testing.T) {
		p := liveBackground(20, "working", "busy")
		p.Name, p.Cwd = "nightly-triage", "/src/app"

		s := domain.Apply(fresh(), p)
		if s.Kind != domain.KindBackground || s.PID != pid || s.Name != "nightly-triage" || s.ProjectDir != "/src/app" {
			t.Errorf("kind %v, pid %d, name %q, project %q", s.Kind, s.PID, s.Name, s.ProjectDir)
		}
	})

	t.Run("keeps the known process and name when the poll has none", func(t *testing.T) {
		first := poll(10, "busy")
		first.Name = "refactor"

		s := domain.Apply(domain.Apply(fresh(), first), exited(20))
		if s.PID != pid || s.Name != "refactor" {
			t.Errorf("pid %d, name %q; want %d, refactor", s.PID, s.Name, pid)
		}
	})

	t.Run("cwd fills only an empty project", func(t *testing.T) {
		st := status(10)
		st.ProjectDir = "/src/app"
		p := poll(20, "busy")
		p.Cwd = "/src/app/internal"

		s := domain.Apply(domain.Apply(fresh(), st), p)
		if s.ProjectDir != "/src/app" {
			t.Errorf("project %q, want /src/app", s.ProjectDir)
		}
	})

	t.Run("an earlier process start replaces StartedAt", func(t *testing.T) {
		p := poll(20, "busy")
		p.StartedAt = at(-300)

		if got := domain.Apply(fresh(), p).StartedAt; !got.Equal(at(-300)) {
			t.Errorf("StartedAt %s, want -5m0s", sinceT0(got))
		}
	})

	t.Run("a later or missing process start keeps StartedAt", func(t *testing.T) {
		later := poll(20, "busy")
		later.StartedAt = at(5)

		s := domain.Apply(domain.Apply(fresh(), later), poll(25, "busy"))
		if !s.StartedAt.Equal(t0) {
			t.Errorf("StartedAt %s, want 0s", sinceT0(s.StartedAt))
		}
	})

	t.Run("a stale poll fills nothing", func(t *testing.T) {
		p := liveBackground(20, "working", "busy")
		p.Name, p.Cwd, p.StartedAt = "old", "/old", at(-300)

		s := domain.Apply(sessionIn(domain.StateWorking, 30), p)
		if s.Kind != domain.KindInteractive || s.PID != 0 || s.Name != "" || s.ProjectDir != "" || !s.StartedAt.Equal(t0) {
			t.Errorf("kind %v, pid %d, name %q, project %q, started %s",
				s.Kind, s.PID, s.Name, s.ProjectDir, sinceT0(s.StartedAt))
		}
	})
}

func TestApply_HookFillsPID(t *testing.T) {
	tests := []struct {
		name   string
		start  domain.Session
		events []domain.Event
		want   int
	}{
		{"a hook with a pid fills it", fresh(), []domain.Event{hook(10, "UserPromptSubmit")}, pid},
		{"a subagent hook fills it too", fresh(), []domain.Event{inSubagent(preToolUse(10, "Bash"))}, pid},
		{
			"a hook from a new process replaces it", fresh(),
			[]domain.Event{poll(5, "busy"), hookFrom(newPID, sessionStart(10, "resume"))},
			newPID,
		},
		{
			"a hook without a pid keeps the known one", fresh(),
			[]domain.Event{poll(5, "busy"), hookFrom(0, hook(10, "Stop"))},
			pid,
		},
		{"a stale hook fills nothing", sessionIn(domain.StateWorking, 30), []domain.Event{hook(20, "Stop")}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.start
			for _, e := range tc.events {
				s = domain.Apply(s, e)
			}
			if s.PID != tc.want {
				t.Errorf("PID = %d, want %d", s.PID, tc.want)
			}
		})
	}
}

// Fuzzing decodes the input into a sequence of small events, fuzzEventSize
// bytes each: event type, time in seconds after t0, two selectors, flags and
// a process start time. Times go up to 255 s and arrive in any order, so the
// sequences cover stale, duplicate and out-of-order delivery as well as both
// sides of the heartbeat grace.
const fuzzEventSize = 6

const (
	fuzzOnB = 1 << iota
	fuzzLiveProcess
	fuzzSubagent
	fuzzBackground
	fuzzNamed
	fuzzNewProcess
)

var (
	fuzzHooks = []string{
		"SessionStart", "UserPromptSubmit", "PermissionRequest", "Notification", "PreToolUse",
		"PostToolUse", "Stop", "StopFailure", "PreCompact", "PostCompact",
		"SubagentStart", "SubagentStop", "SessionEnd", "TeammateIdle",
	}
	fuzzNotifications = []string{
		"permission_prompt", "elicitation_dialog", "agent_needs_input", "idle_prompt", "auth_success", "",
	}
	fuzzTools      = []string{"AskUserQuestion", "Bash", ""}
	fuzzStatuses   = []string{"busy", "waiting", "idle", "paused", ""}
	fuzzBgStates   = []string{"working", "blocked", "done", "failed", "stopped", "queued", ""}
	fuzzWaitingFor = []string{"permission prompt", "input needed", ""}
	fuzzReasons    = []string{"clear", "resume", "logout", "prompt_input_exit", "other", ""}
)

type timedEvent struct {
	at    time.Time
	event domain.Event
}

func pick(options []string, b byte) string { return options[int(b)%len(options)] }

func fuzzEvents(data []byte) []timedEvent {
	events := make([]timedEvent, 0, len(data)/fuzzEventSize)
	for ; len(data) >= fuzzEventSize; data = data[fuzzEventSize:] {
		kind, when, first, second, flags, started := data[0], at(int(data[1])), data[2], data[3], data[4], data[5]

		dir := dirA
		if flags&fuzzOnB != 0 {
			dir = dirB
		}
		processID := 0
		switch {
		case flags&fuzzLiveProcess == 0:
		case flags&fuzzNewProcess != 0:
			processID = newPID
		default:
			processID = pid
		}
		name := ""
		if flags&fuzzNamed != 0 {
			name = "refactor"
		}

		var e domain.Event
		switch kind % 4 {
		case 0:
			h := domain.Hook{
				At: when, Dir: dir, Session: sid, PID: processID, Name: pick(fuzzHooks, first),
				Reason:           pick(fuzzReasons, second),
				NotificationType: pick(fuzzNotifications, second),
				ToolName:         pick(fuzzTools, second>>4),
			}
			if flags&fuzzSubagent != 0 {
				h.AgentID = "agent-7"
			}
			e = h
		case 1:
			p := domain.Poll{
				At: when, Dir: dir, Session: sid, PID: processID, Name: name,
				Status: pick(fuzzStatuses, first), WaitingFor: pick(fuzzWaitingFor, first>>4),
				BgState: pick(fuzzBgStates, second), Cwd: "/src/app",
			}
			if flags&fuzzBackground != 0 {
				p.Kind = domain.KindBackground
			}
			if started != 0 {
				p.StartedAt = at(int(started))
			}
			e = p
		case 2:
			e = domain.Absent{At: when, Dir: dir, Session: sid}
		default:
			e = domain.Status{At: when, Dir: dir, Session: sid, Name: name, Metrics: metrics(float64(first))}
		}
		events = append(events, timedEvent{at: when, event: e})
	}
	return events
}

func FuzzApply(f *testing.F) {
	// Seeds: a prompt answered and a late PostToolUse after SessionEnd; a
	// compaction with subagents; a lost session that a live poll brings back.
	f.Add([]byte{
		0, 70, 1, 0, 0, 0, 0, 80, 2, 0, 0, 0, 0, 100, 12, 4, 0, 0, 0, 90, 5, 0, 0, 0, 1, 100, 0, 0, fuzzLiveProcess, 0,
	})
	f.Add([]byte{
		0, 70, 10, 0, 0, 0, 0, 72, 10, 0, fuzzSubagent, 0, 0, 75, 8, 0, 0, 0,
		1, 76, 0, 0, fuzzLiveProcess, 30, 0, 80, 9, 0, 0, 0, 0, 82, 11, 0, fuzzSubagent, 0,
	})
	f.Add([]byte{3, 64, 0, 0, 0, 0, 2, 90, 0, 0, 0, 0, 2, 120, 0, 0, 0, 0, 1, 130, 2, 0, fuzzLiveProcess | fuzzOnB, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		s := domain.NewSession(sid, dirA, at(64))
		for _, te := range fuzzEvents(data) {
			next := domain.Apply(s, te.event)
			checkInvariants(t, s, te, next)
			s = next
		}
	})
}

func checkInvariants(t *testing.T, before domain.Session, te timedEvent, after domain.Session) {
	t.Helper()

	if before.State == domain.StateEnded && !te.at.After(before.EndedAt) {
		unchanged := before
		unchanged.LastSeenAt = after.LastSeenAt
		if !reflect.DeepEqual(after, unchanged) {
			t.Fatalf("%#v at or before the end changed\n%+v\ninto\n%+v", te.event, before, after)
		}
	}
	if after.LastSeenAt.Before(before.LastSeenAt) || after.StateAt.Before(before.StateAt) {
		t.Fatalf("%#v moved time back: LastSeenAt %s -> %s, StateAt %s -> %s", te.event,
			sinceT0(before.LastSeenAt), sinceT0(after.LastSeenAt), sinceT0(before.StateAt), sinceT0(after.StateAt))
	}
	if (after.State == domain.StateEnded) == after.EndedAt.IsZero() || (after.Lost && after.State != domain.StateEnded) {
		t.Fatalf("%#v left state %v with EndedAt %s, Lost %t", te.event, after.State, sinceT0(after.EndedAt), after.Lost)
	}
	if after.WaitReason != "" && after.State != domain.StateNeedsYou {
		t.Fatalf("%#v left state %v with wait reason %q", te.event, after.State, after.WaitReason)
	}
	if after.Subagents < 0 {
		t.Fatalf("%#v left %d subagents", te.event, after.Subagents)
	}
}
