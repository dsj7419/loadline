package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/dsj7419/loadline/internal/domain"
)

// HookInput is the part of a hook's JSON input that Loadline keeps. It is the
// privacy boundary for hooks: decoding into it drops every other field, among
// them the prompt, tool input and output, and messages, before anything is
// posted or spooled. Do not add a field without deciding that it can never
// carry content.
type HookInput struct {
	SessionID        string `json:"session_id,omitempty"`
	HookEventName    string `json:"hook_event_name,omitempty"`
	Source           string `json:"source,omitempty"`
	Reason           string `json:"reason,omitempty"`
	NotificationType string `json:"notification_type,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	AgentID          string `json:"agent_id,omitempty"`
	AgentType        string `json:"agent_type,omitempty"`
	PermissionMode   string `json:"permission_mode,omitempty"`
	Cwd              string `json:"cwd,omitempty"`
}

// ReadHookInput decodes the JSON object at the start of r into a HookInput,
// as encoding/json would, in memory that does not grow with the input. A
// hook's input can run to many megabytes of tool output, which a JSON
// decoder would hold whole, so the object is read as a stream instead: it
// holds a 16 KiB read buffer, the nesting of the value being read, at most
// 10 000 levels, and the members it keeps, at most MaxPayloadBytes each and
// 1 MiB in all. A longer member is checked and passed over unkept; as it
// could not be forwarded anyway, its field reads as empty. As in
// encoding/json, the last occurrence of a key, in any letter case, wins.
//
// Every field is optional, and one holding a value of the wrong type is
// left empty. Input that encoding/json would refuse, in a kept member or a
// passed-over one, is an error, and then nothing is returned. Reading stops
// at the object's closing brace.
func ReadHookInput(r io.Reader) (HookInput, error) {
	kept, err := shortMembers(bufio.NewReaderSize(r, hookReadBuffer))
	if err != nil {
		return HookInput{}, fmt.Errorf("decode hook input: %w", err)
	}
	var in HookInput
	if err := ignoreFieldTypeError(json.Unmarshal(kept, &in)); err != nil {
		return HookInput{}, fmt.Errorf("decode hook input: %w", err)
	}
	return in, nil
}

// HookEvent maps a hook payload to the domain event, stamped with the
// envelope's time, config dir and process ID.
func HookEvent(env Envelope, in HookInput) (domain.Hook, error) {
	if in.SessionID == "" {
		return domain.Hook{}, errors.New("hook input has no session_id")
	}
	return domain.Hook{
		At:               env.At,
		Dir:              domain.ConfigDir(env.ConfigDir),
		Session:          domain.SessionID(in.SessionID),
		PID:              env.PID,
		Name:             in.HookEventName,
		Source:           in.Source,
		Reason:           in.Reason,
		NotificationType: in.NotificationType,
		ToolName:         in.ToolName,
		AgentID:          in.AgentID,
	}, nil
}
