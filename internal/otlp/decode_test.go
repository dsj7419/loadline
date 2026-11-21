package otlp_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/otlp"
)

const (
	// bodyLimit is generous for every body these tests build.
	bodyLimit = 1 << 20

	// fixtureSession is the fake session in testdata/api_request.json.
	fixtureSession domain.SessionID = "00000000-0000-4000-8000-000000000001"
)

// fixtureAt is the event.timestamp of the api_request record in
// testdata/api_request.json.
var fixtureAt = time.Date(2026, time.November, 21, 22, 23, 17, 642_000_000, time.UTC)

func fixture(tb testing.TB, name string) []byte {
	tb.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		tb.Fatalf("read fixture: %v", err)
	}
	return data
}

// Attribute builders. Each returns one OTLP KeyValue as JSON, in the value
// shapes Claude Code's exporter produces.

func attribute(key, valueJSON string) string {
	return fmt.Sprintf(`{"key":%q,"value":%s}`, key, valueJSON)
}

func strAttr(key, value string) string {
	return attribute(key, fmt.Sprintf(`{"stringValue":%q}`, value))
}

func intAttr(key string, value int64) string {
	return attribute(key, fmt.Sprintf(`{"intValue":%d}`, value))
}

// intStrAttr is an integer sent as a decimal string, as the OTLP/JSON
// mapping of int64 allows.
func intStrAttr(key string, value int64) string {
	return attribute(key, fmt.Sprintf(`{"intValue":"%d"}`, value))
}

// costAttr is a cost_usd attribute, which Claude Code sends as a doubleValue.
func costAttr(value float64) string {
	return attribute("cost_usd", fmt.Sprintf(`{"doubleValue":%v}`, value))
}

func attrs(list ...string) string {
	return "[" + strings.Join(list, ",") + "]"
}

// record is one log record: its body text, an optional timeUnixNano (a JSON
// value, so a test can send a string or a number) and its attributes.
func record(bodyText, timeUnixNano string, attributes ...string) string {
	fields := []string{`"attributes":` + attrs(attributes...)}
	if bodyText != "" {
		fields = append(fields, fmt.Sprintf(`"body":{"stringValue":%q}`, bodyText))
	}
	if timeUnixNano != "" {
		fields = append(fields, `"timeUnixNano":`+timeUnixNano)
	}
	return "{" + strings.Join(fields, ",") + "}"
}

// export wraps records in one resource and one scope, as Claude Code sends
// them.
func export(resourceAttributes string, records ...string) string {
	return fmt.Sprintf(
		`{"resourceLogs":[{"resource":{"attributes":%s},"scopeLogs":[{"logRecords":[%s]}]}]}`,
		resourceAttributes, strings.Join(records, ","),
	)
}

const (
	testSession = "11111111-2222-4333-8444-555555555555"
	testTime    = "2026-11-21T10:00:00.000Z"
)

// apiRequest is an api_request record with the attributes every test needs,
// followed by extra.
func apiRequest(extra ...string) string {
	base := []string{
		strAttr("event.name", "api_request"),
		strAttr("session.id", testSession),
		strAttr("event.timestamp", testTime),
	}
	return record("claude_code.api_request", "", append(base, extra...)...)
}

func decode(t *testing.T, body string) []domain.APIRequest {
	t.Helper()

	got, err := otlp.DecodeLogs(strings.NewReader(body), bodyLimit)
	if err != nil {
		t.Fatalf("DecodeLogs: %v", err)
	}
	return got
}

func decodeOne(t *testing.T, body string) domain.APIRequest {
	t.Helper()

	got := decode(t, body)
	if len(got) != 1 {
		t.Fatalf("DecodeLogs returned %d requests, want 1: %+v", len(got), got)
	}
	return got[0]
}

