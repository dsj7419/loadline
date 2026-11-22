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
)

const microsPerUnit = 1e6

// Bounds on what a record may report. Anything above them reads as absent, so
// that sums over many records cannot overflow or reach infinity.
const (
	maxCount   = 1 << 40
	maxCostUSD = 1_000_000
)

// DecodeLogs reads an OTLP/HTTP JSON logs export and returns the model calls
// it reports, in the order they appear. Every other event is ignored, and only
// the attributes a domain.APIRequest holds are ever read, so no content
// leaves the decoder.
//
// A record that cannot become a domain.APIRequest, because it has no session
// or no usable time, is skipped. A usable time is after the Unix epoch and
// before year 10000, so it always marshals. An attribute of an unexpected
// type, or beyond the bounds a call can plausibly reach, reads as absent.
// Negative counts and costs read as zero. A record's attributes take
// precedence over those of its resource, which supplies none of a record's
// event name or time.
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
	for _, rl := range export.ResourceLogs {
		inherited := inheritedAttributes(rl.Resource.Attributes)
		for _, sl := range rl.ScopeLogs {
			for _, rec := range sl.LogRecords {
				if request, ok := rec.apiRequest(inherited); ok {
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

// number returns the text of a numeric value: an intValue or a doubleValue,
// as a JSON number or a JSON string. A stringValue is never a number.
func (v anyValue) number() (string, bool) {
	for _, raw := range []json.RawMessage{v.IntValue, v.DoubleValue} {
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

// inheritable reports whether a record may take the attribute from its
// resource. A resource describes the process that emits events, not any one
// event, so the attributes that name an event and say when it happened are
// the record's own.
func inheritable(key string) bool {
	switch key {
	case attrSession, attrEmail, attrModel, attrInput, attrOutput, attrCacheRead, attrCacheWrite,
		attrCostUSD, attrCostUSDMicros:
		return true
	}
	return false
}

// inheritedAttributes picks out of a resource's attributes the ones its
// records may inherit, once per resource rather than once per record.
func inheritedAttributes(list []keyValue) map[string]anyValue {
	var inherited map[string]anyValue
	for _, kv := range list {
		if !inheritable(kv.Key) {
			continue
		}
		if _, seen := inherited[kv.Key]; seen {
			continue
		}
		if inherited == nil {
			inherited = make(map[string]anyValue)
		}
		inherited[kv.Key] = kv.Value
	}
	return inherited
}

// attributes looks attribute values up in a log record first and, for the
// inheritable ones, in its resource second.
type attributes struct {
	record    []keyValue
	inherited map[string]anyValue
}

func (a attributes) find(key string) anyValue {
	for _, kv := range a.record {
		if kv.Key == key {
			return kv.Value
		}
	}
	return a.inherited[key]
}

func (a attributes) text(key string) string {
	s, _ := a.find(key).text()
	return s
}

// count returns a non-negative integer attribute, or zero when it is absent,
// unusable or above maxCount.
func (a attributes) count(key string) int64 {
	n, ok := a.find(key).integer()
	if !ok || n > maxCount {
		return 0
	}
	return max(n, 0)
}

// apiRequest returns the model call the record reports, and false when the
// record is another event or cannot be mapped.
func (rec logRecord) apiRequest(inherited map[string]anyValue) (domain.APIRequest, bool) {
	attrs := attributes{record: rec.Attributes, inherited: inherited}
	if !isAPIRequest(rec, attrs) {
		return domain.APIRequest{}, false
	}

	session := attrs.text(attrSession)
	at, hasTime := eventTime(rec, attrs)
	if session == "" || !hasTime {
		return domain.APIRequest{}, false
	}

	return domain.APIRequest{
		At:         at,
		Session:    domain.SessionID(session),
		Email:      attrs.text(attrEmail),
		Model:      attrs.text(attrModel),
		Input:      attrs.count(attrInput),
		Output:     attrs.count(attrOutput),
		CacheRead:  attrs.count(attrCacheRead),
		CacheWrite: attrs.count(attrCacheWrite),
		CostUSD:    cost(attrs),
	}, true
}

// isAPIRequest reports whether the record is a model call. The record's
// event.name attribute decides when it has one; otherwise its body does.
func isAPIRequest(rec logRecord, attrs attributes) bool {
	name, ok := attrs.find(attrEventName).text()
	if !ok {
		name, _ = rec.Body.text()
	}
	return name == eventAPIRequest || name == eventAPIRequestPrefixed
}

// eventTime returns when the call happened: the event.timestamp attribute
// (RFC 3339), or else the record's timeUnixNano. A time that is no valid
// event time counts as missing; that also rules out the 0 an exporter writes
// for an unset timestamp.
func eventTime(rec logRecord, attrs attributes) (time.Time, bool) {
	if text, ok := attrs.find(attrEventTime).text(); ok {
		if at, err := time.Parse(time.RFC3339Nano, text); err == nil && domain.ValidEventTime(at) {
			return at.UTC(), true
		}
	}

	text, ok := numberText(rec.TimeUnixNano)
	if !ok {
		return time.Time{}, false
	}
	nanos, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	at := time.Unix(0, nanos).UTC()
	return at, domain.ValidEventTime(at)
}

// cost returns the call's cost in US dollars, from cost_usd or else from
// cost_usd_micros. A whole-dollar cost arrives as an intValue. A figure above
// maxCostUSD is unusable, like one that is not a number.
func cost(attrs attributes) float64 {
	if usd, ok := attrs.find(attrCostUSD).float(); ok && usd <= maxCostUSD {
		return max(usd, 0)
	}
	if micros, ok := attrs.find(attrCostUSDMicros).float(); ok && micros <= maxCostUSD*microsPerUnit {
		return max(micros, 0) / microsPerUnit
	}
	return 0
}
