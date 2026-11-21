package protocol

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

// maxResetsAt is the last Unix second, 9999-12-31 23:59:59 UTC, that a
// time.Time can hold and still marshal to JSON.
const maxResetsAt = 253402300799

// StatusInput mirrors the documented status-line fields Loadline uses. Every
// field is optional, and JSON null reads as absent. Optional objects are
// pointers and every field is omitted when empty, so marshaling a decoded
// StatusInput leaves absent fields absent. Anything the status line carries
// beyond these fields, such as the transcript path, is not kept.
type StatusInput struct {
	SessionID   string `json:"session_id,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	Version     string `json:"version,omitempty"`
	FastMode    bool   `json:"fast_mode,omitempty"`

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
	ProjectDir string      `json:"project_dir,omitempty"`
	Repo       *StatusRepo `json:"repo,omitempty"`
}

// StatusRepo is the repository parsed from the origin remote. Claude Code
// omits it outside a git repository.
type StatusRepo struct {
	Host  string `json:"host,omitempty"`
	Owner string `json:"owner,omitempty"`
	Name  string `json:"name,omitempty"`
}

// StatusCost is what the session has cost so far.
type StatusCost struct {
	TotalCostUSD      float64 `json:"total_cost_usd,omitempty"`
	TotalDurationMS   int64   `json:"total_duration_ms,omitempty"`
	TotalLinesAdded   int64   `json:"total_lines_added,omitempty"`
	TotalLinesRemoved int64   `json:"total_lines_removed,omitempty"`
}

// StatusContextWindow is the context window's fill. Claude Code sends null
// percentages early in a session.
type StatusContextWindow struct {
	UsedPercentage      float64 `json:"used_percentage,omitempty"`
	RemainingPercentage float64 `json:"remaining_percentage,omitempty"`
	ContextWindowSize   int64   `json:"context_window_size,omitempty"`
	TotalInputTokens    int64   `json:"total_input_tokens,omitempty"`
}

// StatusPromptCache summarizes the prompt cache.
type StatusPromptCache struct {
	Warm     bool    `json:"warm,omitempty"`
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

// StatusSpendLimit is the spend limit behind a Claude apps gateway. Loadline
// decodes it but does not use it yet.
type StatusSpendLimit struct {
	UsedPercentage float64 `json:"used_percentage,omitempty"`
	// ResetsAt is in Unix seconds.
	ResetsAt int64   `json:"resets_at,omitempty"`
	UsedUSD  float64 `json:"used_usd,omitempty"`
	LimitUSD float64 `json:"limit_usd,omitempty"`
	// Period is daily, weekly or monthly.
	Period string `json:"period,omitempty"`
}

// DecodeStatus decodes a status-line payload. Fields it does not mirror are
// dropped.
func DecodeStatus(b []byte) (StatusInput, error) {
	var in StatusInput
	if err := json.Unmarshal(b, &in); err != nil {
		return StatusInput{}, fmt.Errorf("decode status input: %w", err)
	}
	return in, nil
}

// StatusEvent maps a status-line payload to the domain event, stamped with
// the envelope's time and config dir. The rate-limit windows are observed at
// the envelope's time; a window the payload lacks is nil.
func StatusEvent(env Envelope, in StatusInput) (domain.Status, error) {
	if in.SessionID == "" {
		return domain.Status{}, errors.New("status input has no session_id")
	}

	rateLimits := orZero(in.RateLimits)
	fiveHour, err := window(rateLimits.FiveHour)
	if err != nil {
		return domain.Status{}, fmt.Errorf("rate_limits.five_hour: %w", err)
	}
	sevenDay, err := window(rateLimits.SevenDay)
	if err != nil {
		return domain.Status{}, fmt.Errorf("rate_limits.seven_day: %w", err)
	}

	model, effort, cost := orZero(in.Model), orZero(in.Effort), orZero(in.Cost)
	contextWindow, promptCache := orZero(in.ContextWindow), orZero(in.PromptCache)
	return domain.Status{
		At:         env.At,
		Dir:        domain.ConfigDir(env.ConfigDir),
		Session:    domain.SessionID(in.SessionID),
		Name:       in.SessionName,
		ProjectDir: cmp.Or(orZero(in.Workspace).ProjectDir, in.Cwd),
		Metrics: domain.Metrics{
			Model:         model.DisplayName,
			ModelID:       model.ID,
			Effort:        effort.Level,
			ContextPct:    contextWindow.UsedPercentage,
			ContextSize:   contextWindow.ContextWindowSize,
			ContextUsed:   contextWindow.TotalInputTokens,
			CostUSD:       cost.TotalCostUSD,
			DurationMS:    cost.TotalDurationMS,
			LinesAdded:    cost.TotalLinesAdded,
			LinesRemoved:  cost.TotalLinesRemoved,
			CacheWarm:     promptCache.Warm,
			CacheHitRatio: promptCache.HitRatio,
			FastMode:      in.FastMode,
		},
		Limits: domain.Limits{
			FiveHour:   fiveHour,
			SevenDay:   sevenDay,
			ObservedAt: env.At,
		},
	}, nil
}

// window maps a reported window. One that is absent, or whose usage is
// unknown, is nil: unknown usage must not pass for 0%. A window without a
// reset time keeps its usage and has a zero ResetsAt.
func window(w *StatusWindow) (*domain.Window, error) {
	if w == nil || w.UsedPercentage == nil {
		return nil, nil
	}
	resetsAt, err := unixSeconds(w.ResetsAt)
	if err != nil {
		return nil, err
	}
	return &domain.Window{UsedPct: *w.UsedPercentage, ResetsAt: resetsAt}, nil
}

// unixSeconds converts a resets_at value. Zero means the payload gave none.
// A value no calendar date can hold is an error rather than a time that
// could not be stored.
func unixSeconds(seconds int64) (time.Time, error) {
	if seconds == 0 {
		return time.Time{}, nil
	}
	if seconds < 0 || seconds > maxResetsAt {
		return time.Time{}, fmt.Errorf("resets_at %d is outside the calendar", seconds)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

// orZero returns what p points to, or the zero value when p is nil.
func orZero[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