func TestDecodeLogs_RealFixture(t *testing.T) {
	got, err := otlp.DecodeLogs(bytes.NewReader(fixture(t, "api_request.json")), bodyLimit)
	if err != nil {
		t.Fatalf("DecodeLogs: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("DecodeLogs returned %d requests, want 1 (the user_prompt record is not one): %+v", len(got), got)
	}
	request := got[0]
	if !request.At.Equal(fixtureAt) {
		t.Errorf("At = %v, want %v", request.At, fixtureAt)
	}
	request.At = time.Time{}

	want := domain.APIRequest{
		Session:     fixtureSession,
		Email:       "user@example.com",
		Model:       "claude-opus-5-5",
		Input:       2,
		Output:      4,
		CacheRead:   17156,
		CacheWrite:  20819,
		CostUSD:     0.1700712,
		DurationMS:  2426,
		QuerySource: "sdk",
	}
	if request != want {
		t.Errorf("request = %+v, want %+v", request, want)
	}
}

func TestDecodeLogs_IgnoresOtherEvents(t *testing.T) {
	events := []struct {
		name string
		rec  string
	}{
		{"user_prompt", record("claude_code.user_prompt", "", strAttr("event.name", "user_prompt"), strAttr("session.id", testSession))},
		{"assistant_response", record("claude_code.assistant_response", "", strAttr("event.name", "assistant_response"), strAttr("session.id", testSession))},
		{"api_error", record("claude_code.api_error", "", strAttr("event.name", "api_error"), strAttr("session.id", testSession))},
		{"raw body event", record("claude_code.api_request_body", "", strAttr("event.name", "api_request_body"), strAttr("session.id", testSession))},
		{"prefixed event name", record("", "", strAttr("event.name", "claude_code.api_error"), strAttr("session.id", testSession))},
		{"no event name", record("", "", strAttr("session.id", testSession))},
		{"unrelated body", record("hello", "", strAttr("session.id", testSession))},
		{"empty record", `{}`},
	}

	for _, event := range events {
		t.Run(event.name, func(t *testing.T) {
			got := decode(t, export(attrs(), event.rec, apiRequest(strAttr("model", "m"))))

			if len(got) != 1 || got[0].Model != "m" {
				t.Errorf("DecodeLogs = %+v, want only the api_request record", got)
			}
		})
	}
}

func TestDecodeLogs_EventNameSources(t *testing.T) {
	tests := []struct {
		name string
		rec  string
		want bool
	}{
		{"attribute", record("", "", strAttr("event.name", "api_request"), strAttr("session.id", testSession), strAttr("event.timestamp", testTime)), true},
		{"prefixed attribute", record("", "", strAttr("event.name", "claude_code.api_request"), strAttr("session.id", testSession), strAttr("event.timestamp", testTime)), true},
		{"body only", record("claude_code.api_request", "", strAttr("session.id", testSession), strAttr("event.timestamp", testTime)), true},
		{"attribute wins over body", record("claude_code.api_request", "", strAttr("event.name", "user_prompt"), strAttr("session.id", testSession), strAttr("event.timestamp", testTime)), false},
		{"short name as body", record("api_request", "", strAttr("session.id", testSession), strAttr("event.timestamp", testTime)), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decode(t, export(attrs(), tt.rec))

			if (len(got) == 1) != tt.want {
				t.Errorf("DecodeLogs = %+v, want a request: %v", got, tt.want)
			}
		})
	}
}

func TestDecodeLogs_IntAsString(t *testing.T) {
	got := decodeOne(t, export(attrs(), apiRequest(
		intStrAttr("input_tokens", 11),
		intStrAttr("output_tokens", 22),
		intStrAttr("cache_read_tokens", 33),
		intStrAttr("cache_creation_tokens", 44),
		intStrAttr("duration_ms", 5555),
		intStrAttr("cost_usd_micros", 250_000),
	)))

	want := domain.APIRequest{
		Session: testSession, Input: 11, Output: 22, CacheRead: 33, CacheWrite: 44,
		DurationMS: 5555, CostUSD: 0.25,
	}
	got.At = time.Time{}
	if got != want {
		t.Errorf("request = %+v, want %+v", got, want)
	}
}

