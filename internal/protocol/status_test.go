package protocol_test

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/protocol"
)

const demoProjectDir = `C:\Users\example\projects\demo`

// fixtureResetsAt is the fake reset time in testdata/status_ultra.json, in
// Unix seconds: 2027-01-15 08:00:00 UTC.
const fixtureResetsAt = 1800000000

func decodeStatus(t *testing.T, data []byte) protocol.StatusInput {
	t.Helper()

	in, err := protocol.DecodeStatus(data)
	if err != nil {
		t.Fatalf("DecodeStatus: %v", err)
	}
	return in
}

func statusEvent(t *testing.T, payload string) domain.Status {
	t.Helper()

	event, err := protocol.StatusEvent(validEnvelope(t), decodeStatus(t, []byte(payload)))
	if err != nil {
		t.Fatalf("StatusEvent: %v", err)
	}
	return event
}

func TestDecodeStatus_UltraFixture(t *testing.T) {
	in := decodeStatus(t, fixture(t, "status_ultra.json"))

	if in.RateLimits == nil || in.RateLimits.SevenDay == nil {
		t.Fatalf("RateLimits = %+v, want a seven_day window", in.RateLimits)
	}
	if in.RateLimits.FiveHour != nil {
		t.Errorf("FiveHour = %+v, want nil: Ultra reports none", in.RateLimits.FiveHour)
	}
	sevenDay := in.RateLimits.SevenDay
	if sevenDay.UsedPercentage == nil || *sevenDay.UsedPercentage != 98 || sevenDay.ResetsAt != fixtureResetsAt {
		t.Errorf("SevenDay = %+v, want 98%% resetting at %d", sevenDay, fixtureResetsAt)
	}
	if in.ContextWindow == nil || in.ContextWindow.UsedPercentage != 68 {
		t.Errorf("ContextWindow = %+v, want 68%% used", in.ContextWindow)
	}
	if in.Model == nil || in.Model.DisplayName != "Opus 5.5" || in.Model.ID != "claude-opus-5-5" {
		t.Errorf("Model = %+v, want Opus 5.5", in.Model)
	}
	if in.SessionID != demoSession || in.SessionName != "demo-session" {
		t.Errorf("identity fields = %q, %q", in.SessionID, in.SessionName)
	}
}

func TestDecodeStatus_AllFieldsOptional(t *testing.T) {
	in, err := protocol.DecodeStatus([]byte(`{}`))
	if err != nil {
		t.Fatalf("DecodeStatus({}): %v", err)
	}

	if !reflect.DeepEqual(in, protocol.StatusInput{}) {
		t.Errorf("DecodeStatus({}) = %+v, want the zero value", in)
	}
}

func TestDecodeStatus_NullIsAbsent(t *testing.T) {
	tests := map[string]string{
		"early context":  `{"context_window":{"used_percentage":null,"remaining_percentage":null}}`,
		"no effort":      `{"effort":null}`,
		"no rate limits": `{"rate_limits":null}`,
		"no windows":     `{"rate_limits":{"five_hour":null,"seven_day":null,"spend_limit":null}}`,
		"null scalars": `{"session_name":null,"cwd":null,` +
			`"cost":{"total_cost_usd":null,"total_lines_added":null},"prompt_cache":{"hit_ratio":null}}`,
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			in, err := protocol.DecodeStatus([]byte(payload))
			if err != nil {
				t.Fatalf("DecodeStatus(%s): %v", payload, err)
			}

			event, err := protocol.StatusEvent(validEnvelope(t), withSession(in))
			if err != nil {
				t.Fatalf("StatusEvent: %v", err)
			}
			if event.Limits.FiveHour != nil || event.Limits.SevenDay != nil {
				t.Errorf("Limits = %+v, want no windows", event.Limits)
			}
			if event.Metrics != (domain.Metrics{}) {
				t.Errorf("Metrics = %+v, want none", event.Metrics)
			}
		})
	}
}

func withSession(in protocol.StatusInput) protocol.StatusInput {
	in.SessionID = demoSession
	return in
}

func TestDecodeStatus_NullContextPercentageIsZero(t *testing.T) {
	event := statusEvent(t, `{"session_id":"s","context_window":{"used_percentage":null,"context_window_size":200000}}`)

	if event.Metrics.ContextPct != 0 || event.Metrics.ContextSize != 200000 {
		t.Errorf("Metrics = %+v, want 0%% of 200000", event.Metrics)
	}
}

