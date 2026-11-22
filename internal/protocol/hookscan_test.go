package protocol_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/dsj7419/loadline/internal/protocol"
)

const (
	// maxMember is the longest member key or value ReadHookInput keeps.
	maxMember = protocol.MaxPayloadBytes
	// maxField is the longest a field may be as a payload encodes it.
	maxField = 4 << 10
)

// bu is a backslash and a u: the start of a \u escape, kept as two bytes so
// the source never holds a literal escape.
var bu = string([]byte{'\\', 'u'})

// oracle decodes the first JSON value of input as encoding/json does, with
// ReadHookInput's limits applied by hand: a member whose key is longer than
// maxMember is dropped, one whose value is longer reads as "", and a field
// that encodes to more than maxField bytes is emptied. ok is false when
// encoding/json refuses the first value or it is not an object.
func oracle(input []byte) (in protocol.HookInput, ok bool) {
	var raw json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(input)).Decode(&raw); err != nil || raw[0] != '{' {
		return protocol.HookInput{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		panic(err)
	}
	var model []string
	from := dec.InputOffset()
	for dec.More() {
		if _, err := dec.Token(); err != nil {
			panic(err)
		}
		key := bytes.Trim(raw[from:dec.InputOffset()], " \t\r\n,")
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			panic(err)
		}
		from = dec.InputOffset()
		switch {
		case len(key) > maxMember:
		case len(value) > maxMember:
			model = append(model, string(key)+`:""`)
		default:
			model = append(model, string(key)+":"+string(value))
		}
	}
	err := json.Unmarshal([]byte("{"+strings.Join(model, ",")+"}"), &in)
	if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); err != nil && (!ok || typeErr.Field == "") {
		panic(err)
	}
	fields := reflect.ValueOf(&in).Elem()
	for i := range fields.NumField() {
		encoded, err := json.Marshal(fields.Field(i).String())
		if err != nil {
			panic(err)
		}
		if len(encoded)-2 > maxField {
			fields.Field(i).SetString("")
		}
	}
	return in, true
}

// assertAgrees checks ReadHookInput, reading input through r, against the
// oracle: input the oracle refuses is an error with no fields, and any
// other input gives the oracle's fields.
func assertAgrees(t *testing.T, input []byte, r io.Reader) {
	t.Helper()
	got, err := protocol.ReadHookInput(r)
	assertOracleResult(t, input, got, err)
}

// assertOracleResult checks what ReadHookInput gave for input against the
// oracle.
func assertOracleResult(t *testing.T, input []byte, got protocol.HookInput, err error) {
	t.Helper()
	want, ok := oracle(input)
	switch {
	case ok && (err != nil || got != want):
		t.Fatalf("valid input %.200q (%d bytes): got %+v, %v; want %+v", input, len(input), got, err, want)
	case !ok && (err == nil || got != protocol.HookInput{}):
		t.Fatalf("invalid input %.200q (%d bytes): got %+v, %v; want an error and no fields", input, len(input), got, err)
	}
}

// readers are the ways a test feeds an input: whole, a byte at a time, in
// halves, and with the end of input reported alongside the last bytes.
var readers = map[string]func([]byte) io.Reader{
	"whole":     func(b []byte) io.Reader { return bytes.NewReader(b) },
	"one byte":  func(b []byte) io.Reader { return iotest.OneByteReader(bytes.NewReader(b)) },
	"halves":    func(b []byte) io.Reader { return iotest.HalfReader(bytes.NewReader(b)) },
	"eof early": func(b []byte) io.Reader { return iotest.DataErrReader(bytes.NewReader(b)) },
}