func TestDecodeLogs_ResourceAttributesFallback(t *testing.T) {
	resource := attrs(
		strAttr("session.id", "from-resource"),
		strAttr("user.email", "resource@example.com"),
		strAttr("model", "resource-model"),
		intAttr("input_tokens", 99),
		strAttr("query_source", "resource-source"),
	)
	rec := record("claude_code.api_request", "",
		strAttr("event.name", "api_request"),
		strAttr("event.timestamp", testTime),
		strAttr("model", "record-model"),
		intAttr("input_tokens", 7),
	)

	got := decodeOne(t, export(resource, rec))

	if got.Session != "from-resource" || got.Email != "resource@example.com" || got.QuerySource != "resource-source" {
		t.Errorf("request = %+v, want session, email and query source from the resource", got)
	}
	if got.Model != "record-model" || got.Input != 7 {
		t.Errorf("request = %+v, want the record's model and input tokens to win", got)
	}
}

func TestDecodeLogs_FirstDuplicateAttributeWins(t *testing.T) {
	resource := attrs(strAttr("model", "resource-first"), strAttr("model", "resource-second"), strAttr("user.email", "resource@example.com"))
	rec := apiRequest(strAttr("user.email", "record-first@example.com"), strAttr("user.email", "record-second@example.com"))

	got := decodeOne(t, export(resource, rec))

	if got.Model != "resource-first" || got.Email != "record-first@example.com" {
		t.Errorf("request = %+v, want the first of each duplicated attribute", got)
	}
}

func TestDecodeLogs_StringAttributes(t *testing.T) {
	got := decodeOne(t, export(attrs(), apiRequest(
		strAttr("user.email", "dev@example.com"),
		strAttr("model", "claude-haiku-5"),
		strAttr("query_source", "agent:custom"),
		strAttr("agent.name", "Explore"),
	)))

	if got.Email != "dev@example.com" || got.Model != "claude-haiku-5" || got.QuerySource != "agent:custom" || got.AgentName != "Explore" {
		t.Errorf("request = %+v, want email, model, query source and agent name set", got)
	}
}

func TestDecodeLogs_OptionalFieldsAbsent(t *testing.T) {
	got := decodeOne(t, export(attrs(), apiRequest()))

	want := domain.APIRequest{Session: testSession}
	got.At = time.Time{}
	if got != want {
		t.Errorf("request = %+v, want only the session", got)
	}
}