func TestDecodeStatus_ToleratesFloatPercentages(t *testing.T) {
	event := statusEvent(t, `{"session_id":"s","context_window":{"used_percentage":68.5},`+
		`"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":1738425600}}}`)

	if event.Metrics.ContextPct != 68.5 {
		t.Errorf("ContextPct = %v, want 68.5", event.Metrics.ContextPct)
	}
	if five := event.Limits.FiveHour; five == nil || five.UsedPct != 23.5 {
		t.Errorf("FiveHour = %+v, want 23.5%%", five)
	}
}

func TestDecodeStatus_IgnoresUndocumentedFields(t *testing.T) {
	in := decodeStatus(t, []byte(`{"session_id":"s","vim":{"mode":"NORMAL"},"agent":{"name":"x"},"future":[1,2]}`))

	if in.SessionID != "s" {
		t.Errorf("SessionID = %q, want s", in.SessionID)
	}
}

func TestDecodeStatus_RejectsUnreadableInput(t *testing.T) {
	for name, payload := range map[string]string{
		"empty":         ``,
		"truncated":     `{"session_id":"s"`,
		"not an object": `[]`,
		"null":          `null`,
		"a string":      `"session_id"`,
		"a number":      `42`,
		"a boolean":     `true`,
		"trailing data": `{"session_id":"s"}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			in, err := protocol.DecodeStatus([]byte(payload))

			if err == nil {
				t.Errorf("DecodeStatus(%q) = %+v, want an error", payload, in)
			}
		})
	}
}

func TestDecodeStatus_ToleratesMistypedFields(t *testing.T) {
	const wellFormed = `"model":{"display_name":"Opus 5.5"},` +
		`"rate_limits":{"seven_day":{"used_percentage":98,"resets_at":1800000000}}`

	for name, mistyped := range map[string]string{
		"string for a number":   `"context_window":{"used_percentage":"42"}`,
		"string for a counter":  `"cost":{"total_lines_added":"90","total_cost_usd":"free"}`,
		"number for a string":   `"session_name":7`,
		"number for an object":  `"effort":3`,
		"array for an object":   `"workspace":["/work"]`,
		"string for a ratio":    `"prompt_cache":{"hit_ratio":"high"}`,
		"number out of range":   `"context_window":{"context_window_size":1e30}`,
		"object for a string":   `"cwd":{"major":2}`,
		"mistyped nested field": `"workspace":{"project_dir":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			payload := `{"session_id":"s",` + mistyped + `,` + wellFormed + `}`

			in, err := protocol.DecodeStatus([]byte(payload))
			if err != nil {
				t.Fatalf("DecodeStatus(%s): %v", payload, err)
			}
			if in.SessionID != "s" || in.Model == nil || in.Model.DisplayName != "Opus 5.5" {
				t.Errorf("DecodeStatus(%s) lost the well-formed fields: %+v", payload, in)
			}
			sevenDay := in.RateLimits.SevenDay
			if sevenDay == nil || sevenDay.UsedPercentage == nil || *sevenDay.UsedPercentage != 98 ||
				sevenDay.ResetsAt != fixtureResetsAt {
				t.Errorf("DecodeStatus(%s) lost the seven-day window: %+v", payload, sevenDay)
			}
		})
	}
}

func TestDecodeStatus_MistypedFieldStaysZero(t *testing.T) {
	in := decodeStatus(t, []byte(`{"session_id":"s","session_name":7,`+
		`"context_window":{"used_percentage":"42","context_window_size":200000},`+
		`"rate_limits":{"five_hour":{"used_percentage":"half","resets_at":1800000000}}}`))

	if in.SessionName != "" {
		t.Errorf("SessionName = %q, want the mistyped field left empty", in.SessionName)
	}
	if in.ContextWindow == nil || in.ContextWindow.UsedPercentage != 0 || in.ContextWindow.ContextWindowSize != 200000 {
		t.Errorf("ContextWindow = %+v, want 0%% of 200000", in.ContextWindow)
	}
	event, err := protocol.StatusEvent(validEnvelope(t), in)
	if err != nil {
		t.Fatalf("StatusEvent: %v", err)
	}
	if event.Limits.FiveHour != nil {
		t.Errorf("FiveHour = %+v, want nil: a mistyped percentage is unknown, not 0%%", event.Limits.FiveHour)
	}
}

