package otlp_test

import (
	"bytes"
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

	tests := []struct {
		name    string
		rec     string
		want    string // RFC 3339, or "" when the record is skipped
		wantNil bool
	}{
		{
			name: "event.timestamp",
			rec:  record("claude_code.api_request", "", strAttr("session.id", testSession), strAttr("event.timestamp", wantAt)),
			want: wantAt,
		},
		{
			name: "event.timestamp with offset is normalized to UTC",
			rec:  record("claude_code.api_request", "", strAttr("session.id", testSession), strAttr("event.timestamp", "2026-11-21T17:23:17.642-05:00")),
			want: wantAt,
		},
		{
			name: "event.timestamp wins over timeUnixNano",
			rec:  record("claude_code.api_request", `"1000000000"`, strAttr("session.id", testSession), strAttr("event.timestamp", wantAt)),
			want: wantAt,
		},
		{
			name: "timeUnixNano as a string",
			rec:  record("claude_code.api_request", `"`+nanos+`"`, strAttr("session.id", testSession)),
			want: wantAt,
		},
		{
			name: "timeUnixNano as a number",
			rec:  record("claude_code.api_request", nanos, strAttr("session.id", testSession)),
			want: wantAt,
		},
		{
			name: "unparsable event.timestamp falls back to timeUnixNano",
			rec:  record("claude_code.api_request", `"`+nanos+`"`, strAttr("session.id", testSession), strAttr("event.timestamp", "yesterday")),
			want: wantAt,
		},
		{
			name: "zero event.timestamp falls back to timeUnixNano",
			rec:  record("claude_code.api_request", `"`+nanos+`"`, strAttr("session.id", testSession), strAttr("event.timestamp", "0001-01-01T00:00:00Z")),
			want: wantAt,
		},
		{
			name: "event.timestamp of the wrong type falls back to timeUnixNano",
			rec:  record("claude_code.api_request", `"`+nanos+`"`, strAttr("session.id", testSession), intAttr("event.timestamp", 5)),
			want: wantAt,
		},
		{
			name:    "no time at all skips the record",
			rec:     record("claude_code.api_request", "", strAttr("session.id", testSession)),
			wantNil: true,
		},
		{
			name:    "timeUnixNano zero means unset",
			rec:     record("claude_code.api_request", `"0"`, strAttr("session.id", testSession)),
			wantNil: true,
		},
		{
			name:    "unparsable times skip the record",
			rec:     record("claude_code.api_request", `"soon"`, strAttr("session.id", testSession), strAttr("event.timestamp", "later")),
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decode(t, export(attrs(), tt.rec))

			if tt.wantNil {
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
		{"cost_usd as a numeric string", []string{strAttr("cost_usd", "0.25")}, 0.25},
		{"unusable cost_usd falls back to micros", []string{strAttr("cost_usd", "free"), intAttr("cost_usd_micros", 42)}, 0.000042},
		{"infinite cost is dropped", []string{strAttr("cost_usd", "Inf"), intAttr("cost_usd_micros", 42)}, 0.000042},
		{"NaN cost is dropped", []string{strAttr("cost_usd", "NaN")}, 0},
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
			if request.At.IsZero() || request.At.Location() != time.UTC {
				t.Errorf("request %+v has no UTC time", request)
			}
			if request.Input < 0 || request.Output < 0 || request.CacheRead < 0 || request.CacheWrite < 0 || request.DurationMS < 0 {
				t.Errorf("request %+v has a negative count", request)
			}
			if request.CostUSD < 0 || math.IsNaN(request.CostUSD) || math.IsInf(request.CostUSD, 0) {
				t.Errorf("request %+v has an unusable cost", request)
			}
		}

		if len(data) > 0 {
			if _, err := otlp.DecodeLogs(bytes.NewReader(data), int64(len(data))-1); !errors.Is(err, otlp.ErrTooLarge) {
				t.Errorf("a limit one byte short returned %v, want ErrTooLarge", err)
			}
		}
	})
}