func TestDecodeLogs_Timestamp(t *testing.T) {
	const wantAt = "2026-11-21T22:23:17.642Z"
	nanos := fmt.Sprint(fixtureAt.UnixNano())
	quotedNanos := `"` + nanos + `"`

	tests := []struct {
		name           string
		timeUnixNano   string // JSON value, or "" for none
		eventTimestamp string // attribute JSON, or "" for none
		want           string // RFC 3339, or "" when the record is skipped
	}{
		{name: "event.timestamp", eventTimestamp: strAttr("event.timestamp", wantAt), want: wantAt},
		{
			name:           "event.timestamp with offset is normalized to UTC",
			eventTimestamp: strAttr("event.timestamp", "2026-11-21T17:23:17.642-05:00"),
			want:           wantAt,
		},
		{
			name:           "event.timestamp wins over timeUnixNano",
			timeUnixNano:   `"1000000000"`,
			eventTimestamp: strAttr("event.timestamp", wantAt),
			want:           wantAt,
		},
		{name: "timeUnixNano as a string", timeUnixNano: quotedNanos, want: wantAt},
		{name: "timeUnixNano as a number", timeUnixNano: nanos, want: wantAt},
		{
			name:           "unparsable event.timestamp falls back to timeUnixNano",
			timeUnixNano:   quotedNanos,
			eventTimestamp: strAttr("event.timestamp", "yesterday"),
			want:           wantAt,
		},
		{
			name:           "zero event.timestamp falls back to timeUnixNano",
			timeUnixNano:   quotedNanos,
			eventTimestamp: strAttr("event.timestamp", "0001-01-01T00:00:00Z"),
			want:           wantAt,
		},
		{
			name:           "event.timestamp of the wrong type falls back to timeUnixNano",
			timeUnixNano:   quotedNanos,
			eventTimestamp: intAttr("event.timestamp", 5),
			want:           wantAt,
		},
		{
			name:           "event.timestamp before the epoch falls back to timeUnixNano",
			timeUnixNano:   quotedNanos,
			eventTimestamp: strAttr("event.timestamp", "1969-12-31T23:59:59Z"),
			want:           wantAt,
		},
		{
			name:           "event.timestamp at the epoch falls back to timeUnixNano",
			timeUnixNano:   quotedNanos,
			eventTimestamp: strAttr("event.timestamp", "1970-01-01T00:00:00Z"),
			want:           wantAt,
		},
		{
			name:           "event.timestamp before year 1 once in UTC falls back to timeUnixNano",
			timeUnixNano:   quotedNanos,
			eventTimestamp: strAttr("event.timestamp", "0000-01-01T00:00:00+05:00"),
			want:           wantAt,
		},
		{
			name:           "event.timestamp past year 9999 once in UTC falls back to timeUnixNano",
			timeUnixNano:   quotedNanos,
			eventTimestamp: strAttr("event.timestamp", "9999-12-31T23:59:59-05:00"),
			want:           wantAt,
		},
		{
			name:           "latest event.timestamp that can be stored",
			eventTimestamp: strAttr("event.timestamp", "9999-12-31T23:59:59Z"),
			want:           "9999-12-31T23:59:59Z",
		},
		{name: "first instant after the epoch", timeUnixNano: `"1"`, want: "1970-01-01T00:00:00.000000001Z"},
		{name: "timeUnixNano at the integer maximum", timeUnixNano: `"9223372036854775807"`, want: "2262-04-11T23:47:16.854775807Z"},
		{name: "no time at all skips the record"},
		{name: "timeUnixNano zero means unset", timeUnixNano: `"0"`},
		{name: "negative timeUnixNano skips the record", timeUnixNano: `"-1"`},
		{
			name:           "event.timestamp before year 1 once in UTC and no fallback skips the record",
			eventTimestamp: strAttr("event.timestamp", "0000-01-01T00:00:00+05:00"),
		},
		{
			name:           "event.timestamp past year 9999 once in UTC and no fallback skips the record",
			eventTimestamp: strAttr("event.timestamp", "9999-12-31T23:59:59-05:00"),
		},
		{
			name:           "event.timestamp before the epoch and no fallback skips the record",
			eventTimestamp: strAttr("event.timestamp", "1969-12-31T23:59:59Z"),
		},
		{
			name:           "unparsable times skip the record",
			timeUnixNano:   `"soon"`,
			eventTimestamp: strAttr("event.timestamp", "later"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attributes := []string{strAttr("session.id", testSession)}
			if tt.eventTimestamp != "" {
				attributes = append(attributes, tt.eventTimestamp)
			}

			got := decode(t, export(attrs(), record("claude_code.api_request", tt.timeUnixNano, attributes...)))

			if tt.want == "" {
				if len(got) != 0 {
					t.Errorf("DecodeLogs = %+v, want the record skipped", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("DecodeLogs returned %d requests, want 1", len(got))
			}
			want, err := time.Parse(time.RFC3339Nano, tt.want)
			if err != nil {
				t.Fatalf("bad test time: %v", err)
			}
			if !got[0].At.Equal(want) {
				t.Errorf("At = %v, want %v", got[0].At, want)
			}
			if got[0].At.Location() != time.UTC {
				t.Errorf("At location = %v, want UTC", got[0].At.Location())
			}
			if _, err := json.Marshal(got[0].At); err != nil {
				t.Errorf("At %v does not marshal: %v", got[0].At, err)
			}
		})
	}
}

func TestDecodeLogs_Cost(t *testing.T) {
	tests := []struct {
		name  string
		extra []string
		want  float64
	}{
		{"cost_usd", []string{costAttr(0.1700712)}, 0.1700712},
		{"cost_usd wins over micros", []string{costAttr(0.5), intAttr("cost_usd_micros", 250_000)}, 0.5},
		{"micros fallback", []string{intAttr("cost_usd_micros", 170_071)}, 0.170071},
		{"micros as string", []string{intStrAttr("cost_usd_micros", 1_500_000)}, 1.5},
		{"whole-dollar cost arrives as an int", []string{intAttr("cost_usd", 2)}, 2},
		{"zero cost arrives as an int", []string{intAttr("cost_usd", 0), intAttr("cost_usd_micros", 5)}, 0},
		{"cost_usd as a string value is not a number", []string{strAttr("cost_usd", "0.25")}, 0},
		{"unusable cost_usd falls back to micros", []string{attribute("cost_usd", `{"doubleValue":"free"}`), intAttr("cost_usd_micros", 42)}, 0.000042},
		{"infinite cost is dropped", []string{attribute("cost_usd", `{"doubleValue":"Infinity"}`), intAttr("cost_usd_micros", 42)}, 0.000042},
		{"NaN cost is dropped", []string{attribute("cost_usd", `{"doubleValue":"NaN"}`)}, 0},
		{"cost_usd at the cap", []string{costAttr(1_000_000)}, 1_000_000},
		{"cost_usd over the cap falls back to micros", []string{costAttr(1_000_000.01), intAttr("cost_usd_micros", 42)}, 0.000042},
		{"cost_usd over the cap", []string{costAttr(1_000_000.01)}, 0},
		{"micros at the cap", []string{intAttr("cost_usd_micros", 1_000_000_000_000)}, 1_000_000},
		{"micros over the cap", []string{intAttr("cost_usd_micros", 1_000_000_000_001)}, 0},
		{"negative cost clamps to zero", []string{costAttr(-3)}, 0},
		{"negative micros clamp to zero", []string{intAttr("cost_usd_micros", -3)}, 0},
		{"no cost", nil, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeOne(t, export(attrs(), apiRequest(tt.extra...)))

			if math.Abs(got.CostUSD-tt.want) > 1e-12 {
				t.Errorf("CostUSD = %v, want %v", got.CostUSD, tt.want)
			}
		})
	}
}

func TestDecodeLogs_NegativeCountsClampToZero(t *testing.T) {
	got := decodeOne(t, export(attrs(), apiRequest(
		intAttr("input_tokens", -1),
		intAttr("output_tokens", -2),
		intAttr("cache_read_tokens", -3),
		intStrAttr("cache_creation_tokens", -4),
		intAttr("duration_ms", -5),
	)))

	if got.Input != 0 || got.Output != 0 || got.CacheRead != 0 || got.CacheWrite != 0 || got.DurationMS != 0 {
		t.Errorf("request = %+v, want every negative count clamped to 0", got)
	}
}

func TestDecodeLogs_CountsAboveTheCapReadAsAbsent(t *testing.T) {
	const maxCount = 1 << 40

	tests := []struct {
		name string
		attr func(key string, value int64) string
	}{
		{"number", intAttr},
		{"string", intStrAttr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atCap := decodeOne(t, export(attrs(), apiRequest(
				tt.attr("input_tokens", maxCount),
				tt.attr("output_tokens", maxCount),
				tt.attr("cache_read_tokens", maxCount),
				tt.attr("cache_creation_tokens", maxCount),
				tt.attr("duration_ms", maxCount),
			)))
			if atCap.Input != maxCount || atCap.Output != maxCount || atCap.CacheRead != maxCount || atCap.CacheWrite != maxCount || atCap.DurationMS != maxCount {
				t.Errorf("request = %+v, want every count at the cap kept", atCap)
			}

			overCap := decodeOne(t, export(attrs(), apiRequest(
				tt.attr("input_tokens", maxCount+1),
				tt.attr("output_tokens", maxCount+1),
				tt.attr("cache_read_tokens", maxCount+1),
				tt.attr("cache_creation_tokens", maxCount+1),
				tt.attr("duration_ms", maxCount+1),
			)))
			if overCap.Input != 0 || overCap.Output != 0 || overCap.CacheRead != 0 || overCap.CacheWrite != 0 || overCap.DurationMS != 0 {
				t.Errorf("request = %+v, want every count over the cap read as 0", overCap)
			}
		})
	}
}

