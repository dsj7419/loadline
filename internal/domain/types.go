// Package domain holds Loadline's core types. It performs no I/O.
package domain

import (
	"fmt"
	"time"
)

// ConfigDir is the normalized absolute path of a Claude Code configuration
// directory. Loadline keys each account by it.
type ConfigDir string

// SessionID is a Claude Code session_id.
type SessionID string

// State is where a session stands, as the dashboard shows it.
type State uint8

const (
	// StateStarting is a session that is running but has not yet shown what
	// it is doing.
	StateStarting State = iota
	// StateWorking is a session running a turn.
	StateWorking
	// StateNeedsYou is a session blocked on the user: a permission prompt, a
	// question or a dialog. WaitReason says which.
	StateNeedsYou
	// StateIdle is a session waiting for the next prompt.
	StateIdle
	// StateCompacting is a session compacting its context.
	StateCompacting
	// StateError is a session whose last turn failed.
	StateError
	// StateParked is a background session with no live process that can be
	// resumed.
	StateParked
	// StateEnded is a session that has finished, or disappeared without a
	// trace (Lost).
	StateEnded
)

var stateNames = [...]string{
	StateStarting:   "starting",
	StateWorking:    "working",
	StateNeedsYou:   "needs_you",
	StateIdle:       "idle",
	StateCompacting: "compacting",
	StateError:      "error",
	StateParked:     "parked",
	StateEnded:      "ended",
}

// String returns the state's text form, or State(n) for a value outside the
// defined states.
func (s State) String() string {
	if int(s) < len(stateNames) {
		return stateNames[s]
	}
	return fmt.Sprintf("State(%d)", s)
}

// MarshalText implements encoding.TextMarshaler. It refuses a value outside
// the defined states, which could not be read back.
func (s State) MarshalText() ([]byte, error) {
	if int(s) >= len(stateNames) {
		return nil, fmt.Errorf("unknown session state %d", s)
	}
	return []byte(stateNames[s]), nil
}

// UnmarshalText implements encoding.TextUnmarshaler. Unknown text is an
// error and leaves s unchanged.
func (s *State) UnmarshalText(text []byte) error {
	for state := State(0); int(state) < len(stateNames); state++ {
		if stateNames[state] == string(text) {
			*s = state
			return nil
		}
	}
	return fmt.Errorf("unknown session state %q", text)
}

// Kind says how a session runs.
type Kind uint8

const (
	// KindInteractive is a session in a terminal.
	KindInteractive Kind = iota
	// KindBackground is a background agent session.
	KindBackground
)

var kindNames = [...]string{
	KindInteractive: "interactive",
	KindBackground:  "background",
}

// String returns the kind's text form, or Kind(n) for a value outside the
// defined kinds.
func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return fmt.Sprintf("Kind(%d)", k)
}

// MarshalText implements encoding.TextMarshaler. It refuses a value outside
// the defined kinds, which could not be read back.
func (k Kind) MarshalText() ([]byte, error) {
	if int(k) >= len(kindNames) {
		return nil, fmt.Errorf("unknown session kind %d", k)
	}
	return []byte(kindNames[k]), nil
}

// UnmarshalText implements encoding.TextUnmarshaler. Unknown text is an
// error and leaves k unchanged.
func (k *Kind) UnmarshalText(text []byte) error {
	for kind := Kind(0); int(kind) < len(kindNames); kind++ {
		if kindNames[kind] == string(text) {
			*k = kind
			return nil
		}
	}
	return fmt.Errorf("unknown session kind %q", text)
}

// Window is one rate-limit window.
type Window struct {
	UsedPct  float64
	ResetsAt time.Time
}

// Limits are an account's rate-limit windows as one status line reported
// them. A nil window was not reported.
type Limits struct {
	FiveHour, SevenDay *Window
	ObservedAt         time.Time
}

// Metrics are the figures a session's status line reports.
type Metrics struct {
	Model, ModelID, Effort   string
	ContextPct               float64
	ContextSize, ContextUsed int64
	CostUSD                  float64

	DurationMS, LinesAdded, LinesRemoved int64

	CacheWarm     bool
	CacheHitRatio float64
	FastMode      bool
}

// Session is everything Loadline knows about one Claude Code session.
type Session struct {
	ID         SessionID
	Dir        ConfigDir
	Kind       Kind
	PID        int
	Name       string
	ProjectDir string

	State, prev State
	// WaitReason says what a session in StateNeedsYou is waiting for.
	WaitReason string
	// EndReason is the SessionEnd reason, or "lost" for a session that
	// disappeared.
	EndReason string

	// StateAt is when State last changed.
	StateAt time.Time
	// StartedAt is when the session's process started, as far as Loadline
	// knows: the earliest of first sight and the poll's start time.
	StartedAt time.Time
	// LastSeenAt is the newest hook, poll or status line about the session.
	LastSeenAt time.Time
	// LastStatusAt is the newest status line.
	LastStatusAt time.Time
	// EndedAt is when the session ended; zero unless State is StateEnded.
	EndedAt time.Time

	// Lost marks a session that ended because it vanished from the poll
	// and its status line went quiet, rather than through SessionEnd.
	Lost bool
	// Subagents is the number of subagents running.
	Subagents int
	Metrics   Metrics
}

// Previous returns the state the session was in before its current one.
func (s Session) Previous() State { return s.prev }

// Transition records a session entering a state. The reducer doesn't keep
// them; whoever applies events records one each time State changes.
type Transition struct {
	At     time.Time
	State  State
	Reason string
}
