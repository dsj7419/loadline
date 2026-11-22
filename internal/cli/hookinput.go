package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/dsj7419/loadline/internal/protocol"
)

const (
	// maxHookMember is the longest member key or value of a hook payload
	// the hook keeps. An envelope's whole payload is capped at
	// protocol.MaxPayloadBytes, so a longer member is never one Loadline
	// could forward: it is content, such as a tool's input or output.
	maxHookMember = protocol.MaxPayloadBytes
	// maxHookKept bounds the members kept in all. Claude Code sends about a
	// dozen, each a few hundred bytes at most, beside its content fields.
	maxHookKept = 1 << 20

	hookReadBuffer = 64 << 10
)

var errHookTooManyFields = fmt.Errorf("hook input holds over %d bytes of fields", maxHookKept)

// readHookInput decodes the hook payload on r as protocol.ReadHookInput
// does, in memory that does not grow with the payload. The decoder there
// holds a whole value at once, and a tool's output can run to many
// megabytes; here the members too long to forward are read past without
// being kept, and protocol.ReadHookInput, the allowlist, decodes the rest.
func readHookInput(r io.Reader) (protocol.HookInput, error) {
	kept, err := shortMembers(bufio.NewReaderSize(r, hookReadBuffer))
	if err != nil {
		return protocol.HookInput{}, fmt.Errorf("read hook input: %w", err)
	}
	return protocol.ReadHookInput(bytes.NewReader(kept))
}

// shortMembers reads the JSON object at the start of r and returns it
// without the members whose key or value is longer than maxHookMember. It
// checks the object's own punctuation; the decoder checks what is kept.
func shortMembers(r *bufio.Reader) ([]byte, error) {
	if err := expect(r, '{'); err != nil {
		return nil, err
	}
	kept := []byte{'{'}
	if closed, err := consumeIf(r, '}'); err != nil || closed {
		return append(kept, '}'), err
	}
	for {
		if err := skipSpace(r); err != nil {
			return nil, err
		}
		if next, err := r.Peek(1); err != nil || next[0] != '"' {
			return nil, errors.New("an object key is not a string")
		}
		key, keyFits, err := scanValue(r)
		if err != nil {
			return nil, err
		}
		if err := expect(r, ':'); err != nil {
			return nil, err
		}
		if err := skipSpace(r); err != nil {
			return nil, err
		}
		value, valueFits, err := scanValue(r)
		if err != nil {
			return nil, err
		}
		if keyFits && valueFits {
			if len(kept) > 1 {
				kept = append(kept, ',')
			}
			kept = append(append(append(kept, key...), ':'), value...)
			if len(kept) > maxHookKept {
				return nil, errHookTooManyFields
			}
		}

		if err := skipSpace(r); err != nil {
			return nil, err
		}
		c, err := r.ReadByte()
		if err != nil {
			return nil, unexpectedEnd(err)
		}
		switch c {
		case ',':
		case '}':
			return append(kept, '}'), nil
		default:
			return nil, fmt.Errorf("unexpected %q after an object member", c)
		}
	}
}

// capture keeps the text of a value as it is read, up to maxHookMember
// bytes; past that it keeps nothing.
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

// scanValue reads one JSON value and returns its text, and whether it fits:
// the text of a value longer than maxHookMember is nil.
func scanValue(r *bufio.Reader) (text []byte, fits bool, err error) {
	var c capture
	first, err := r.ReadByte()
	if err != nil {
		return nil, false, unexpectedEnd(err)
	}
	c.addByte(first)
	switch first {
	case '"':
		err = scanString(r, &c)
	case '{', '[':
		err = scanNested(r, &c)
	default:
		err = scanScalar(r, &c, first)
	}
	if err != nil {
		return nil, false, err
	}
	return c.text, !c.long, nil
}

// scanString reads the rest of a string whose opening quote is read. It
// skips ahead to each quote, which ends the string unless an odd run of
// backslashes escapes it; a run can span reads.
func scanString(r *bufio.Reader, c *capture) error {
	backslashes := 0
	for {
		chunk, err := r.ReadSlice('"')
		c.add(chunk)
		if errors.Is(err, bufio.ErrBufferFull) {
			backslashes = trailingBackslashes(chunk, backslashes)
			continue
		}
		if err != nil {
			return unexpectedEnd(err)
		}
		if trailingBackslashes(chunk[:len(chunk)-1], backslashes)%2 == 0 {
			return nil
		}
		backslashes = 0
	}
}

// trailingBackslashes returns the length of the run of backslashes that ends
// p, counting the carried run before p when p is all backslashes.
func trailingBackslashes(p []byte, carried int) int {
	n := 0
	for n < len(p) && p[len(p)-1-n] == '\\' {
		n++
	}
	if n == len(p) {
		return carried + n
	}
	return n
}

// scanNested reads the rest of an object or array whose opening bracket is
// read, to its matching close. Depth is a count, not a recursion, so no
// nesting exhausts the stack.
func scanNested(r *bufio.Reader, c *capture) error {
	for depth := 1; depth > 0; {
		b, err := r.ReadByte()
		if err != nil {
			return unexpectedEnd(err)
		}
		c.addByte(b)
		switch b {
		case '"':
			if err := scanString(r, c); err != nil {
				return err
			}
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}
	return nil
}

// scanScalar reads the rest of a number, true, false or null whose first
// byte is read, up to the byte that ends it.
func scanScalar(r *bufio.Reader, c *capture, first byte) error {
	if isDelimiter(first) {
		return fmt.Errorf("unexpected %q where a value belongs", first)
	}
	for {
		b, err := r.ReadByte()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if isDelimiter(b) {
			return r.UnreadByte()
		}
		c.addByte(b)
	}
}

func isDelimiter(b byte) bool {
	switch b {
	case ',', ':', '}', ']', ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

func skipSpace(r *bufio.Reader) error {
	for {
		b, err := r.ReadByte()
		if err != nil {
			return unexpectedEnd(err)
		}
		switch b {
		case ' ', '\t', '\n', '\r':
		default:
			return r.UnreadByte()
		}
	}
}

// expect reads past spaces and then want, failing on anything else.
func expect(r *bufio.Reader, want byte) error {
	if err := skipSpace(r); err != nil {
		return err
	}
	got, err := r.ReadByte()
	if err != nil {
		return unexpectedEnd(err)
	}
	if got != want {
		return fmt.Errorf("unexpected %q where %q belongs", got, want)
	}
	return nil
}

// consumeIf reads past spaces and then b, when b comes next.
func consumeIf(r *bufio.Reader, b byte) (bool, error) {
	if err := skipSpace(r); err != nil {
		return false, err
	}
	next, err := r.Peek(1)
	if err != nil {
		return false, unexpectedEnd(err)
	}
	if next[0] != b {
		return false, nil
	}
	_, err = r.ReadByte()
	return true, err
}

func unexpectedEnd(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
