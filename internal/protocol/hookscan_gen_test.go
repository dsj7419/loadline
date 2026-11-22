package protocol_test

import (
	"bytes"
	"encoding/json"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/protocol"
)

// genJSON builds random valid JSON for hook payloads: strings rich in
// escapes and brackets, and arrays, objects and nestings that run past the
// member cap.
type genJSON struct{ r *rand.Rand }

func newGen(seed uint64) genJSON {
	return genJSON{rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))} //nolint:gosec // G404: seeded, so that a failure repeats
}

var genStringParts = []string{
	"a", " ", "{", "}", "[", "]", ",", ":", `\\`, `\"`, `\n`, bu + "00e9", bu + "D83D" + bu + "DE00", `\/`,
	"\xc3\xa9", "\xf0\x9f\x98\x80", "\x7f",
}

func (g genJSON) str(n int) string {
	var b strings.Builder
	b.WriteByte('"')
	for b.Len() < n {
		b.WriteString(genStringParts[g.r.IntN(len(genStringParts))])
	}
	b.WriteByte('"')
	return b.String()
}

func (g genJSON) ws() string { return []string{"", "", " ", "\n", "\t", "\r\n "}[g.r.IntN(6)] }

func (g genJSON) scalar() string {
	return []string{
		"true", "false", "null", "0", "-0", "-1.5e+3", "12345678901234567890", "0.25E-2", "1e9", "-0.0e-0",
	}[g.r.IntN(10)]
}

// small returns a short value of any kind.
func (g genJSON) small(depth int) string {
	kind := g.r.IntN(6)
	if depth > 3 {
		kind = g.r.IntN(2)
	}
	switch kind {
	case 0:
		return g.scalar()
	case 1:
		return g.str(g.r.IntN(24))
	case 2, 3:
		parts := make([]string, g.r.IntN(4))
		for i := range parts {
			parts[i] = g.ws() + g.small(depth+1) + g.ws()
		}
		return "[" + strings.Join(parts, ",") + "]"
	default:
		parts := make([]string, g.r.IntN(4))
		for i := range parts {
			parts[i] = g.ws() + g.str(g.r.IntN(6)) + g.ws() + ":" + g.ws() + g.small(depth+1) + g.ws()
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
}

// big returns a value of at least size bytes: a long string, a long array
// or object of small values, or a deep nesting around one of those.
func (g genJSON) big(size, depth int) string {
	switch kind := g.r.IntN(5); {
	case kind == 0 || depth > 30:
		return g.str(size)
	case kind <= 2:
		var b strings.Builder
		b.WriteByte('[')
		for b.Len() < size {
			if b.Len() > 1 {
				b.WriteString(g.ws() + "," + g.ws())
			}
			b.WriteString(g.small(0))
		}
		b.WriteByte(']')
		return b.String()
	case kind == 3:
		var b strings.Builder
		b.WriteByte('{')
		for b.Len() < size {
			if b.Len() > 1 {
				b.WriteByte(',')
			}
			b.WriteString(g.ws() + g.str(g.r.IntN(8)) + g.ws() + ":" + g.ws() + g.small(0) + g.ws())
		}
		b.WriteByte('}')
		return b.String()
	default:
		n := 1 + g.r.IntN(40)
		inner := g.big(size, depth+n)
		if g.r.IntN(2) == 0 {
			return strings.Repeat("[", n) + inner + strings.Repeat("]", n)
		}
		return strings.Repeat(`{"k":`, n) + inner + strings.Repeat("}", n)
	}
}

// genKeys are member keys, with allowlisted ones repeated, in other letter
// cases, escaped, and folded as encoding/json folds them.
var genKeys = []string{
	`"session_id"`, `"session_id"`, `"SESSION_ID"`, `"session_` + bu + `0069d"`, `"` + "\xc5\xbf" + `ession_id"`,
	`"hoo` + "\xe2\x84\xaa" + `_event_name"`, `"cwd"`, `"tool_name"`, `"tool_response"`, `"x"`,
}

// object returns a payload of a few members, many of them on either side
// of the member cap, some fields on either side of the field cap.
func (g genJSON) object() []byte {
	var members []string
	for range 1 + g.r.IntN(5) {
		key := genKeys[g.r.IntN(len(genKeys))]
		if g.r.IntN(25) == 0 {
			key = g.str(maxMember - 20 + g.r.IntN(40))
		}
		var value string
		switch g.r.IntN(4) {
		case 0:
			value = g.small(0)
		case 1:
			// Escapes shrink as a payload encodes them again, so the text
			// runs well past the field cap to put the field on both sides.
			value = g.str(maxField - 400 + g.r.IntN(2400))
		default:
			value = g.big(maxMember-200+g.r.IntN(maxMember), 0)
		}
		members = append(members, g.ws()+key+g.ws()+":"+g.ws()+value+g.ws())
	}
	return []byte(g.ws() + "{" + strings.Join(members, ",") + "}" + g.ws())
}

const mutationBytes = "\"\\{}[],: a0eE.-+tnul\x01\x00\xff\f/"

// mutate changes, adds or drops a few bytes of in, and sometimes cuts it
// short.
func (g genJSON) mutate(in []byte) []byte {
	out := bytes.Clone(in)
	for range 1 + g.r.IntN(3) {
		i := g.r.IntN(len(out))
		c := mutationBytes[g.r.IntN(len(mutationBytes))]
		switch g.r.IntN(3) {
		case 0:
			out[i] = c
		case 1:
			out = append(out[:i], append([]byte{c}, out[i:]...)...)
		default:
			out = append(out[:i], out[i+1:]...)
		}
	}
	if g.r.IntN(8) == 0 {
		out = out[:g.r.IntN(len(out))]
	}
	return out
}

func (g genJSON) reader(b []byte) io.Reader {
	names := []string{"whole", "whole", "one byte", "halves", "eof early"}
	return readers[names[g.r.IntN(len(names))]](b)
}

// On valid JSON with members on both sides of the caps, ReadHookInput gives
// what encoding/json gives with the caps applied.
func TestReadHookInput_MatchesEncodingJSONAcrossTheCap(t *testing.T) {
	g := newGen(1)
	for range 150 {
		input := g.object()
		if !json.Valid(input) {
			t.Fatalf("generated invalid JSON %.300q", input)
		}

		assertAgrees(t, input, g.reader(input))
	}
}

// Mutated payloads are mostly malformed, often deep inside a member too
// long to keep: each must be an error exactly when encoding/json finds the
// first value malformed, and give its fields otherwise. None may hang the
// reader.
func TestReadHookInput_MutatedInputMatchesEncodingJSON(t *testing.T) {
	g := newGen(2)
	type result struct {
		in  protocol.HookInput
		err error
	}
	for range 300 {
		input := g.mutate(g.object())
		reader := g.reader(input)
		done := make(chan result, 1)
		go func() {
			in, err := protocol.ReadHookInput(reader)
			done <- result{in, err}
		}()
		select {
		case r := <-done:
			assertOracleResult(t, input, r.in, r.err)
		case <-time.After(10 * time.Second):
			t.Fatalf("ReadHookInput hung on %.300q", input)
		}
	}
}
