package protocol_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/protocol"
)

// maxMember is the longest member key or value ReadHookInput keeps.
const maxMember = protocol.MaxPayloadBytes

// stdlibHookInput decodes input as encoding/json does, the reference that
// ReadHookInput must agree with on every input whose members all fit.
func stdlibHookInput(input string) (protocol.HookInput, error) {
	var in *protocol.HookInput
	err := json.NewDecoder(strings.NewReader(input)).Decode(&in)
	if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok && typeErr.Field != "" {
		err = nil
	}
	if err != nil {
		return protocol.HookInput{}, err
	}
	if in == nil {
		return protocol.HookInput{}, errors.New("null")
	}
	return *in, nil
}

// assertMatchesStdlib checks ReadHookInput against encoding/json on input:
// both fail, or both give the same fields.
func assertMatchesStdlib(t *testing.T, input string) {
	t.Helper()
	want, wantErr := stdlibHookInput(input)
	got, gotErr := protocol.ReadHookInput(strings.NewReader(input))
	if (wantErr == nil) != (gotErr == nil) || got != want {
		t.Fatalf("ReadHookInput(%.300q) = %+v, %v; encoding/json gives %+v, %v", input, got, gotErr, want, wantErr)
	}
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
	`{"session_id":"\u00e9\ud83d\ude00","source":"startup","x":"\/\b\f\n\r\t"}`,
	`{"session_id":"s1","session_id":"s2"}`,
	`{"session_id":"s1","SESSION_ID":"s2","tool\u005fname":"t","cwd":null}`,
	`{"session_id":"s1"} trailing`,
	"{\"session_id\":\"s1\"}\n{\"session_id\":\"s2\"}",
	`{"session_id":"s1",}`,
	`{"session_id" "s1"}`,
	`{"session_id":"s1" "cwd":"x"}`,
	`{"session_id":}`,
	`{"session_id":tru}`,
	`{"session_id":truex}`,
	`{"x":01}`, `{"x":-}`, `{"x":1.}`, `{"x":.5}`, `{"x":+1}`, `{"x":1e}`, `{"x":1e+}`, `{"x":-01}`,
	`{"x":[1,]}`, `{"x":[,1]}`, `{"x":[1 2]}`, `{"x":{"a":1,}}`, `{"x":{"a"}}`, `{"x":{1:2}}`,
	`{"x":[}`, `{"x":{]}`, `{"x":"\q"}`, `{"x":"\u12"}`, `{"x":"\u12g4"}`,
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
}

func TestReadHookInput_MatchesEncodingJSONOnShortMembers(t *testing.T) {
	for _, input := range hookSeeds {
		assertMatchesStdlib(t, input)
	}
}

func FuzzReadHookInputMatchesEncodingJSON(f *testing.F) {
	for _, input := range hookSeeds {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > maxMember {
			t.Skip("a member may be too long to keep, where the two differ by design")
		}
		assertMatchesStdlib(t, input)
	})
}