func TestDecodeLogs_NumbersAreNotReadFromStringValues(t *testing.T) {
	got := decodeOne(t, export(attrs(), apiRequest(
		strAttr("input_tokens", "11"),
		strAttr("output_tokens", "22"),
		strAttr("cache_read_tokens", "33"),
		strAttr("cache_creation_tokens", "44"),
		strAttr("duration_ms", "55"),
		strAttr("cost_usd_micros", "66"),
	)))

	want := domain.APIRequest{Session: testSession}
	got.At = time.Time{}
	if got != want {
		t.Errorf("request = %+v, want every string-typed number left at zero", got)
	}
}

func TestDecodeLogs_EventNameComesFromTheRecordOnly(t *testing.T) {
	resourceNamed := attrs(strAttr("event.name", "api_request"))
	own := []string{strAttr("session.id", testSession), strAttr("event.timestamp", testTime)}
	tests := []struct {
		name string
		rec  string
	}{
		{"another event named on the record", record("claude_code.user_prompt", "", append([]string{strAttr("event.name", "user_prompt")}, own...)...)},
		{"unnamed record", record("", "", own...)},
		{"record with an unrelated body", record("hello", "", own...)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decode(t, export(resourceNamed, tt.rec)); len(got) != 0 {
				t.Errorf("DecodeLogs = %+v, want none: a resource's event.name does not name its records", got)
			}
		})
	}
}

