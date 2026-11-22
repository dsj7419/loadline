package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/protocol"
)

// hookInputSeeds are payloads whose every member fits, so readHookInput must
// agree with protocol.ReadHookInput on each.
var hookInputSeeds = []string{
	`{"session_id":"s1","hook_event_name":"Stop"}`,
	` { "session_id" : "s1" , "cwd" : "C:\\Users\\x" } `,
	`{}`,
	`{ }`,
	`{"session_id":"s\"1","tool_name":"a\\\\","agent_id":"\\\""}`,
	`{"tool_input":{"a":["}","{",{"b":"]\"["}]},"session_id":"s1"}`,
	`{"n":-1.5e+3,"t":true,"f":false,"z":null,"session_id":"s1"}`,
	`{"session_id":1,"hook_event_name":["x"],"cwd":{"a":1}}`,
	`{"session_id":"\u00e9\ud83d\ude00","source":"startup"}`,
	`{"session_id":"s1","session_id":"s2"}`,
	`{"session_id":"s1"} trailing`,
	"{\"session_id\":\"s1\"}\n{\"session_id\":\"s2\"}",
	`{"session_id":"s1",}`,
	`{"session_id" "s1"}`,
	`{"session_id":"s1" "cwd":"x"}`,
	`{"session_id":}`,
	`{"session_id":tru}`,
	`{session_id:"s1"}`,
	`{"session_id":"s1"`,
	`{"session_id":"s1`,
	`{"a":[1,2}`,
	`[{"session_id":"s1"}]`,
	`null`,
	`"s1"`,
	``,
	"{\"session_id\":\"line\nbreak\"}",
	"\ufeff{\"session_id\":\"s1\"}",
	"{\"session_id\":\"s1\"\f}",
	`{"a":` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `,"session_id":"s1"}`,
}

// assertAgrees checks readHookInput against protocol.ReadHookInput on input
// whose members all fit: both fail, or both give the same fields.
func assertAgrees(t *testing.T, input string) {
	t.Helper()
	want, wantErr := protocol.ReadHookInput(strings.NewReader(input))
	got, gotErr := readHookInput(strings.NewReader(input))
	if (wantErr == nil) != (gotErr == nil) {
		t.Fatalf("readHookInput(%.200q) error = %v, protocol.ReadHookInput error = %v; want both or neither", input, gotErr, wantErr)
	}
	if got != want {
		t.Errorf("readHookInput(%.200q) = %+v, want %+v", input, got, want)
	}
}

func TestReadHookInput_AgreesWithTheProtocolDecoder(t *testing.T) {
	for _, input := range hookInputSeeds {
		assertAgrees(t, input)
	}
}

func FuzzReadHookInput(f *testing.F) {
	for _, input := range hookInputSeeds {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > maxHookMember {
			t.Skip("a member may be too long to keep, and the decoders then differ by design")
		}
		assertAgrees(t, input)
	})
}

// A long string is read in slices of the read buffer's size. A run of
// backslashes split across two of them still decides whether the quote
// after it closes the string. The lengths tried put the end of the run on
// every side of the first slice boundary, wherever in the buffer the string
// starts.
func TestReadHookInput_EscapesAcrossReadBoundaries(t *testing.T) {
	tails := []string{`\"`, `\\`, `\\\"`, `\\\\`, `\"\\`}
	const prefix = `{"tool_response":"`
	for _, tail := range tails {
		for length := hookReadBuffer - 40; length <= hookReadBuffer+40; length++ {
			body := strings.Repeat("x", length-len(tail)) + tail
			input := prefix + body + `","session_id":"s1","tool_name":"Bash"}`

			got, err := readHookInput(strings.NewReader(input))

			if err != nil || got.SessionID != "s1" || got.ToolName != "Bash" {
				t.Fatalf("tail %q at length %d: got %+v, %v; want s1's Bash call", tail, length, got, err)
			}
		}
	}
}

// Members too long to keep are skipped, whatever their kind, and the short
// ones around them still decode.
func TestReadHookInput_SkipsLongMembers(t *testing.T) {
	long := strings.Repeat("y", maxHookMember)
	for name, member := range map[string]string{
		"string":      `"tool_response":"` + long + `"`,
		"object":      `"tool_response":{"output":"` + long + `","nested":[{"a":"}"}]}`,
		"array":       `"tool_response":["` + long + `"]`,
		"number":      `"tool_response":1` + strings.Repeat("0", maxHookMember),
		"key":         `"` + long + `":"x"`,
		"allowlisted": `"cwd":"` + long + `"`,
	} {
		t.Run(name, func(t *testing.T) {
			input := `{"session_id":"s1",` + member + `,"tool_name":"Bash"}`

			got, err := readHookInput(strings.NewReader(input))

			if err != nil || got != (protocol.HookInput{SessionID: "s1", ToolName: "Bash"}) {
				t.Errorf("got %+v, %v; want s1's Bash call alone", got, err)
			}
		})
	}
}

func TestReadHookInput_RefusesTooManyShortMembers(t *testing.T) {
	var input bytes.Buffer
	input.WriteString(`{"session_id":"s1"`)
	member := `,"m":"` + strings.Repeat("z", maxHookMember-16) + `"`
	for input.Len() <= maxHookKept {
		input.WriteString(member)
	}
	input.WriteString("}")

	if _, err := readHookInput(&input); err == nil {
		t.Error("read a hook input with over 1 MiB of short members, want an error")
	}
}
