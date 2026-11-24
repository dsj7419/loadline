package protocol

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

const (
	// maxResetsAt is the last Unix second, 9999-12-31 23:59:59 UTC, that a
	// time.Time can hold and still marshal to JSON.
	maxResetsAt = 253402300799
	// maxPercentage bounds a reported percentage. A reading can run a little
	// past 100, so the bound only keeps an absurd one out of the view and the
	// alert messages.
	maxPercentage = 1000
)

// StatusInput mirrors the documented status-line fields Loadline uses. Every
// field is optional, and JSON null reads as absent. Optional objects are
// pointers and every field is omitted when empty, so marshaling a decoded
// StatusInput leaves absent fields absent. Anything the status line carries
// beyond these fields, such as the transcript path, is not kept.
type StatusInput struct {
	SessionID   string `json:"session_id,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	Cwd         string `json:"cwd,omitempty"`

	Model         *StatusModel         `json:"model,omitempty"`
	Effort        *StatusEffort        `json:"effort,omitempty"`
	Workspace     *StatusWorkspace     `json:"workspace,omitempty"`
	Cost          *StatusCost          `json:"cost,omitempty"`
	ContextWindow *StatusContextWindow `json:"context_window,omitempty"`
	PromptCache   *StatusPromptCache   `json:"prompt_cache,omitempty"`
	RateLimits    *StatusRateLimits    `json:"rate_limits,omitempty"`
}

// StatusModel is the model that answered last.
type StatusModel struct {
	ID          string `json:"id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// StatusEffort is the session's reasoning effort. Claude Code omits it for
// models without one.
type StatusEffort struct {
	Level string `json:"level,omitempty"`
}

// StatusWorkspace says where the session works.
type StatusWorkspace struct {
	ProjectDir string `json:"project_dir,omitempty"`
}

// StatusCost is what the session has cost so far.
type StatusCost struct {
	TotalCostUSD       float64 `json:"total_cost_usd,omitempty"`
	TotalAPIDurationMS int64   `json:"total_api_duration_ms,omitempty"`
	TotalLinesAdded    int64   `json:"total_lines_added,omitempty"`
	TotalLinesRemoved  int64   `json:"total_lines_removed,omitempty"`
}

// StatusContextWindow is the context window's fill. Claude Code sends null
// percentages early in a session.
type StatusContextWindow struct {
	UsedPercentage    float64 `json:"used_percentage,omitempty"`
	ContextWindowSize int64   `json:"context_window_size,omitempty"`
	TotalInputTokens  int64   `json:"total_input_tokens,omitempty"`
}

// StatusPromptCache is how well the prompt cache serves the session.
type StatusPromptCache struct {
	HitRatio float64 `json:"hit_ratio,omitempty"`
}

// StatusRateLimits are the account's rate-limit windows. Each is absent when
// the account has no such limit or Claude Code has not learned it yet.
type StatusRateLimits struct {
	FiveHour   *StatusWindow     `json:"five_hour,omitempty"`
	SevenDay   *StatusWindow     `json:"seven_day,omitempty"`
	SpendLimit *StatusSpendLimit `json:"spend_limit,omitempty"`
}

// StatusWindow is one rate-limit window. UsedPercentage is a pointer so that
// a window without one is told apart from a window at 0%.
type StatusWindow struct {
	UsedPercentage *float64 `json:"used_percentage,omitempty"`
	// ResetsAt is in Unix seconds.
	ResetsAt int64 `json:"resets_at,omitempty"`
}

// UnmarshalJSON reads a window leniently. A percentage that is absent, null
// or not a number leaves UsedPercentage nil, which encoding/json cannot do on
// its own: it allocates the pointer before it finds the type wrong, and a
// percentage nobody reported would read as 0%. A reset time that is not a
// whole number of seconds is left at zero, and a window that is not an object
// stays empty.
func (w *StatusWindow) UnmarshalJSON(b []byte) error {
	var fields struct {
		UsedPercentage json.RawMessage `json:"used_percentage"`
		ResetsAt       json.RawMessage `json:"resets_at"`
	}
	_ = json.Unmarshal(b, &fields) // the mismatches this reports are the leniency above

	var used *float64
	if json.Unmarshal(fields.UsedPercentage, &used) == nil {
		w.UsedPercentage = used
	}
	var resetsAt int64
	if json.Unmarshal(fields.ResetsAt, &resetsAt) == nil {
		w.ResetsAt = resetsAt
	}
	return nil
}

// StatusSpendLimit is the spend limit behind a Claude apps gateway: what has
// been spent against it, and the limit. The amounts are pointers so that one
// the line leaves out is told apart from $0.
type StatusSpendLimit struct {
	UsedUSD  *float64 `json:"used_usd,omitempty"`
	LimitUSD *float64 `json:"limit_usd,omitempty"`
}

// UnmarshalJSON reads a spend limit leniently, as StatusWindow's does: an
// amount that is absent, null or not a number leaves its pointer nil, and a
// spend limit that is not an object stays empty.
func (s *StatusSpendLimit) UnmarshalJSON(b []byte) error {
	var fields struct {
		UsedUSD  json.RawMessage `json:"used_usd"`
		LimitUSD json.RawMessage `json:"limit_usd"`
	}
	_ = json.Unmarshal(b, &fields) // the mismatches this reports are the leniency above

	s.UsedUSD, s.LimitUSD = optionalNumber(fields.UsedUSD), optionalNumber(fields.LimitUSD)
	return nil
}

// optionalNumber is the number raw holds, or nil when it holds none.
func optionalNumber(raw json.RawMessage) *float64 {
	var n *float64
	if json.Unmarshal(raw, &n) != nil {
		return nil
	}
	return n
}

// DecodeStatus decodes a status-line payload, which must be a JSON object.
// Fields it does not mirror are dropped, and a field holding a value of the
// wrong type is left empty.
func DecodeStatus(b []byte) (StatusInput, error) {
	// A pointer target tells a top-level null, which decodes to nil, from {}.
	var in *StatusInput
	if err := ignoreFieldTypeError(json.Unmarshal(b, &in)); err != nil {
		return StatusInput{}, fmt.Errorf("decode status input: %w", err)
	}
	if in == nil {
		return StatusInput{}, errors.New("decode status input: the value is null, not an object")
	}
	return *in, nil
}

// StatusEvent maps a status-line payload to the domain event, stamped with
// the envelope's time, config dir and process ID. The rate-limit windows are
// observed at the envelope's time; a window the payload lacks is nil.
func StatusEvent(env Envelope, in StatusInput) (domain.Status, error) {
	if in.SessionID == "" {
		return domain.Status{}, errors.New("status input has no session_id")
	}

	rateLimits := orZero(in.RateLimits)
	model, effort, cost := orZero(in.Model), orZero(in.Effort), orZero(in.Cost)
	contextWindow, promptCache := orZero(in.ContextWindow), orZero(in.PromptCache)
	// An unknown context fill reads as 0%, as Claude Code's null does.
	contextPct, _ := percentage(contextWindow.UsedPercentage)
	return domain.Status{
		At:         env.At,
		Dir:        domain.ConfigDir(env.ConfigDir),
		Session:    domain.SessionID(in.SessionID),
		PID:        env.PID,
		Name:       in.SessionName,
		ProjectDir: cmp.Or(orZero(in.Workspace).ProjectDir, in.Cwd),
		Metrics: domain.Metrics{
			Model:         model.DisplayName,
			ModelID:       model.ID,
			Effort:        effort.Level,
			ContextPct:    contextPct,
			ContextSize:   contextWindow.ContextWindowSize,
			ContextUsed:   contextWindow.TotalInputTokens,
			CostUSD:       cost.TotalCostUSD,
			LinesAdded:    cost.TotalLinesAdded,
			LinesRemoved:  cost.TotalLinesRemoved,
			CacheHitRatio: promptCache.HitRatio,
		},
		Limits: domain.Limits{
			FiveHour:   window(rateLimits.FiveHour),
			SevenDay:   window(rateLimits.SevenDay),
			ObservedAt: env.At,
		},
		SpendLimit:    spendLimit(rateLimits.SpendLimit),
		APIDurationMS: cost.TotalAPIDurationMS,
	}, nil
}

// spendLimit maps a reported spend limit. One that lacks either amount, or
// whose amounts are negative or not finite, is nil: an amount nobody
// reported must not pass for $0.
func spendLimit(s *StatusSpendLimit) *domain.SpendLimit {
	if s == nil || !usableAmount(s.UsedUSD) || !usableAmount(s.LimitUSD) {
		return nil
	}
	return &domain.SpendLimit{UsedUSD: *s.UsedUSD, LimitUSD: *s.LimitUSD}
}

func usableAmount(usd *float64) bool {
	return usd != nil && !math.IsNaN(*usd) && !math.IsInf(*usd, 0) && *usd >= 0
}

// window maps a reported window. One that is absent, or whose usage is
// unknown or impossible, is nil: unknown usage must not pass for 0%. A window without a
// usable reset time keeps its usage and has a zero ResetsAt.
func window(w *StatusWindow) *domain.Window {
	if w == nil || w.UsedPercentage == nil {
		return nil
	}
	used, ok := percentage(*w.UsedPercentage)
	if !ok {
		return nil
	}
	return &domain.Window{UsedPct: used, ResetsAt: unixSeconds(w.ResetsAt)}
}

// percentage returns a reported percentage held to at most maxPercentage, or
// false for one that is negative or not a finite number, which is no reading
// at all.
func percentage(p float64) (float64, bool) {
	if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 {
		return 0, false
	}
	return min(p, maxPercentage), true
}

// unixSeconds converts a resets_at value. Zero, a negative number and a number
// no calendar date can hold, such as a time in milliseconds, give the zero
// time: the reset is unknown, and the line that reported it is still good.
func unixSeconds(seconds int64) time.Time {
	if seconds <= 0 || seconds > maxResetsAt {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

// orZero returns what p points to, or the zero value when p is nil.
func orZero[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