func TestDecodeLogs_EventTimeComesFromTheRecordOnly(t *testing.T) {
	rec := record("claude_code.api_request", "", strAttr("session.id", testSession))

	got := decode(t, export(attrs(strAttr("event.timestamp", testTime)), rec))

	if len(got) != 0 {
		t.Errorf("DecodeLogs = %+v, want none: a resource's event.timestamp is not any record's time", got)
	}
}

func TestDecodeLogs_UnusableNumbersReadAsAbsent(t *testing.T) {
	got := decodeOne(t, export(attrs(), apiRequest(
		strAttr("input_tokens", "many"),
		attribute("output_tokens", `{"intValue":"9223372036854775808"}`),
		attribute("cache_read_tokens", `{"intValue":1.5}`),
		attribute("cache_creation_tokens", `{"intValue":true}`),
		attribute("duration_ms", `{"intValue":null}`),
		intAttr("model", 7),
	)))

	want := domain.APIRequest{Session: testSession}
	got.At = time.Time{}
	if got != want {
		t.Errorf("request = %+v, want every unusable field left at zero", got)
	}
}

func TestDecodeLogs_SessionRequired(t *testing.T) {
	tests := []struct {
		name string
		rec  string
	}{
		{"missing", record("claude_code.api_request", "", strAttr("event.timestamp", testTime))},
		{"empty", record("claude_code.api_request", "", strAttr("session.id", ""), strAttr("event.timestamp", testTime))},
		{"wrong type", record("claude_code.api_request", "", intAttr("session.id", 5), strAttr("event.timestamp", testTime))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decode(t, export(attrs(), tt.rec, apiRequest(strAttr("model", "kept"))))

			if len(got) != 1 || got[0].Model != "kept" {
				t.Errorf("DecodeLogs = %+v, want only the record with a session", got)
			}
		})
	}
}

func TestDecodeLogs_ManyRecordsInOrder(t *testing.T) {
	first := fmt.Sprintf(`{"resource":{"attributes":%s},"scopeLogs":[{"logRecords":[%s,%s]},{"logRecords":[%s]}]}`,
		attrs(strAttr("model", "from-first-resource")),
		apiRequest(intAttr("input_tokens", 1)),
		apiRequest(intAttr("input_tokens", 2)),
		apiRequest(intAttr("input_tokens", 3)),
	)
	second := fmt.Sprintf(`{"scopeLogs":[{"logRecords":[%s]}]}`, apiRequest(intAttr("input_tokens", 4)))

	got := decode(t, `{"resourceLogs":[`+first+`,`+second+`]}`)

	if len(got) != 4 {
		t.Fatalf("DecodeLogs returned %d requests, want 4: %+v", len(got), got)
	}
	for i, request := range got {
		if request.Input != int64(i+1) {
			t.Errorf("request %d has Input %d, want %d", i, request.Input, i+1)
		}
	}
	if got[0].Model != "from-first-resource" || got[3].Model != "" {
		t.Errorf("models = %q, %q: a resource's attributes must reach only its own records", got[0].Model, got[3].Model)
	}
}