func TestDecodeStatus_MistypedWindowIsNotReported(t *testing.T) {
	event := statusEvent(t, `{"session_id":"s","rate_limits":{"five_hour":98,`+
		`"seven_day":{"used_percentage":40,"resets_at":1800000000}}}`)

	if event.Limits.FiveHour != nil {
		t.Errorf("FiveHour = %+v, want nil: the window was not an object", event.Limits.FiveHour)
	}
	if seven := event.Limits.SevenDay; seven == nil || seven.UsedPct != 40 {
		t.Errorf("SevenDay = %+v, want 40%%", seven)
	}
}

func TestStatusEvent_FractionalResetTimeKeepsUsage(t *testing.T) {
	event := statusEvent(t, `{"session_id":"s","rate_limits":{"five_hour":{"used_percentage":10,"resets_at":1800000000.5}}}`)

	window := event.Limits.FiveHour
	if window == nil || window.UsedPct != 10 || !window.ResetsAt.IsZero() {
		t.Errorf("FiveHour = %+v, want 10%% with no reset time: resets_at is whole seconds", window)
	}
}

func TestStatusInput_RemarshalKeepsAbsentFieldsAbsent(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := marshal(t, decodeStatus(t, []byte(`{}`))); got != `{}` {
			t.Errorf("Marshal = %s, want {}", got)
		}
	})

	t.Run("ultra fixture", func(t *testing.T) {
		out := marshal(t, decodeStatus(t, fixture(t, "status_ultra.json")))

		for _, dropped := range []string{
			"five_hour", "transcript", "scratchpad", "prompt_id", "output_style", "thinking", "repo",
			"added_dirs", "current_usage", "caching_observed",
		} {
			if strings.Contains(out, dropped) {
				t.Errorf("re-marshaled payload still contains %q: %s", dropped, out)
			}
		}
	})

	t.Run("round trip", func(t *testing.T) {
		first := decodeStatus(t, fixture(t, "status_ultra.json"))

		second := decodeStatus(t, []byte(marshal(t, first)))

		if !reflect.DeepEqual(first, second) {
			t.Errorf("a re-marshaled payload decodes differently\n first %+v\nsecond %+v", first, second)
		}
	})

	t.Run("zero window percentage stays", func(t *testing.T) {
		in := decodeStatus(t, []byte(`{"rate_limits":{"five_hour":{"used_percentage":0,"resets_at":1800000000}}}`))

		out := marshal(t, in)

		if !strings.Contains(out, `"used_percentage":0`) {
			t.Errorf("a window at 0%% lost its percentage: %s", out)
		}
	})
}

func TestStatusEvent_UltraFixture(t *testing.T) {
	env := validEnvelope(t)
	env.Kind = protocol.KindStatus
	in := decodeStatus(t, fixture(t, "status_ultra.json"))

	got, err := protocol.StatusEvent(env, in)
	if err != nil {
		t.Fatalf("StatusEvent: %v", err)
	}

	want := domain.Status{
		At:         envelopeTime,
		Dir:        domain.ConfigDir(env.ConfigDir),
		Session:    demoSession,
		Name:       "demo-session",
		ProjectDir: demoProjectDir,
		Metrics: domain.Metrics{
			Model:         "Opus 5.5",
			ModelID:       "claude-opus-5-5",
			Effort:        "xhigh",
			ContextPct:    68,
			ContextSize:   1000000,
			ContextUsed:   679604,
			CostUSD:       1146.6823081900031,
			LinesAdded:    1200,
			LinesRemoved:  340,
			CacheHitRatio: 0.990277427070115,
		},
		Limits: domain.Limits{
			SevenDay:   &domain.Window{UsedPct: 98, ResetsAt: time.Unix(fixtureResetsAt, 0).UTC()},
			ObservedAt: envelopeTime,
		},
		APIDurationMS: 2400000,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("StatusEvent()\n got %+v\nwant %+v", got, want)
	}
	if got.Limits.FiveHour != nil {
		t.Errorf("FiveHour = %+v, want nil", got.Limits.FiveHour)
	}
}

