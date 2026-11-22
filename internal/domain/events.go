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
	// SpendLimit is the account's spend limit, when the line reports one.
	SpendLimit *SpendLimit
	// APIDurationMS is how long the session has spent on API calls, which
	// grows with each response it gets: a line on which it has grown carries
	// the rate limits of a response the session got since its last line.
	APIDurationMS int64
}

// ValidEventTime reports whether t can date an event: in UTC, after the Unix
// epoch and before year 10000. Every event Loadline takes in was stamped by a
// clock on this machine, so a time outside that span is a broken clock or a
// forged message, and one past year 9999 could not even be encoded as JSON.
func ValidEventTime(t time.Time) bool {
	t = t.UTC()
	return t.After(time.Unix(0, 0)) && t.Year() < 10000
}

func (Hook) isEvent()   {}
func (Poll) isEvent()   {}
func (Absent) isEvent() {}
func (Status) isEvent() {}
