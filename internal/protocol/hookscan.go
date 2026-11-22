package protocol

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

const (
	// maxHookMember is the longest member key or value of a hook payload
	// that is kept, as written. An envelope's whole payload is capped at
	// MaxPayloadBytes, so a longer member is never one Loadline could
	// forward: it is content, such as a tool's input or output.
	maxHookMember = MaxPayloadBytes
	// maxHookKept bounds the members kept in all. Claude Code sends about a
	// dozen beside its content fields, each a few hundred bytes at most.
	maxHookKept = 1 << 20
	// maxHookNesting is how deeply arrays and objects may nest, the
	// payload's own object included. It is encoding/json's limit.
	maxHookNesting = 10000
	hookReadBuffer = 16 << 10
)

var (
	errHookTooManyFields = fmt.Errorf("the object holds over %d bytes of fields", maxHookKept)
	errHookTooDeep       = fmt.Errorf("the value nests over %d levels deep", maxHookNesting)
)

// shortMembers reads the JSON object at the start of r and returns it as a
// new object of the members short enough to keep, checking the whole input
// as encoding/json does. A member whose key is too long is left out: no
// field has a name that long. A member whose value is too long keeps its
// key with an empty string, so that, as in encoding/json, it still replaces
// an earlier member of the same name. Reading stops at the object's closing
// brace.
func shortMembers(r *bufio.Reader) ([]byte, error) {
	if err := expect(r, '{'); err != nil {
		return nil, err
	}
	kept := []byte{'{'}
	b, err := nextToken(r)
	if err != nil {
		return nil, err
	}
	if b == '}' {
		return append(kept, '}'), nil
	}
	for {
		if b != '"' {
			return nil, fmt.Errorf("invalid character %q where an object key belongs", b)
		}
		key := capture{text: []byte{'"'}}
		if err := scanString(r, &key); err != nil {
			return nil, err
		}
		if err := expect(r, ':'); err != nil {
			return nil, err
		}
		value, err := scanValue(r)
		if err != nil {
			return nil, err
		}

		switch {
		case key.long:
		case value.long:
			kept = appendMember(kept, key.text, []byte(`""`))
		default:
			kept = appendMember(kept, key.text, value.text)
		}
		if len(kept) > maxHookKept {
			return nil, errHookTooManyFields
		}

		switch b, err = nextToken(r); {
		case err != nil:
			return nil, err
		case b == '}':
			return append(kept, '}'), nil
		case b != ',':
			return nil, fmt.Errorf("invalid character %q after an object member", b)
		}
		if b, err = nextToken(r); err != nil {
			return nil, err
		}
	}
}

func appendMember(kept, key, value []byte) []byte {
	if len(kept) > 1 {
		kept = append(kept, ',')
	}
	return append(append(append(kept, key...), ':'), value...)
}

// capture keeps the text of a key or value as it is read, up to
// maxHookMember bytes; past that it keeps nothing.
type capture struct {
	text []byte
	long bool
}

func (c *capture) add(p []byte) {
	if c.long {
		return
	}
	if len(c.text)+len(p) > maxHookMember {
		c.long, c.text = true, nil
		return
	}
	c.text = append(c.text, p...)
}

func (c *capture) addByte(b byte) { c.add([]byte{b}) }

// scanValue reads the value of a member of the payload's object.
func scanValue(r *bufio.Reader) (capture, error) {
	var c capture
	first, err := nextToken(r)
	if err != nil {
		return c, err
	}
	c.addByte(first)
	switch {
	case first == '"':
		err = scanString(r, &c)
	case first == '{' || first == '[':
		err = scanNested(r, &c, first)
	case isScalarByte(first):
		err = scanScalar(r, &c, first)
	default:
		err = fmt.Errorf("invalid character %q where a value belongs", first)
	}
	return c, err
}

// nestedState is what a nested object or array accepts next.
type nestedState uint8