func TestStatusEvent_ResetsAtSeconds(t *testing.T) {
	event := statusEvent(t, `{"session_id":"s","rate_limits":{`+
		`"five_hour":{"used_percentage":10,"resets_at":1800000000},`+
		`"seven_day":{"used_percentage":20,"resets_at":1800086400}}}`)

	fiveHour, sevenDay := event.Limits.FiveHour, event.Limits.SevenDay
	if fiveHour == nil || sevenDay == nil {
		t.Fatalf("Limits = %+v, want both windows", event.Limits)
	}
	if want := time.Date(2027, time.January, 15, 8, 0, 0, 0, time.UTC); !fiveHour.ResetsAt.Equal(want) {
		t.Errorf("FiveHour.ResetsAt = %v, want %v: resets_at is in seconds", fiveHour.ResetsAt, want)
	}
	if want := time.Date(2027, time.January, 16, 8, 0, 0, 0, time.UTC); !sevenDay.ResetsAt.Equal(want) {
		t.Errorf("SevenDay.ResetsAt = %v, want %v", sevenDay.ResetsAt, want)
	}
	if !event.Limits.ObservedAt.Equal(envelopeTime) {
		t.Errorf("ObservedAt = %v, want the envelope time %v", event.Limits.ObservedAt, envelopeTime)
	}
}

func TestStatusEvent_WindowWithoutResetTimeKeepsUsage(t *testing.T) {
	event := statusEvent(t, `{"session_id":"s","rate_limits":{"seven_day":{"used_percentage":40}}}`)

	window := event.Limits.SevenDay
	if window == nil || window.UsedPct != 40 || !window.ResetsAt.IsZero() {
		t.Errorf("SevenDay = %+v, want 40%% with no reset time", window)
	}
}

func TestStatusEvent_WindowWithoutUsageIsNotReported(t *testing.T) {
	event := statusEvent(t, `{"session_id":"s","rate_limits":{`+
		`"five_hour":{"resets_at":1800000000},"seven_day":{"used_percentage":null,"resets_at":1800000000}}}`)

	if event.Limits.FiveHour != nil || event.Limits.SevenDay != nil {
		t.Errorf("Limits = %+v, want no windows: unknown usage is not 0%%", event.Limits)
	}
}

func TestStatusEvent_ZeroUsageIsReported(t *testing.T) {
	event := statusEvent(t, `{"session_id":"s","rate_limits":{"five_hour":{"used_percentage":0,"resets_at":1800000000}}}`)

	if five := event.Limits.FiveHour; five == nil || five.UsedPct != 0 {
		t.Errorf("FiveHour = %+v, want a window at 0%%", five)
	}
}

func TestStatusEvent_MapsTheSpendLimit(t *testing.T) {
	payload := []byte(`{"session_id":"s","rate_limits":{"spend_limit":{` +
		`"used_percentage":62.8,"resets_at":1740787200,"used_usd":314.12,"limit_usd":500,"period":"monthly"}}}`)

	in := decodeStatus(t, payload)

	spend := in.RateLimits.SpendLimit
	if spend == nil || spend.UsedUSD == nil || *spend.UsedUSD != 314.12 || spend.LimitUSD == nil || *spend.LimitUSD != 500 {
		t.Errorf("SpendLimit = %+v", spend)
	}
	event, err := protocol.StatusEvent(validEnvelope(t), in)
	if err != nil {
		t.Fatalf("StatusEvent: %v", err)
	}
	if want := (domain.SpendLimit{UsedUSD: 314.12, LimitUSD: 500}); event.SpendLimit == nil || *event.SpendLimit != want {
		t.Errorf("SpendLimit = %+v, want %+v", event.SpendLimit, want)
	}
	if event.Limits.FiveHour != nil || event.Limits.SevenDay != nil {
		t.Errorf("Limits = %+v, a spend limit is not a rate-limit window", event.Limits)
	}
}

func TestStatusEvent_MistypedSpendLimitFieldKeepsTheAmounts(t *testing.T) {
	event := statusEvent(t, `{"session_id":"s","rate_limits":{"spend_limit":`+
		`{"used_usd":1.5,"limit_usd":5,"period":7,"resets_at":"soon","used_percentage":"30"}}}`)

	if want := (domain.SpendLimit{UsedUSD: 1.5, LimitUSD: 5}); event.SpendLimit == nil || *event.SpendLimit != want {
		t.Errorf("SpendLimit = %+v, want %+v", event.SpendLimit, want)
	}
}

func TestStatusEvent_ZeroSpendIsReported(t *testing.T) {
	event := statusEvent(t, `{"session_id":"s","rate_limits":{"spend_limit":{"used_usd":0,"limit_usd":0}}}`)

	if event.SpendLimit == nil || *event.SpendLimit != (domain.SpendLimit{}) {
		t.Errorf("SpendLimit = %+v, want $0 of $0", event.SpendLimit)
	}
}

