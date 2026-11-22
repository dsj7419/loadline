// Package store keeps what the tracker learns across restarts: accounts,
// sessions and their state changes, rate-limit samples and per-minute usage,
// in a SQLite database. It holds figures and names only, never a prompt, a
// response or anything a tool read or wrote.
//
// SQLite writes to the database through AsyncRecorder, which the tracker
// reports each change to, and Load gives the tracker back what a new run
// needs. Two things a session holds are not kept: the state it was in before
// its current one, and the time of its newest observation. A session
// restored mid-compaction therefore takes the state the next poll reports
// once the compaction ends, and its first events are judged stale against
// its last state change alone.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

// Times are kept as Unix milliseconds, UTC. Reading one back truncates it to
// the millisecond, so a restored time is never later than the one recorded
// and cannot make an event that followed it look stale.

// millis is t as a nullable column: an unset time is NULL.
func millis(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

// fromMillis reads a nullable time column.
func fromMillis(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return instant(n.Int64)
}

func instant(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// minuteOf is the start of the UTC minute t falls in, which usage is kept by.
func minuteOf(t time.Time) int64 { return t.Truncate(time.Minute).UnixMilli() }

// Rate-limit windows are named in limit_samples by these.
const (
	fiveHourWindow = "five_hour"
	sevenDayWindow = "seven_day"
)

// metricsColumn is how a session's metrics are kept in metrics_json. Its
// names are the stored format, so they stay fixed whatever the domain calls
// the fields. Rows that earlier versions wrote also hold durationMS,
// cacheWarm and fastMode, which decoding passes over.
type metricsColumn struct {
	Model         string  `json:"model"`
	ModelID       string  `json:"modelId"`
	Effort        string  `json:"effort"`
	ContextPct    float64 `json:"contextPct"`
	ContextSize   int64   `json:"contextSize"`
	ContextUsed   int64   `json:"contextUsed"`
	CostUSD       float64 `json:"costUSD"`
	LinesAdded    int64   `json:"linesAdded"`
	LinesRemoved  int64   `json:"linesRemoved"`
	CacheHitRatio float64 `json:"cacheHitRatio"`
}

func encodeMetrics(m domain.Metrics) (string, error) {
	data, err := json.Marshal(metricsColumn{
		Model: m.Model, ModelID: m.ModelID, Effort: m.Effort,
		ContextPct: m.ContextPct, ContextSize: m.ContextSize, ContextUsed: m.ContextUsed, CostUSD: m.CostUSD,
		LinesAdded: m.LinesAdded, LinesRemoved: m.LinesRemoved, CacheHitRatio: m.CacheHitRatio,
	})
	if err != nil {
		return "", fmt.Errorf("encode metrics: %w", err)
	}
	return string(data), nil
}

func decodeMetrics(text string) (domain.Metrics, error) {
	var c metricsColumn
	if err := json.Unmarshal([]byte(text), &c); err != nil {
		return domain.Metrics{}, fmt.Errorf("decode metrics: %w", err)
	}
	return domain.Metrics{
		Model: c.Model, ModelID: c.ModelID, Effort: c.Effort,
		ContextPct: c.ContextPct, ContextSize: c.ContextSize, ContextUsed: c.ContextUsed, CostUSD: c.CostUSD,
		LinesAdded: c.LinesAdded, LinesRemoved: c.LinesRemoved, CacheHitRatio: c.CacheHitRatio,
	}, nil
}

// subagentsColumn is how a session's subagent IDs are kept in subagent_ids.
type subagentsColumn struct {
	Running []string `json:"running"`
	Stopped []string `json:"stopped"`
}

func encodeSubagentIDs(ids domain.SubagentIDs) (string, error) {
	data, err := json.Marshal(subagentsColumn{Running: ids.Running, Stopped: ids.Stopped})
	if err != nil {
		return "", fmt.Errorf("encode subagent IDs: %w", err)
	}
	return string(data), nil
}

func decodeSubagentIDs(text string) (domain.SubagentIDs, error) {
	var c subagentsColumn
	if err := json.Unmarshal([]byte(text), &c); err != nil {
		return domain.SubagentIDs{}, fmt.Errorf("decode subagent IDs: %w", err)
	}
	return domain.SubagentIDs{Running: c.Running, Stopped: c.Stopped}, nil
}

// textOf is the text form of an enumeration value.
func textOf(v interface{ MarshalText() ([]byte, error) }) (string, error) {
	text, err := v.MarshalText()
	if err != nil {
		return "", err
	}
	return string(text), nil
}