const (
	wantValueOrClose nestedState = iota // just after '['
	wantKeyOrClose                      // just after '{'
	wantValue                           // after ':', or after ',' in an array
	wantKey                             // after ',' in an object
	wantColon                           // after an object key
	wantCommaOrClose                    // after a value
)

// scanNested reads the rest of an object or array whose opening bracket,
// first, is read, to its matching close, checking its grammar. It keeps an
// explicit stack of the open brackets rather than recursing, and refuses
// nesting deeper than maxHookNesting, the payload's own object counted.
func scanNested(r *bufio.Reader, c *capture, first byte) error {
	stack := []byte{first}
	state := opened(first)
	for len(stack) > 0 {
		b, err := r.ReadByte()
		if err != nil {
			return unexpectedEnd(err)
		}
		c.addByte(b)
		top := stack[len(stack)-1]
		switch {
		case isSpace(b):
		case b == '"' && (state == wantKey || state == wantKeyOrClose):
			if err := scanString(r, c); err != nil {
				return err
			}
			state = wantColon
		case b == ':' && state == wantColon:
			state = wantValue
		case b == ',' && state == wantCommaOrClose:
			state = wantValue
			if top == '{' {
				state = wantKey
			}
		case b == '}' && top == '{' && (state == wantKeyOrClose || state == wantCommaOrClose),
			b == ']' && top == '[' && (state == wantValueOrClose || state == wantCommaOrClose):
			stack = stack[:len(stack)-1]
			state = wantCommaOrClose
		case state != wantValue && state != wantValueOrClose:
			return fmt.Errorf("invalid character %q in a nested value", b)
		case b == '{' || b == '[':
			// The payload's own object encloses the member.
			if 1+len(stack)+1 > maxHookNesting {
				return errHookTooDeep
			}
			stack = append(stack, b)
			state = opened(b)
		case b == '"':
			if err := scanString(r, c); err != nil {
				return err
			}
			state = wantCommaOrClose
		case isScalarByte(b):
			if err := scanScalar(r, c, b); err != nil {
				return err
			}
			state = wantCommaOrClose
		default:
			return fmt.Errorf("invalid character %q where a value belongs", b)
		}
	}
	return nil
}

func opened(bracket byte) nestedState {
	if bracket == '{' {
		return wantKeyOrClose
	}
	return wantValueOrClose
}

// scanString reads the rest of a string whose opening quote is read,
// checking its escapes and that it holds no control character. It works
// through the read buffer a slice at a time, skipping ahead over the bytes
// that need no thought.
func scanString(r *bufio.Reader, c *capture) error {
	var s stringCheck
	for {
		if r.Buffered() == 0 {
			if _, err := r.Peek(1); err != nil {
				return unexpectedEnd(err)
			}
		}
		data, err := r.Peek(r.Buffered())
		if err != nil {
			return err
		}
		n, closed, err := s.scan(data)
		c.add(data[:n])
		if _, discardErr := r.Discard(n); discardErr != nil {
			return discardErr
		}
		if err != nil || closed {
			return err
		}
	}
}

// stringCheck follows a string's contents from one slice of it to the next.
type stringCheck struct {
	backslash bool
	// hex is how many hex digits of a \u escape are still to come.
	hex int
}

// scan follows data, returning how much of it belongs to the string and
// whether the string closed within it.
func (s *stringCheck) scan(data []byte) (n int, closed bool, err error) {
	for i := 0; i < len(data); i++ {
		b := data[i]
		switch {
		case s.hex > 0:
			if !isHex(b) {
				return i, false, fmt.Errorf("invalid character %q in a \\u escape", b)
			}
			s.hex--
		case s.backslash:
			s.backslash = false
			switch b {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				s.hex = 4
			default:
				return i, false, fmt.Errorf("invalid escape %q in a string", b)
			}
		case b == '"':
			return i + 1, true, nil
		case b == '\\':
			s.backslash = true
		case b < 0x20:
			return i, false, fmt.Errorf("control character %q in a string", b)
		default:
			for i+1 < len(data) && plainStringByte(data[i+1]) {
				i++
			}
		}
	}
	return len(data), false, nil
}