func TestStatusEvent_SpendLimitWithoutUsableFiguresIsNotReported(t *testing.T) {
	for name, spendLimit := range map[string]string{
		"absent":                  `{}`,
		"null":                    `null`,
		"not an object":           `500`,
		"a share alone":           `{"used_percentage":62.8,"period":"monthly"}`,
		"no amount spent":         `{"limit_usd":500}`,
		"no limit":                `{"used_usd":314.12}`,
		"null amounts":            `{"used_usd":null,"limit_usd":null}`,
		"text for an amount":      `{"used_usd":"314.12","limit_usd":500}`,
		"a negative amount spent": `{"used_usd":-1,"limit_usd":500}`,
		"a negative limit":        `{"used_usd":1,"limit_usd":-500}`,
		"an amount out of range":  `{"used_usd":1e400,"limit_usd":500}`,
	} {
		t.Run(name, func(t *testing.T) {
			payload := `{"session_id":"s","rate_limits":{"spend_limit":` + spendLimit +
				`,"seven_day":{"used_percentage":40,"resets_at":1800000000}}}`

			event := statusEvent(t, payload)

			if event.SpendLimit != nil {
				t.Errorf("SpendLimit = %+v, want none: unknown spend must not pass for $0", *event.SpendLimit)
			}
			if seven := event.Limits.SevenDay; seven == nil || seven.UsedPct != 40 {
				t.Errorf("SevenDay = %+v, want the window kept", seven)
			}
		})
	}
}

func TestStatusInput_RemarshalKeepsAReportedZeroSpend(t *testing.T) {
	in := decodeStatus(t, []byte(`{"rate_limits":{"spend_limit":{"used_usd":0,"limit_usd":50}}}`))

	got := marshal(t, in)

	if want := `{"rate_limits":{"spend_limit":{"used_usd":0,"limit_usd":50}}}`; got != want {
		t.Errorf("Marshal = %s, want %s", got, want)
	}
}

// The tap forwards only what the dashboard reads. The status line's version,
// repository, fast mode, total duration, remaining context and cache warmth,
// and its spend limit's share, reset time and period, stay behind with the
// fields Loadline never mirrored.
func TestStatusInput_ForwardsOnlyWhatTheDashboardReads(t *testing.T) {
	in := decodeStatus(t, []byte(`{
		"session_id": "s", "version": "2.1.330", "fast_mode": true,
		"workspace": {"project_dir": "/work/project", "repo": {"host": "h", "owner": "o", "name": "n"}},
		"cost": {"total_cost_usd": 1.5, "total_duration_ms": 90000},
		"context_window": {"used_percentage": 42.5, "remaining_percentage": 57.5},
		"prompt_cache": {"warm": true, "hit_ratio": 0.75},
		"rate_limits": {"spend_limit": {"used_percentage": 62.8, "resets_at": 1740787200,
			"used_usd": 314.12, "limit_usd": 500, "period": "monthly"}}
	}`))

	got := marshal(t, in)

	want := `{"session_id":"s","workspace":{"project_dir":"/work/project"},"cost":{"total_cost_usd":1.5},` +
		`"context_window":{"used_percentage":42.5},"prompt_cache":{"hit_ratio":0.75},` +
		`"rate_limits":{"spend_limit":{"used_usd":314.12,"limit_usd":500}}}`
	if got != want {
		t.Errorf("Marshal =\n%s\nwant\n%s", got, want)
	}
}

func TestStatusEvent_MapsEveryMetric(t *testing.T) {
	event := statusEvent(t, `{
		"session_id": "s",
		"session_name": "named",
		"cwd": "/work/cwd",
		"version": "2.1.330",
		"fast_mode": true,
		"model": {"id": "m-id", "display_name": "M"},
		"effort": {"level": "high"},
		"workspace": {"project_dir": "/work/project", "repo": {"host": "h", "owner": "o", "name": "n"}},
		"cost": {"total_cost_usd": 1.5, "total_duration_ms": 90000, "total_api_duration_ms": 30000,
			"total_lines_added": 12, "total_lines_removed": 3},
		"context_window": {"used_percentage": 42.5, "remaining_percentage": 57.5,
			"context_window_size": 200000, "total_input_tokens": 85000},
		"prompt_cache": {"warm": true, "hit_ratio": 0.75}
	}`)

	want := domain.Metrics{
		Model: "M", ModelID: "m-id", Effort: "high",
		ContextPct: 42.5, ContextSize: 200000, ContextUsed: 85000,
		CostUSD: 1.5, LinesAdded: 12, LinesRemoved: 3, CacheHitRatio: 0.75,
	}
	if event.Metrics != want {
		t.Errorf("Metrics\n got %+v\nwant %+v", event.Metrics, want)
	}
	if event.APIDurationMS != 30000 {
		t.Errorf("APIDurationMS = %d, want 30000", event.APIDurationMS)
	}
	if event.Name != "named" || event.ProjectDir != "/work/project" || event.Session != "s" {
		t.Errorf("identity = %q, %q, %q", event.Name, event.ProjectDir, event.Session)
	}
}