func TestDecodeLogs_EmptyExports(t *testing.T) {
	bodies := map[string]string{
		"empty object":      `{}`,
		"null":              `null`,
		"no resource logs":  `{"resourceLogs":[]}`,
		"null resource log": `{"resourceLogs":[null]}`,
		"no scope logs":     `{"resourceLogs":[{"resource":{}}]}`,
		"no records":        `{"resourceLogs":[{"scopeLogs":[{"logRecords":[]}]}]}`,
		"null record":       `{"resourceLogs":[{"scopeLogs":[{"logRecords":[null]}]}]}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			if got := decode(t, body); len(got) != 0 {
				t.Errorf("DecodeLogs = %+v, want none", got)
			}
		})
	}
}

func TestDecodeLogs_OddAttributeShapesDoNotFailTheExport(t *testing.T) {
	rec := record("claude_code.api_request", "",
		strAttr("event.name", "api_request"),
		strAttr("session.id", testSession),
		strAttr("event.timestamp", testTime),
		`{"key":"model","value":"not an object"}`,
		`{"key":"input_tokens","value":null}`,
		`{"key":"output_tokens"}`,
		attribute("cache_read_tokens", `{"arrayValue":{"values":[]}}`),
		attribute("user.email", `{"kvlistValue":{"values":[]}}`),
		intAttr("duration_ms", 12),
	)

	got := decodeOne(t, export(attrs(), rec))

	if got.Session != testSession || got.DurationMS != 12 || got.Model != "" || got.Email != "" {
		t.Errorf("request = %+v, want the usable fields kept and the odd ones absent", got)
	}
}

func TestDecodeLogs_MalformedJSON(t *testing.T) {
	bodies := map[string]string{
		"empty body":          ``,
		"whitespace":          "  \n",
		"not json":            `hello`,
		"truncated":           `{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"attributes":[`,
		"trailing garbage":    `{} {}`,
		"top level array":     `[]`,
		"top level string":    `"x"`,
		"resource logs typed": `{"resourceLogs":5}`,
		"scope logs typed":    `{"resourceLogs":[{"scopeLogs":"x"}]}`,
		"attributes typed":    `{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"attributes":{}}]}]}]}`,
		"key typed":           `{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"attributes":[{"key":5}]}]}]}]}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			got, err := otlp.DecodeLogs(strings.NewReader(body), bodyLimit)

			if err == nil {
				t.Fatalf("DecodeLogs = %+v, want an error", got)
			}
			if errors.Is(err, otlp.ErrTooLarge) {
				t.Errorf("error %v reports an oversized body, want a decode error", err)
			}
			if got != nil {
				t.Errorf("DecodeLogs returned %+v with an error, want none", got)
			}
		})
	}
}

func TestDecodeLogs_OverLimit(t *testing.T) {
	body := export(attrs(), apiRequest(strAttr("model", "m")))
	size := int64(len(body))

	t.Run("exactly at the limit", func(t *testing.T) {
		got, err := otlp.DecodeLogs(strings.NewReader(body), size)

		if err != nil || len(got) != 1 {
			t.Errorf("DecodeLogs = %+v, %v, want the request", got, err)
		}
	})

	t.Run("one byte over", func(t *testing.T) {
		got, err := otlp.DecodeLogs(strings.NewReader(body), size-1)

		if !errors.Is(err, otlp.ErrTooLarge) {
			t.Errorf("error = %v, want ErrTooLarge", err)
		}
		if got != nil {
			t.Errorf("DecodeLogs returned %+v with an error, want none", got)
		}
	})

	t.Run("nothing fits a negative limit", func(t *testing.T) {
		if _, err := otlp.DecodeLogs(strings.NewReader(`{}`), -1); !errors.Is(err, otlp.ErrTooLarge) {
			t.Errorf("error = %v, want ErrTooLarge", err)
		}
	})

	t.Run("limit at the integer maximum", func(t *testing.T) {
		got, err := otlp.DecodeLogs(strings.NewReader(body), math.MaxInt64)

		if err != nil || len(got) != 1 {
			t.Errorf("DecodeLogs = %+v, %v, want the request", got, err)
		}
	})

	t.Run("an endless body is cut off at the limit", func(t *testing.T) {
		const limit = 4096
		endless := &countingReader{}

		_, err := otlp.DecodeLogs(endless, limit)

		if !errors.Is(err, otlp.ErrTooLarge) {
			t.Errorf("error = %v, want ErrTooLarge", err)
		}
		if endless.read > limit+1 {
			t.Errorf("read %d bytes, want at most %d: the limit must bound what is read", endless.read, limit+1)
		}
	})
}