func plainStringByte(b byte) bool { return b >= 0x20 && b != '"' && b != '\\' }

func isHex(b byte) bool {
	return ('0' <= b && b <= '9') || ('a' <= b && b <= 'f') || ('A' <= b && b <= 'F')
}

// scanScalar reads the rest of a number, true, false or null whose first
// byte is read: the run of bytes a scalar may hold, checked as one.
func scanScalar(r *bufio.Reader, c *capture, first byte) error {
	check := newScalarCheck(first)
	for {
		b, err := r.ReadByte()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if !isScalarByte(b) {
			if err := r.UnreadByte(); err != nil {
				return err
			}
			break
		}
		c.addByte(b)
		if !check.step(b) {
			return fmt.Errorf("invalid character %q in a number or literal", b)
		}
	}
	if !check.complete() {
		return errors.New("an incomplete number or literal")
	}
	return nil
}

// isScalarByte reports whether b may be part of a number or literal.
func isScalarByte(b byte) bool {
	return ('0' <= b && b <= '9') || ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z') ||
		b == '-' || b == '+' || b == '.'
}

// numberState is how far a number has got, after the bytes read so far.
type numberState uint8

const (
	numberBad      numberState = iota
	numberSign                 // "-"
	numberZero                 // a leading 0
	numberInt                  // digits from 1 to 9 first
	numberDot                  // "."
	numberFraction             // digits after "."
	numberE                    // "e" or "E"
	numberESign                // the exponent's sign
	numberExponent             // the exponent's digits
)

// scalarCheck follows a number or a literal byte by byte, as encoding/json
// reads one.
type scalarCheck struct {
	literal string
	matched int
	number  numberState
}

func newScalarCheck(first byte) scalarCheck {
	switch first {
	case 't':
		return scalarCheck{literal: "true", matched: 1}
	case 'f':
		return scalarCheck{literal: "false", matched: 1}
	case 'n':
		return scalarCheck{literal: "null", matched: 1}
	case '-':
		return scalarCheck{number: numberSign}
	case '0':
		return scalarCheck{number: numberZero}
	}
	if '1' <= first && first <= '9' {
		return scalarCheck{number: numberInt}
	}
	return scalarCheck{number: numberBad}
}

func (s *scalarCheck) step(b byte) bool {
	if s.literal != "" {
		if s.matched < len(s.literal) && b == s.literal[s.matched] {
			s.matched++
			return true
		}
		return false
	}
	digit := '0' <= b && b <= '9'
	switch {
	case s.number == numberSign && b == '0':
		s.number = numberZero
	case s.number == numberSign && digit:
		s.number = numberInt
	case (s.number == numberInt || s.number == numberFraction || s.number == numberExponent) && digit:
	case (s.number == numberZero || s.number == numberInt) && b == '.':
		s.number = numberDot
	case (s.number == numberDot) && digit:
		s.number = numberFraction
	case (s.number == numberZero || s.number == numberInt || s.number == numberFraction) && (b == 'e' || b == 'E'):
		s.number = numberE
	case s.number == numberE && (b == '+' || b == '-'):
		s.number = numberESign
	case (s.number == numberE || s.number == numberESign) && digit:
		s.number = numberExponent
	default:
		s.number = numberBad
		return false
	}
	return true
}

func (s *scalarCheck) complete() bool {
	if s.literal != "" {
		return s.matched == len(s.literal)
	}
	switch s.number {
	case numberZero, numberInt, numberFraction, numberExponent:
		return true
	default:
		return false
	}
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// nextToken reads past spaces and returns the byte after them.
func nextToken(r *bufio.Reader) (byte, error) {
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, unexpectedEnd(err)
		}
		if !isSpace(b) {
			return b, nil
		}
	}
}

// expect reads past spaces and then want, failing on anything else.
func expect(r *bufio.Reader, want byte) error {
	got, err := nextToken(r)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("invalid character %q where %q belongs", got, want)
	}
	return nil
}

func unexpectedEnd(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