func TestStatusEvent_ProjectDirFallsBackToCwd(t *testing.T) {
	tests := map[string]struct {
		payload string
		want    string
	}{
		"workspace wins": {`{"session_id":"s","cwd":"/cwd","workspace":{"project_dir":"/project"}}`, "/project"},
		"no workspace":   {`{"session_id":"s","cwd":"/cwd"}`, "/cwd"},
		"empty project":  {`{"session_id":"s","cwd":"/cwd","workspace":{"project_dir":""}}`, "/cwd"},
		"neither":        {`{"session_id":"s"}`, ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := statusEvent(t, tt.payload).ProjectDir; got != tt.want {
				t.Errorf("ProjectDir = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusEvent_RequiresSessionID(t *testing.T) {
	_, err := protocol.StatusEvent(validEnvelope(t), protocol.StatusInput{SessionName: "orphan"})

	if err == nil {
		t.Error("StatusEvent() accepted an input without a session_id")
	}
}

func TestStatusEvent_UnusableResetTimeDegradesToUnknown(t *testing.T) {
	// 1800000000000 is a milliseconds value where seconds belong.
	for _, resetsAt := range []int64{-1, 253402300800, 1800000000000, math.MaxInt64, math.MinInt64} {
		for _, name := range []string{"five_hour", "seven_day"} {
			used := 50.0
			window := &protocol.StatusWindow{UsedPercentage: &used, ResetsAt: resetsAt}
			in := protocol.StatusInput{SessionID: "s", RateLimits: &protocol.StatusRateLimits{}}
			if name == "five_hour" {
				in.RateLimits.FiveHour = window
			} else {
				in.RateLimits.SevenDay = window
			}

			event, err := protocol.StatusEvent(validEnvelope(t), in)
			if err != nil {
				t.Fatalf("%s resets_at %d: StatusEvent: %v", name, resetsAt, err)
			}

			got := event.Limits.FiveHour
			if name == "seven_day" {
				got = event.Limits.SevenDay
			}
			if got == nil || got.UsedPct != 50 || !got.ResetsAt.IsZero() {
				t.Errorf("%s resets_at %d: window = %+v, want 50%% with no reset time", name, resetsAt, got)
			}
		}
	}
}

func TestStatusEvent_MillisecondResetTimeFromPayloadKeepsUsage(t *testing.T) {
	event := statusEvent(t, `{"session_id":"s","rate_limits":{`+
		`"seven_day":{"used_percentage":98,"resets_at":1800000000000}}}`)

	window := event.Limits.SevenDay
	if window == nil || window.UsedPct != 98 || !window.ResetsAt.IsZero() {
		t.Errorf("SevenDay = %+v, want 98%% with no reset time", window)
	}
}

func TestStatusEvent_EveryResetTimeCanBeStored(t *testing.T) {
	for _, resetsAt := range []int64{1, fixtureResetsAt, 253402300799} {
		used := 1.0
		in := protocol.StatusInput{
			SessionID:  "s",
			RateLimits: &protocol.StatusRateLimits{SevenDay: &protocol.StatusWindow{UsedPercentage: &used, ResetsAt: resetsAt}},
		}

		event, err := protocol.StatusEvent(validEnvelope(t), in)
		if err != nil {
			t.Fatalf("resets_at %d: StatusEvent: %v", resetsAt, err)
		}
		if _, err := json.Marshal(event.Limits.SevenDay.ResetsAt); err != nil {
			t.Errorf("resets_at %d does not marshal: %v", resetsAt, err)
		}
	}
}

func FuzzDecodeStatus(f *testing.F) {
	f.Add(fixture(f, "status_ultra.json"))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"context_window":{"used_percentage":null},"rate_limits":{"five_hour":{"used_percentage":1e400}}}`))
	f.Add([]byte(`{"rate_limits":{"seven_day":{"used_percentage":0,"resets_at":9223372036854775807}}}`))
	f.Add([]byte(`{"session_id":"s","cost":{"total_cost_usd":-0},"fast_mode":true}`))
	f.Add([]byte(`{"session_id":7,"effort":"high","rate_limits":{"seven_day":98,"five_hour":{"resets_at":1.5}}}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		in, err := protocol.DecodeStatus(data)
		if err != nil {
			return
		}

		// What tap forwards must decode to the same input on the server.
		again, err := protocol.DecodeStatus([]byte(marshal(t, in)))
		if err != nil || !reflect.DeepEqual(again, in) {
			t.Errorf("re-decoding %+v = %+v, %v", in, again, err)
		}

		// Only a missing session_id may stop a decoded input from becoming an
		// event, and what the event holds must be storable.
		event, err := protocol.StatusEvent(validEnvelope(t), in)
		if (err != nil) != (in.SessionID == "") {
			t.Errorf("StatusEvent(%+v) = %v, want an error exactly when session_id is empty", in, err)
		}
		if _, err := json.Marshal(event.Limits); err != nil {
			t.Errorf("limits do not marshal: %v", err)
		}
	})
}

// A percentage reaches the view and the alert messages as it was reported,
// so an absurd one is bounded on the way in. A reading can run a little past
// 100%, so only what no reading could be is held back.
func TestStatusEvent_BoundsPercentages(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pct     float64
		want    float64
		unknown bool
	}{
		{name: "an ordinary reading", pct: 42.5, want: 42.5},
		{name: "a reading a little past 100", pct: 103, want: 103},
		{name: "the bound", pct: 1000, want: 1000},
		{name: "past the bound", pct: 1e308, want: 1000},
		{name: "negative", pct: -5, unknown: true},
		{name: "infinite", pct: math.Inf(1), unknown: true},
		{name: "negative infinity", pct: math.Inf(-1), unknown: true},
		{name: "not a number", pct: math.NaN(), unknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// JSON holds no infinity or NaN, so the input is built rather than
			// decoded.
			pct := tc.pct
			in := protocol.StatusInput{
				SessionID:     "s",
				ContextWindow: &protocol.StatusContextWindow{UsedPercentage: pct},
				RateLimits: &protocol.StatusRateLimits{
					FiveHour: &protocol.StatusWindow{UsedPercentage: &pct, ResetsAt: fixtureResetsAt},
					SevenDay: &protocol.StatusWindow{UsedPercentage: &pct, ResetsAt: fixtureResetsAt},
				},
			}

			event, err := protocol.StatusEvent(validEnvelope(t), in)
			if err != nil {
				t.Fatalf("StatusEvent: %v", err)
			}

			wantContext := tc.want
			if tc.unknown {
				wantContext = 0
			}
			if event.Metrics.ContextPct != wantContext {
				t.Errorf("ContextPct = %v, want %v", event.Metrics.ContextPct, wantContext)
			}
			for name, w := range map[string]*domain.Window{"FiveHour": event.Limits.FiveHour, "SevenDay": event.Limits.SevenDay} {
				switch {
				case tc.unknown && w != nil:
					t.Errorf("%s = %+v, want no window: the usage is unknown", name, w)
				case !tc.unknown && (w == nil || w.UsedPct != tc.want):
					t.Errorf("%s = %+v, want %v%%", name, w, tc.want)
				}
			}
		})
	}
}

func TestStatusEvent_BoundsPercentagesFromTheWire(t *testing.T) {
	event := statusEvent(t, `{"session_id":"s","context_window":{"used_percentage":1e308},`+
		`"rate_limits":{"five_hour":{"used_percentage":-1,"resets_at":1800000000},"seven_day":{"used_percentage":1e308,"resets_at":1800000000}}}`)

	if event.Metrics.ContextPct != 1000 {
		t.Errorf("ContextPct = %v, want 1000", event.Metrics.ContextPct)
	}
	if event.Limits.FiveHour != nil {
		t.Errorf("FiveHour = %+v, want no window for a negative usage", event.Limits.FiveHour)
	}
	if seven := event.Limits.SevenDay; seven == nil || seven.UsedPct != 1000 {
		t.Errorf("SevenDay = %+v, want 1000%%", seven)
	}
}