func TestDecodeLogs_ReadError(t *testing.T) {
	errBoom := errors.New("connection reset")

	_, err := otlp.DecodeLogs(io.MultiReader(strings.NewReader(`{"resourceLogs":`), errReader{errBoom}), bodyLimit)

	if !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want it to wrap the read error", err)
	}
	if errors.Is(err, otlp.ErrTooLarge) {
		t.Errorf("error %v reports an oversized body, want a read error", err)
	}
}

type countingReader struct{ read int64 }

func (c *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	c.read += int64(len(p))
	return len(p), nil
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func FuzzDecodeLogs(f *testing.F) {
	f.Add(fixture(f, "api_request.json"))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(export(attrs(strAttr("session.id", "s")), apiRequest(intStrAttr("input_tokens", 5), costAttr(1.5)))))
	f.Add([]byte(export(attrs(), apiRequest(intAttr("input_tokens", -9), strAttr("cost_usd", "NaN"), strAttr("cost_usd", "Inf")))))
	f.Add([]byte(export(attrs(), record("claude_code.api_request", `"9223372036854775807"`, strAttr("session.id", "s")))))
	f.Add([]byte(export(attrs(), record("claude_code.api_request", `-9223372036854775808`, strAttr("session.id", "s")))))
	f.Add([]byte(export(attrs(), record("claude_code.api_request", "", strAttr("session.id", "s"), strAttr("event.timestamp", "0000-01-01T00:00:00+05:00")))))
	f.Add([]byte(export(attrs(), record("claude_code.api_request", "", strAttr("session.id", "s"), strAttr("event.timestamp", "9999-12-31T23:59:59-05:00")))))
	f.Add([]byte(export(attrs(), apiRequest(intAttr("input_tokens", 1<<41), costAttr(2e6)))))
	f.Add([]byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"attributes":[{"key":"event.name","value":{"stringValue":"api_request"}},{"key":"session.id","value":"x"}]}]}]}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := otlp.DecodeLogs(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			if got != nil {
				t.Errorf("DecodeLogs returned %+v with error %v", got, err)
			}
			return
		}

		for _, request := range got {
			if request.Session == "" {
				t.Errorf("request %+v has no session", request)
			}
			if !request.At.After(time.Unix(0, 0)) || request.At.Location() != time.UTC {
				t.Errorf("request %+v has no UTC time after the epoch", request)
			}
			if _, err := json.Marshal(request.At); err != nil {
				t.Errorf("request %+v has a time that does not marshal: %v", request, err)
			}
			for _, count := range []int64{request.Input, request.Output, request.CacheRead, request.CacheWrite, request.DurationMS} {
				if count < 0 || count > 1<<40 {
					t.Errorf("request %+v has a count outside 0 to 2^40", request)
				}
			}
			if request.CostUSD < 0 || request.CostUSD > 1_000_000 || math.IsNaN(request.CostUSD) {
				t.Errorf("request %+v has a cost outside 0 to 1e6 USD", request)
			}
		}

		if len(data) > 0 {
			if _, err := otlp.DecodeLogs(bytes.NewReader(data), int64(len(data))-1); !errors.Is(err, otlp.ErrTooLarge) {
				t.Errorf("a limit one byte short returned %v, want ErrTooLarge", err)
			}
		}
	})
}