// A member too long to keep is malformed or not exactly as encoding/json
// would find it: malformed input is an error, never a source of fields.
func TestReadHookInput_MalformedLongMemberIsAnError(t *testing.T) {
	pad := strings.Repeat("a", maxMember+100)
	for name, input := range map[string]string{
		"bad scalar":         `{"x":` + pad + `"q,"session_id":"leak"}`,
		"letters":            `{"x":` + pad + `,"session_id":"leak"}`,
		"mismatched":         `{"x":["` + pad + `"},"session_id":"leak"}`,
		"deep nesting":       `{"x":` + strings.Repeat("[", 40000) + strings.Repeat("]", 40000) + `,"session_id":"leak"}`,
		"control char":       `{"x":"` + pad + "\x01" + `","session_id":"leak"}`,
		"bad escape":         `{"x":"` + pad + `\q","session_id":"leak"}`,
		"short unicode":      `{"x":"` + pad + `\u12","session_id":"leak"}`,
		"missing comma":      `{"x":[1 "` + pad + `"],"session_id":"leak"}`,
		"trailing comma":     `{"x":["` + pad + `",],"session_id":"leak"}`,
		"bad number":         `{"x":[01,"` + pad + `"],"session_id":"leak"}`,
		"non-string key":     `{"x":{1:"` + pad + `"},"session_id":"leak"}`,
		"missing colon":      `{"x":{"a" "` + pad + `"},"session_id":"leak"}`,
		"unterminated":       `{"x":"` + pad,
		"unclosed container": `{"x":[["` + pad + `"]`,
		"object closed by ]": `{"x":{"a":"` + pad + `"],"session_id":"leak"}`,
		"colon in an array":  `{"x":["` + pad + `":1],"session_id":"leak"}`,
		"comma after a key":  `{"x":{"a","` + pad + `":1},"session_id":"leak"}`,
		"bad literal":        `{"x":["` + pad + `",nul],"session_id":"leak"}`,
		"literal run on":     `{"x":["` + pad + `",truex],"session_id":"leak"}`,
		"double colon":       `{"x":{"a"::"` + pad + `"},"session_id":"leak"}`,
		"exponent cut short": `{"x":["` + pad + `",1e+],"session_id":"leak"}`,
	} {
		t.Run(name, func(t *testing.T) {
			in, err := protocol.ReadHookInput(strings.NewReader(input))

			if err == nil || in != (protocol.HookInput{}) {
				t.Errorf("got %+v, %v; want an error and no fields", in, err)
			}
		})
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

// An allowlisted field too long to forward reads as empty, so the rest of
// the event still goes.
func TestReadHookInput_FieldTooLongToForwardIsEmpty(t *testing.T) {
	input := `{"session_id":"s1","cwd":"` + strings.Repeat("d", maxMember) + `","tool_name":"Bash"}`

	got, err := protocol.ReadHookInput(strings.NewReader(input))

	if err != nil || got != (protocol.HookInput{SessionID: "s1", ToolName: "Bash"}) {
		t.Errorf("got %+v, %v; want s1's Bash call without its cwd", got, err)
	}
}

// As in encoding/json, the last occurrence of a key wins, in any letter
// case; when that one is too long to keep, the field is left empty.
func TestReadHookInput_LongLastDuplicateEmptiesTheField(t *testing.T) {
	pad := strings.Repeat("a", maxMember+100)
	for name, input := range map[string]string{
		"string":      `{"session_id":"s1","session_id":"` + pad + `","tool_name":"Bash"}`,
		"letter case": `{"session_id":"s1","SESSION_ID":"` + pad + `","tool_name":"Bash"}`,
		"nested":      `{"session_id":"s1","session_id":["` + pad + `"],"tool_name":"Bash"}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := protocol.ReadHookInput(strings.NewReader(input))

			if err != nil || got != (protocol.HookInput{ToolName: "Bash"}) {
				t.Errorf("got %+v, %v; want the session left empty", got, err)
			}
		})
	}
	got, err := protocol.ReadHookInput(strings.NewReader(`{"session_id":"` + pad + `","session_id":"s2"}`))
	if err != nil || got.SessionID != "s2" {
		t.Errorf("a short last duplicate: got %+v, %v; want s2", got, err)
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

// genJSON builds random valid JSON, strings rich in escapes and brackets.
type genJSON struct{ r *rand.Rand }

var genStringParts = []string{
	"a", "b", " ", "{", "}", "[", "]", ",", ":", `\\`, `\"`, `\n`, `\u00e9`, `\/`, "A", "é", "日", "😀",
}

func (g genJSON) str(n int) string {
	var b strings.Builder
	b.WriteByte('"')
	for b.Len() < n {
		b.WriteString(genStringParts[g.r.IntN(len(genStringParts))])
	}
	switch g.r.IntN(4) {
	case 0:
		b.WriteString(strings.Repeat(`\\`, g.r.IntN(5)))
	case 1:
		b.WriteString(`\"`)
	}
	b.WriteByte('"')
	return b.String()
}

func (g genJSON) ws() string { return []string{"", "", " ", "\n", "\t", "\r\n "}[g.r.IntN(6)] }

func (g genJSON) value(depth, size int) string {
	kind := g.r.IntN(7)
	if depth > 4 {
		kind = g.r.IntN(3)
	}
	switch kind {
	case 0:
		return g.str(size)
	case 1:
		return []string{"true", "false", "null", "0", "-0", "-1.5e+3", "12345678901234567890", "0.25E-2"}[g.r.IntN(8)]
	case 2:
		return g.str(g.r.IntN(20))
	case 3, 4:
		parts := make([]string, g.r.IntN(4))
		for i := range parts {
			parts[i] = g.ws() + g.value(depth+1, size/(len(parts)+1)) + g.ws()
		}
		return "[" + strings.Join(parts, ",") + "]"
	default:
		parts := make([]string, g.r.IntN(4))
		for i := range parts {
			parts[i] = g.ws() + g.str(g.r.IntN(10)) + g.ws() + ":" + g.ws() + g.value(depth+1, size/(len(parts)+1)) + g.ws()
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
}

var genKeys = []string{
	`"session_id"`, `"session_id"`, `"Session_ID"`, `"hook_event_name"`, `"cwd"`, `"tool_name"`,
	`"tool_response"`, `"x"`,
}

// On valid JSON with members on both sides of the cap, ReadHookInput gives
// what encoding/json gives once each long member's value is "": a long key
// names no field and goes, and a long value leaves its field empty, which
// for an omitempty string field is the field absent.
func TestReadHookInput_MatchesEncodingJSONAcrossTheCap(t *testing.T) {
	g := genJSON{rand.New(rand.NewPCG(1, 2))} //nolint:gosec // G404: a fixed seed, for inputs that repeat from run to run
	for range 400 {
		var full, model []string
		for range g.r.IntN(6) + 1 {
			key := genKeys[g.r.IntN(len(genKeys))]
			if g.r.IntN(30) == 0 {
				key = g.str(maxMember - 50 + g.r.IntN(100))
			}
			size := g.r.IntN(64)
			if g.r.IntN(3) == 0 {
				size = maxMember - 300 + g.r.IntN(600)
			}
			value := g.value(0, size)
			full = append(full, g.ws()+key+g.ws()+":"+g.ws()+value+g.ws())
			switch {
			case len(key) > maxMember:
			case len(value) > maxMember:
				model = append(model, key+`:""`)
			default:
				model = append(model, key+":"+value)
			}
		}
		input := g.ws() + "{" + strings.Join(full, ",") + "}" + g.ws()
		if !json.Valid([]byte(input)) {
			t.Fatalf("generated invalid JSON %.300q", input)
		}

		want, wantErr := stdlibHookInput("{" + strings.Join(model, ",") + "}")
		got, gotErr := protocol.ReadHookInput(strings.NewReader(input))

		if (wantErr == nil) != (gotErr == nil) || got != want {
			t.Fatalf("ReadHookInput(%.400q) = %+v, %v; want %+v, %v", input, got, gotErr, want, wantErr)
		}
	}
}

func TestReadHookInput_MutatedInputNeitherPanicsNorHangs(t *testing.T) {
	g := genJSON{rand.New(rand.NewPCG(3, 4))} //nolint:gosec // G404: a fixed seed, for inputs that repeat from run to run
	const alphabet = `"\{}[],: a0`
	for range 300 {
		input := []byte(`{"session_id":"s1","tool_response":` + g.value(0, maxMember-200+g.r.IntN(400)) + `,"cwd":"x"}`)
		for range g.r.IntN(8) {
			input[g.r.IntN(len(input))] = alphabet[g.r.IntN(len(alphabet))]
		}
		if g.r.IntN(4) == 0 {
			input = input[:g.r.IntN(len(input))]
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = protocol.ReadHookInput(bytes.NewReader(input))
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("ReadHookInput hung on %.300q", input)
		}
	}
}

// A long string is read in slices of the read buffer's size. An escape split
// across two of them still decides whether the quote after it closes the
// string. The lengths tried put the end of the escape on every side of the
// first slice boundary, wherever in the buffer the string starts.
func TestReadHookInput_EscapesAcrossReadBoundaries(t *testing.T) {
	unicodeEscape := `\` + `u00e9`
	tails := []string{`\"`, `\\`, `\\\"`, `\\\\`, `\"\\`, unicodeEscape, unicodeEscape + `\"`}
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
