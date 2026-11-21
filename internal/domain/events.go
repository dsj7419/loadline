package domain

import "time"

// Event is one observation about a session. The set is closed: Hook, Poll,
// Absent and Status, always passed by value.
type Event interface {
	isEvent()
}

// Hook is one Claude Code hook invocation. Name is hook_event_name; the
// other strings are the hook's source, reason, notification_type, tool_name
// and agent_id fields, empty when the payload has none. PID is the Claude
// process ID, zero when unknown.
type Hook struct {
	At      time.Time
	Dir     ConfigDir
	Session SessionID
	PID     int
	Name    string

	Source, Reason, NotificationType, ToolName, AgentID string
}

// Poll is the session's entry in `claude agents --json`. PID and Status
// (busy, waiting or idle) are set only while the process is alive;
// WaitingFor only while it waits; BgState only for background sessions.
type Poll struct {
	At      time.Time
	Dir     ConfigDir
	Session SessionID
	Kind    Kind
	PID     int

	Status, WaitingFor, BgState, Name, Cwd string

	StartedAt time.Time
}

// Absent reports that the poll of Dir no longer lists the session.
type Absent struct {
	At      time.Time
	Dir     ConfigDir
	Session SessionID
}

// Status is one status-line update.
type Status struct {
	At      time.Time
	Dir     ConfigDir
	Session SessionID

	Name, ProjectDir string

	Metrics Metrics
	Limits  Limits
}

func (Hook) isEvent()   {}
func (Poll) isEvent()   {}
func (Absent) isEvent() {}
func (Status) isEvent() {}
