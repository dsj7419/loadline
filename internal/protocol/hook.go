package protocol

import (
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

// ReadHookInput decodes the first JSON value of r, which must be an object,
// into a HookInput. Every field is optional, and one holding a value of the
// wrong type is left empty. It does not cap the input: the decoder holds one
// value at a time, and nothing outside the allowlist outlives the call.
func ReadHookInput(r io.Reader) (HookInput, error) {
	// A pointer target tells a top-level null, which decodes to nil, from {}.
	var in *HookInput
	if err := ignoreFieldTypeError(json.NewDecoder(r).Decode(&in)); err != nil {
		return HookInput{}, fmt.Errorf("decode hook input: %w", err)
	}
	if in == nil {
		return HookInput{}, errors.New("decode hook input: the value is null, not an object")
	}
	return *in, nil
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
