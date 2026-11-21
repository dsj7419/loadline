// Package otlp decodes the OpenTelemetry log events Claude Code exports over
// OTLP/HTTP JSON into domain records.
package otlp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

// ErrTooLarge reports an export body that exceeds the caller's size limit.
var ErrTooLarge = errors.New("otlp: export body too large")

// Spellings of the event that reports a model call. Claude Code puts the
// short one in a record's event.name attribute and the prefixed one in its
// body; either names the event wherever it appears.
const (
	eventAPIRequest         = "api_request"
	eventAPIRequestPrefixed = "claude_code.api_request"
)

// Attribute names, as Claude Code documents them.
const (
	attrEventName     = "event.name"
	attrEventTime     = "event.timestamp"
	attrSession       = "session.id"
	attrEmail         = "user.email"
	attrModel         = "model"
	attrInput         = "input_tokens"
	attrOutput        = "output_tokens"
	attrCacheRead     = "cache_read_tokens"
	attrCacheWrite    = "cache_creation_tokens"
	attrCostUSD       = "cost_usd"
	attrCostUSDMicros = "cost_usd_micros"
	attrDurationMS    = "duration_ms"
	attrQuerySource   = "query_source"
	attrAgentName     = "agent.name"
)

const microsPerUnit = 1e6

// DecodeLogs reads an OTLP/HTTP JSON logs export and returns the model calls
// it reports, in the order they appear. Every other event is ignored, and only
// the attributes a domain.APIRequest holds are ever read, so no content
// leaves the decoder.
//
// A record that cannot become a domain.APIRequest, because it has no session
// or no usable time, is skipped; so is an attribute of an unexpected type,
// which reads as absent. Negative counts and costs read as zero. A record's
// attributes take precedence over those of its resource.
//
// DecodeLogs reads at most limit bytes of r and returns ErrTooLarge when the
// body is longer. Malformed JSON is an error.
func DecodeLogs(r io.Reader, limit int64) ([]domain.APIRequest, error) {
	// Read one byte past the limit to tell a body that fits from one that
	// does not. The clamp keeps limit+1 from overflowing.
	body, err := io.ReadAll(io.LimitReader(r, min(limit, math.MaxInt64-1)+1))
	if err != nil {
		return nil, fmt.Errorf("read OTLP logs: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrTooLarge, limit)
	}

	var export exportRequest
	if err := json.Unmarshal(body, &export); err != nil {
		return nil, fmt.Errorf("decode OTLP logs: %w", err)
	}

	var requests []domain.APIRequest
	for _, resourceLogs := range export.ResourceLogs {
		for _, scopeLogs := range resourceLogs.ScopeLogs {
			for _, rec := range scopeLogs.LogRecords {
				if request, ok := rec.apiRequest(resourceLogs.Resource.Attributes); ok {
					requests = append(requests, request)
				}
			}
		}
	}
	return requests, nil
}

// The types below mirror only the parts of the OTLP/HTTP JSON logs export
// that DecodeLogs reads.

type exportRequest struct {
	ResourceLogs []resourceLogs `json:"resourceLogs"`
}

type resourceLogs struct {
	Resource  resource    `json:"resource"`
	ScopeLogs []scopeLogs `json:"scopeLogs"`
}

type resource struct {
	Attributes []keyValue `json:"attributes"`
}

type scopeLogs struct {
	LogRecords []logRecord `json:"logRecords"`
}

type logRecord struct {
	// TimeUnixNano is a JSON string, or a number from some exporters.
	TimeUnixNano json.RawMessage `json:"timeUnixNano"`
	Body         anyValue        `json:"body"`
	Attributes   []keyValue      `json:"attributes"`
}

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

// anyValue is an OTLP AnyValue. Each field holds the raw JSON of one kind of
// value, because exporters differ in how they write them: an int64 may be a
// JSON string or number, and JavaScript exporters write any whole number,
// 2.0 as much as 2, as an intValue.
type anyValue struct {
	StringValue json.RawMessage `json:"stringValue"`
	IntValue    json.RawMessage `json:"intValue"`
	DoubleValue json.RawMessage `json:"doubleValue"`
}

// UnmarshalJSON reads a value of an unexpected shape, such as a string where
// an object belongs, as empty, so one odd attribute cannot cost the export.
// encoding/json has already checked the syntax, so an error here is a type
// mismatch and nothing else.
func (v *anyValue) UnmarshalJSON(data []byte) error {
	type plain anyValue
	var decoded plain
	if json.Unmarshal(data, &decoded) == nil {
		*v = anyValue(decoded)
	}
	return nil
}

// text returns the value of a stringValue.
func (v anyValue) text() (string, bool) {
	var s string
	if len(v.StringValue) == 0 || json.Unmarshal(v.StringValue, &s) != nil {
		return "", false
	}
	return s, true
}

// number returns the text of a numeric value. It reads an intValue or a
// doubleValue, as a JSON number or a JSON string, and also a stringValue
// holding a number, which Claude Code uses for some attributes.
func (v anyValue) number() (string, bool) {
	for _, raw := range []json.RawMessage{v.IntValue, v.DoubleValue, v.StringValue} {
		if text, ok := numberText(raw); ok {
			return text, true
		}
	}
	return "", false
}

// numberText returns the digits of a raw JSON number or of a JSON string
// that holds one. Whether they parse is for the caller to find out.
func numberText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	if raw[0] != '"' {
		return string(raw), true
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return "", false
	}
	return text, true
}