// hookSeeds are payloads whose members all fit, valid and not.
var hookSeeds = []string{
	`{"session_id":"s1","hook_event_name":"Stop"}`,
	` { "session_id" : "s1" , "cwd" : "C:\\Users\\x" } `,
	`{}`,
	`{ }`,
	`{"session_id":"s\"1","tool_name":"a\\\\","agent_id":"\\\""}`,
	`{"tool_input":{"a":["}","{",{"b":"]\"["}]},"session_id":"s1"}`,
	`{"n":-1.5e+3,"t":true,"f":false,"z":null,"e":[0,-0,1E9,0.5e-2],"session_id":"s1"}`,
	`{"session_id":1,"hook_event_name":["x"],"cwd":{"a":1}}`,
	`{"session_id":"` + bu + `00e9` + bu + `d83d` + bu + `de00","source":"startup","x":"\/\b\f\n\r\t"}`,
	`{"session_id":"s1","session_id":"s2"}`,
	`{"session_id":"s1","SESSION_ID":"s2","tool` + bu + `005fname":"t","cwd":null}`,
	`{"session_id":"s1"} trailing`,
	"{\"session_id\":\"s1\"}\n{\"session_id\":\"s2\"}",
	`{"session_id":"s1",}`,
	`{"session_id" "s1"}`,
	`{"session_id":"s1" "cwd":"x"}`,
	`{"session_id":}`,
	`{"session_id":tru}`,
	`{"session_id":truex}`,
	`{"x":01}`, `{"x":-}`, `{"x":1.}`, `{"x":.5}`, `{"x":+1}`, `{"x":1e}`, `{"x":1e+}`, `{"x":-01}`,
	`{"x":[1,]}`, `{"x":[,1]}`, `{"x":[1 2]}`, `{"x":{"a":1,}}`, `{"x":{"a"}}`, `{"x":{1:2}}`, `{"x":{"a":,"b":1}}`,
	`{"x":[}`, `{"x":{]}`, `{"x":"\q"}`, `{"x":"` + bu + `12"}`, `{"x":"` + bu + `12g4"}`, `{"x":"` + bu + `123"}`,
	`{session_id:"s1"}`,
	`{"session_id":"s1"`,
	`{"session_id":"s1`,
	`{"a":[1,2}`,
	`[{"session_id":"s1"}]`,
	`null`, `"s1"`, `42`, ``, "  \n",
	"{\"session_id\":\"line\nbreak\"}",
	"{\"session_id\":\"tab\tin\"}",
	"{\"session_id\":\"del\x7f\"}",
	"{\"session_id\":\"bad utf8 \xff\xfe\"}",
	"\ufeff{\"session_id\":\"s1\"}",
	"{\"session_id\":\"s1\"\f}",
	`{"a":` + strings.Repeat("[", 9998) + strings.Repeat("]", 9998) + `,"session_id":"s1"}`,
	`{"a":` + strings.Repeat("[", 9999) + strings.Repeat("]", 9999) + `,"session_id":"s1"}`,
	`{"a":` + strings.Repeat("[", 10000) + strings.Repeat("]", 10000) + `,"session_id":"s1"}`,
	`{"session_id":"` + strings.Repeat("s", maxField) + `"}`,
	`{"session_id":"` + strings.Repeat("s", maxField+1) + `"}`,
}

func TestReadHookInput_MatchesEncodingJSONOnShortMembers(t *testing.T) {
	for _, input := range hookSeeds {
		for name, reader := range readers {
			t.Run(name, func(t *testing.T) { assertAgrees(t, []byte(input), reader([]byte(input))) })
		}
	}
}

func FuzzReadHookInputMatchesEncodingJSON(f *testing.F) {
	for _, input := range hookSeeds {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		assertAgrees(t, []byte(input), strings.NewReader(input))
	})
}