func (v anyValue) integer() (int64, bool) {
	text, ok := v.number()
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		// ParseInt returns the nearest bound for a number out of range.
		return 0, false
	}
	return n, true
}

// float returns a finite number. A cost of NaN or infinity would poison every
// sum it joined.
func (v anyValue) float() (float64, bool) {
	text, ok := v.number()
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// attributes looks attribute values up in a log record first and in its
// resource second.
type attributes struct {
	record, resource []keyValue
}

func (a attributes) find(key string) anyValue {
	for _, list := range [][]keyValue{a.record, a.resource} {
		for _, kv := range list {
			if kv.Key == key {
				return kv.Value
			}
		}
	}
	return anyValue{}
}

func (a attributes) text(key string) string {
	s, _ := a.find(key).text()
	return s
}

// count returns a non-negative integer attribute, or zero when it is absent
// or unusable.
func (a attributes) count(key string) int64 {
	n, _ := a.find(key).integer()
	return max(n, 0)
}

// apiRequest returns the model call the record reports, and false when the
// record is another event or cannot be mapped.
func (rec logRecord) apiRequest(resourceAttributes []keyValue) (domain.APIRequest, bool) {
	attrs := attributes{record: rec.Attributes, resource: resourceAttributes}
	if !isAPIRequest(rec, attrs) {
		return domain.APIRequest{}, false
	}

	session := attrs.text(attrSession)
	at, hasTime := eventTime(rec, attrs)
	if session == "" || !hasTime {
		return domain.APIRequest{}, false
	}

	return domain.APIRequest{
		At:          at,
		Session:     domain.SessionID(session),
		Email:       attrs.text(attrEmail),
		Model:       attrs.text(attrModel),
		Input:       attrs.count(attrInput),
		Output:      attrs.count(attrOutput),
		CacheRead:   attrs.count(attrCacheRead),
		CacheWrite:  attrs.count(attrCacheWrite),
		CostUSD:     cost(attrs),
		DurationMS:  attrs.count(attrDurationMS),
		QuerySource: attrs.text(attrQuerySource),
		AgentName:   attrs.text(attrAgentName),
	}, true
}

// isAPIRequest reports whether the record is a model call. The event.name
// attribute decides when it is present; otherwise the body does.
func isAPIRequest(rec logRecord, attrs attributes) bool {
	name, ok := attrs.find(attrEventName).text()
	if !ok {
		name, _ = rec.Body.text()
	}
	return name == eventAPIRequest || name == eventAPIRequestPrefixed
}

// eventTime returns when the call happened: the event.timestamp attribute
// (RFC 3339), or else the record's timeUnixNano.
func eventTime(rec logRecord, attrs attributes) (time.Time, bool) {
	if text, ok := attrs.find(attrEventTime).text(); ok {
		if at, err := time.Parse(time.RFC3339Nano, text); err == nil && !at.IsZero() {
			return at.UTC(), true
		}
	}

	text, ok := numberText(rec.TimeUnixNano)
	if !ok {
		return time.Time{}, false
	}
	nanos, err := strconv.ParseInt(text, 10, 64)
	if err != nil || nanos <= 0 {
		return time.Time{}, false
	}
	return time.Unix(0, nanos).UTC(), true
}

// cost returns the call's cost in US dollars, from cost_usd or else from
// cost_usd_micros. A whole-dollar cost arrives as an intValue.
func cost(attrs attributes) float64 {
	if usd, ok := attrs.find(attrCostUSD).float(); ok {
		return max(usd, 0)
	}
	if micros, ok := attrs.find(attrCostUSDMicros).float(); ok {
		return max(micros, 0) / microsPerUnit
	}
	return 0
}