// longMemberCases are inputs with a member too long to keep, so that only
// ReadHookInput's own checks judge it: nothing re-reads it afterwards.
func longMemberCases() map[string]string {
	pad := strings.Repeat("a", maxMember+100)
	ones := strings.Repeat("1,", maxMember)
	nulls := strings.Repeat("null,", maxMember/4)
	return map[string]string{
		"nested key without colon":    `{"x":{"` + pad + `"}},"session_id":"leak"}`,
		"nested key colon no value":   `{"x":{"` + pad + `":}},"session_id":"leak"}`,
		"comma for a nested value":    `{"x":{"a":,"b":"` + pad + `"},"session_id":"leak"}`,
		"long number cut at dot":      `{"x":1` + strings.Repeat("0", maxMember) + `.,"session_id":"leak"}`,
		"long leading zeros":          `{"x":0` + strings.Repeat("0", maxMember) + `,"session_id":"leak"}`,
		"long letters":                `{"x":` + strings.Repeat("t", maxMember+10) + `,"session_id":"leak"}`,
		"bad scalar":                  `{"x":` + pad + `"q,"session_id":"leak"}`,
		"bad literal deep in array":   `{"x":[` + nulls + `nulll],"session_id":"leak"}`,
		"three hex digits":            `{"x":"` + pad + bu + `123","session_id":"leak"}`,
		"two hex digits":              `{"x":"` + pad + bu + `00","session_id":"leak"}`,
		"raw newline after cap":       `{"x":"` + pad + "\n" + `","session_id":"leak"}`,
		"control char":                `{"x":"` + pad + "\x01" + `","session_id":"leak"}`,
		"bad escape":                  `{"x":"` + pad + `\q","session_id":"leak"}`,
		"uppercase escape":            `{"x":"` + pad + `\N","session_id":"leak"}`,
		"extra close after long":      `{"x":["` + pad + `"]],"session_id":"leak"}`,
		"mismatched":                  `{"x":["` + pad + `"},"session_id":"leak"}`,
		"object closed by ]":          `{"x":{"a":"` + pad + `"],"session_id":"leak"}`,
		"trailing comma top":          `{"x":"` + pad + `",}`,
		"trailing comma nested":       `{"x":["` + pad + `",],"session_id":"leak"}`,
		"long key missing colon":      `{"` + pad + `" "v","session_id":"leak"}`,
		"long key bad escape":         `{"` + pad + `\x":1,"session_id":"leak"}`,
		"unterminated long array":     `{"x":[` + ones + `1`,
		"unclosed container":          `{"x":[["` + pad + `"]`,
		"unterminated string":         `{"x":"` + pad,
		"formfeed in long array":      `{"x":[` + ones + "1\f" + `],"session_id":"leak"}`,
		"vertical tab in long array":  `{"x":[` + ones + "1\v" + `],"session_id":"leak"}`,
		"NUL in long array":           `{"x":[` + ones + "\x00" + `],"session_id":"leak"}`,
		"nested missing value":        `{"x":{"a":"` + pad + `","b"},"session_id":"leak"}`,
		"missing comma nested":        `{"x":[1 "` + pad + `"],"session_id":"leak"}`,
		"array swallows next member":  `{"x":[` + ones + `1,"session_id":"leak"}`,
		"objects closed by brace":     `{"x":[{}` + strings.Repeat(`,{}`, maxMember/3) + `}],"session_id":"leak"}`,
		"backslash at EOF":            `{"x":"` + pad + `\`,
		"lone minus":                  `{"x":[` + ones + `-],"session_id":"leak"}`,
		"exponent without digits":     `{"x":[` + ones + `1e],"session_id":"leak"}`,
		"exponent sign alone":         `{"x":[` + ones + `1e+],"session_id":"leak"}`,
		"leading dot":                 `{"x":[` + ones + `.5],"session_id":"leak"}`,
		"trailing dot":                `{"x":[` + ones + `1.],"session_id":"leak"}`,
		"leading plus":                `{"x":[` + ones + `+1],"session_id":"leak"}`,
		"leading zero":                `{"x":[` + ones + `-01],"session_id":"leak"}`,
		"two exponents":               `{"x":[` + ones + `1e5e5],"session_id":"leak"}`,
		"NaN":                         `{"x":[` + ones + `NaN],"session_id":"leak"}`,
		"hex number":                  `{"x":[` + ones + `0x10],"session_id":"leak"}`,
		"literal run on":              `{"x":["` + pad + `",truex],"session_id":"leak"}`,
		"literal run into string":     `{"x":[` + nulls + `null"a"],"session_id":"leak"}`,
		"colon after long value":      `{"x":"` + pad + `":1,"session_id":"leak"}`,
		"missing comma top":           `{"x" : "` + pad + `" "session_id":"leak"}`,
		"EOF after long value":        `{"x":"` + pad + `"`,
		"array in key position":       `{"x":{["` + pad + `"]:1},"session_id":"leak"}`,
		"nested comma comma":          `{"x":[` + ones + `,1],"session_id":"leak"}`,
		"colon in an array":           `{"x":["` + pad + `":1],"session_id":"leak"}`,
		"nested object colon comma":   `{"x":{"a":"` + pad + `":1},"session_id":"leak"}`,
		"comma after a key":           `{"x":{"a","` + pad + `":1},"session_id":"leak"}`,
		"double colon":                `{"x":{"a"::"` + pad + `"},"session_id":"leak"}`,
		"string in comma slot":        `{"x":{"a":1 "b":"` + pad + `"},"session_id":"leak"}`,
		"close bracket on empty obj":  `{"x":[` + ones + `{]],"session_id":"leak"}`,
		"depth 10 001 arrays":         `{"x":` + strings.Repeat("[", 10000) + `"` + pad + `"` + strings.Repeat("]", 10000) + `,"session_id":"leak"}`,
		"depth 10 001 objects":        `{"x":` + strings.Repeat(`{"a":`, 10000) + `"` + pad + `"` + strings.Repeat("}", 10000) + `,"session_id":"leak"}`,
		"depth 40 001 arrays":         `{"x":` + strings.Repeat("[", 40000) + strings.Repeat("]", 40000) + `,"session_id":"leak"}`,
		"depth 10 000 arrays":         `{"x":` + strings.Repeat("[", 9999) + `"` + pad + `"` + strings.Repeat("]", 9999) + `,"session_id":"s"}`,
		"depth 10 000 objects":        `{"x":` + strings.Repeat(`{"a":`, 9999) + `"` + pad + `"` + strings.Repeat("}", 9999) + `,"session_id":"s"}`,
		"tab in long string":          `{"x":"` + pad + "\t" + `","session_id":"leak"}`,
		"DEL and 0xff in long string": `{"x":"` + pad + "\x7f\xff\xfe" + `","session_id":"s"}`,
		"lone surrogate long string":  `{"x":"` + pad + bu + `d800","session_id":"s"}`,
		"garbage after object":        `{"x":"` + pad + `","session_id":"s"}garbage{[`,
		"escaped key dup long last":   `{"session_id":"s1","session_` + bu + `0069d":"` + pad + `"}`,
		"long s folded dup long last": `{"session_id":"s1","` + "\xc5\xbf" + `ession_id":"` + pad + `"}`,
		"kelvin folded dup":           `{"hook_event_name":"a","hoo` + "\xe2\x84\xaa" + `_event_name":"` + pad + `"}`,
		"long first short last":       `{"SESSION_ID":"` + pad + `","session_id":"s2"}`,
		"exactly cap value":           `{"session_id":"` + strings.Repeat("b", maxMember-2) + `","cwd":"c"}`,
		"cap plus one value":          `{"session_id":"` + strings.Repeat("b", maxMember-1) + `","cwd":"c"}`,
		"exactly cap key":             `{"` + strings.Repeat("k", maxMember-2) + `":1,"cwd":"c"}`,
		"cap plus one key":            `{"` + strings.Repeat("k", maxMember-1) + `":1,"cwd":"c"}`,
		"many long members":           `{` + strings.Repeat(`"x":"`+pad+`",`, 60) + `"session_id":"s"}`,
		"valid big mixed": `{"x":[` + strings.Repeat(`{"a":[true,false,null,-0.5e-3,"`+bu+`00e9\\\""],"b":{}},`, 2000) +
			`[]],"session_id":"s"}`,
		"BOM in long string":           `{"x":"` + pad + "\xef\xbb\xbf" + `","session_id":"s"}`,
		"escapes after cap":            `{"x":"` + pad + `\/\b\f\n\r\t\"\\","session_id":"s"}`,
		"hex after cap":                `{"x":"` + pad + bu + `ABCD` + bu + `abcd","session_id":"s"}`,
		"top value then nothing":       `{"x":[` + ones + `1]`,
		"number then EOF":              `{"x":1` + strings.Repeat("0", maxMember+5),
		"empty key long value":         `{"":"` + pad + `","session_id":"s"}`,
		"whitespace-only after member": `{"x":"` + pad + `"   ` + "\r\n\t" + `}`,
	}
}

// Long members, well-formed and malformed, at the grammar's edges (escapes,
// numbers, literals, brackets, commas and truncated input), each read whole,
// a byte at a time, in halves and with an early end of input.
func TestReadHookInput_AgreesWithEncodingJSONOnLongMembers(t *testing.T) {
	for name, input := range longMemberCases() {
		for readerName, reader := range readers {
			t.Run(name+"/"+readerName, func(t *testing.T) { assertAgrees(t, []byte(input), reader([]byte(input))) })
		}
	}
}

// A member too long to keep is malformed or not exactly as encoding/json
// would find it: malformed input is an error, never a source of fields.
// These are the grammar's edges, which a member short enough to keep would
// have checked again by encoding/json.
func TestReadHookInput_MalformedLongMemberIsAnError(t *testing.T) {
	pad := strings.Repeat("a", maxMember+100)
	ones := strings.Repeat("1,", maxMember)
	for name, input := range map[string]string{
		"10 001 levels":          `{"x":` + strings.Repeat("[", 10000) + `"` + pad + `"` + strings.Repeat("]", 10000) + `,"session_id":"leak"}`,
		"a lone -":               `{"x":[` + ones + `-],"session_id":"leak"}`,
		"a trailing .":           `{"x":[` + ones + `1.],"session_id":"leak"}`,
		"a 3-digit escape":       `{"x":"` + pad + bu + `123","session_id":"leak"}`,
		"a comma for a value":    `{"x":{"a":,"b":"` + pad + `"},"session_id":"leak"}`,
		"a bad scalar":           `{"x":` + pad + `"q,"session_id":"leak"}`,
		"a mismatched bracket":   `{"x":["` + pad + `"},"session_id":"leak"}`,
		"an object closed by ]":  `{"x":{"a":"` + pad + `"],"session_id":"leak"}`,
		"a missing colon":        `{"x":{"a" "` + pad + `"},"session_id":"leak"}`,
		"an unterminated string": `{"x":"` + pad,
	} {
		t.Run(name, func(t *testing.T) {
			in, err := protocol.ReadHookInput(strings.NewReader(input))

			if err == nil || in != (protocol.HookInput{}) {
				t.Errorf("got %+v, %v; want an error and no fields", in, err)
			}
		})
	}
	deepest := `{"x":` + strings.Repeat("[", 9999) + `"` + pad + `"` + strings.Repeat("]", 9999) + `,"session_id":"s"}`
	if in, err := protocol.ReadHookInput(strings.NewReader(deepest)); err != nil || in.SessionID != "s" {
		t.Errorf("10 000 levels: got %+v, %v; want them read", in, err)
	}
}

// A well-formed long member is passed over, and its neighbors still count,
// quote traps included.
func TestReadHookInput_SkipsLongMembers(t *testing.T) {
	long := strings.Repeat("y", maxMember)
	for name, member := range map[string]string{
		"string":     `"tool_response":"` + long + `"`,
		"object":     `"tool_response":{"output":"` + long + `","nested":[{"a":"}"}],"n":-1.5e+3}`,
		"array":      `"tool_response":["` + long + `",true,null,0]`,
		"number":     `"tool_response":1` + strings.Repeat("0", maxMember),
		"key":        `"` + long + `":"x"`,
		"quote trap": `"x":{"a":"` + long + `\"},\"session_id\":\"leak"}`,
		"deep":       `"x":` + strings.Repeat("[", 9998) + `"` + long + `"` + strings.Repeat("]", 9998),
	} {
		t.Run(name, func(t *testing.T) {
			input := `{"session_id":"s1",` + member + `,"tool_name":"Bash"}`

			got, err := protocol.ReadHookInput(strings.NewReader(input))

			if err != nil || got != (protocol.HookInput{SessionID: "s1", ToolName: "Bash"}) {
				t.Errorf("got %+v, %v; want s1's Bash call alone", got, err)
			}
		})
	}
}

// As in encoding/json, the last occurrence of a key wins, in any letter
// case; when that one is too long to keep, or longer than a field may be,
// the field is left empty.
func TestReadHookInput_LongLastDuplicateEmptiesTheField(t *testing.T) {
	pad := strings.Repeat("a", maxMember+100)
	overField := strings.Repeat("a", maxField+1)
	for name, input := range map[string]string{
		"string":                    `{"session_id":"s1","session_id":"` + pad + `","tool_name":"Bash"}`,
		"letter case":               `{"session_id":"s1","SESSION_ID":"` + pad + `","tool_name":"Bash"}`,
		"nested":                    `{"session_id":"s1","session_id":["` + pad + `"],"tool_name":"Bash"}`,
		"over a field":              `{"session_id":"s1","session_id":"` + overField + `","tool_name":"Bash"}`,
		"over a field, letter case": `{"session_id":"s1","Session_ID":"` + overField + `","tool_name":"Bash"}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := protocol.ReadHookInput(strings.NewReader(input))

			if err != nil || got != (protocol.HookInput{ToolName: "Bash"}) {
				t.Errorf("got %+v, %v; want the session left empty", got, err)
			}
		})
	}
	for name, input := range map[string]string{
		"after a member too long to keep": `{"session_id":"` + pad + `","session_id":"s2"}`,
		"after a field over 4 KiB":        `{"session_id":"` + overField + `","session_id":"s2"}`,
	} {
		t.Run("short last duplicate "+name, func(t *testing.T) {
			got, err := protocol.ReadHookInput(strings.NewReader(input))

			if err != nil || got.SessionID != "s2" {
				t.Errorf("got %+v, %v; want s2", got, err)
			}
		})
	}
}

// everyField returns a hook payload with every HookInput field set to value.
func everyField(t *testing.T, value string) string {
	t.Helper()
	var in protocol.HookInput
	fields := reflect.ValueOf(&in).Elem()
	for i := range fields.NumField() {
		fields.Field(i).SetString(value)
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// A field longer than 4 KiB, as a payload encodes it, reads as empty, like
// a member too long to keep, so that every HookInput fits in a payload.
func TestReadHookInput_EmptiesFieldsTooLongForAPayload(t *testing.T) {
	tests := []struct {
		name, value string
		kept        bool
	}{
		{name: "4 KiB", value: strings.Repeat("v", maxField), kept: true},
		{name: "4 KiB and a byte", value: strings.Repeat("v", maxField+1)},
		// json.Marshal writes < as six bytes, a \u escape.
		{name: "4 KiB encoded", value: strings.Repeat("<", maxField/6) + strings.Repeat("v", maxField%6), kept: true},
		{name: "4 KiB and a byte encoded", value: strings.Repeat("<", maxField/6) + strings.Repeat("v", maxField%6+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, err := protocol.ReadHookInput(strings.NewReader(everyField(t, tt.value)))
			if err != nil {
				t.Fatalf("ReadHookInput: %v", err)
			}

			fields := reflect.ValueOf(in)
			for i := range fields.NumField() {
				got, name := fields.Field(i).String(), fields.Type().Field(i).Name
				if tt.kept && got != tt.value {
					t.Errorf("%s = %d bytes, want the %d byte value kept", name, len(got), len(tt.value))
				}
				if !tt.kept && got != "" {
					t.Errorf("%s = %d bytes, want it emptied", name, len(got))
				}
			}
		})
	}
}

// Whatever ReadHookInput gives, encoded again, fits in an envelope's
// payload, however its fields were escaped.
func TestReadHookInput_PayloadAlwaysFits(t *testing.T) {
	for name, value := range map[string]string{
		"plain":             strings.Repeat("v", maxField),
		"escaped as html":   strings.Repeat("<", maxField/6),
		"control bytes":     strings.Repeat("\x01", maxField/6),
		"invalid utf-8":     strings.Repeat("\xff", maxField/6),
		"escaped backslash": strings.Repeat(`\`, maxField/2),
	} {
		t.Run(name, func(t *testing.T) {
			in, err := protocol.ReadHookInput(strings.NewReader(everyField(t, value)))
			if err != nil {
				t.Fatalf("ReadHookInput: %v", err)
			}

			payload, err := json.Marshal(in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if in.SessionID == "" || len(payload) > protocol.MaxPayloadBytes {
				t.Errorf("every field at the limit encodes to %d bytes, want them kept and at most %d",
					len(payload), protocol.MaxPayloadBytes)
			}
		})
	}
}

func TestReadHookInput_RefusesTooManyShortMembers(t *testing.T) {
	var input bytes.Buffer
	input.WriteString(`{"session_id":"s1"`)
	member := `,"m":"` + strings.Repeat("z", maxMember-16) + `"`
	for input.Len() <= 1<<20 {
		input.WriteString(member)
	}
	input.WriteString("}")

	if _, err := protocol.ReadHookInput(&input); err == nil {
		t.Error("read a hook input with over 1 MiB of short members, want an error")
	}
}

// Fifty megabytes in one member, far beyond any real payload, cost a few
// read buffers, not the member.
func TestReadHookInput_HoldsLittleOfALongMember(t *testing.T) {
	for name, source := range map[string]func() io.Reader{
		"a string": func() io.Reader {
			return io.MultiReader(strings.NewReader(`{"session_id":"s","tool_response":"`),
				&repeatReader{b: 'x', n: 50 << 20}, strings.NewReader(`","hook_event_name":"PostToolUse"}`))
		},
		"an array": func() io.Reader {
			return io.MultiReader(strings.NewReader(`{"session_id":"s","tool_response":[`),
				&cycleReader{s: `{"line":"xxxxxxxxxxxxxxxx","n":12.5e-1},`, left: 50 << 20},
				strings.NewReader(`null],"hook_event_name":"PostToolUse"}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := source()
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)

			in, err := protocol.ReadHookInput(input)

			runtime.ReadMemStats(&after)
			if err != nil || in.SessionID != "s" || in.HookEventName != "PostToolUse" {
				t.Errorf("got %+v, %v; want the event around the content", in, err)
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
				t.Errorf("allocated %d KiB reading a 50 MiB member, want under 1 MiB", allocated>>10)
			}
		})
	}
}

// cycleReader yields copies of s, left bytes in all, the last copy cut
// short, without allocating as it goes.
type cycleReader struct {
	s    string
	at   int
	left int
}

func (r *cycleReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && r.left > 0 {
		c := copy(p[n:min(len(p), n+r.left)], r.s[r.at:])
		n += c
		r.left -= c
		r.at = (r.at + c) % len(r.s)
	}
	return n, nil
}

// A long string is read in slices of the read buffer's size. An escape split
// across two of them still decides whether the quote after it closes the
// string. The lengths tried put the end of the escape on every side of the
// first slice boundary, wherever in the buffer the string starts.
func TestReadHookInput_EscapesAcrossReadBoundaries(t *testing.T) {
	tails := []string{`\"`, `\\`, `\\\"`, `\\\\`, `\"\\`, bu + `00e9`, bu + `00E9\"`}
	const prefix = `{"tool_response":"`
	for _, tail := range tails {
		for length := protocol.HookReadBuffer - 40; length <= protocol.HookReadBuffer+40; length++ {
			body := strings.Repeat("x", length-len(tail)) + tail
			input := prefix + body + `","session_id":"s1","tool_name":"Bash"}`

			got, err := protocol.ReadHookInput(strings.NewReader(input))

			if err != nil || got.SessionID != "s1" || got.ToolName != "Bash" {
				t.Fatalf("tail %q at length %d: got %+v, %v; want s1's Bash call", tail, length, got, err)
			}
		}
	}
}
