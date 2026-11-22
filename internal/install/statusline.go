package install

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// The pieces of the status-line commands install writes. The user's own
// command stays in the settings file behind the tap prefix, so anything else
// that reads the same file gets the same status line.
//
//	bash:       "<exe>" tap | <original>
//	PowerShell: & "<exe>" tap --run '<original, single quotes doubled>'
//	no status line of the user's own: "<exe>" tap --print (with & in PowerShell)
//
// PowerShell would hand the JSON on stdin to the original command
// re-encoded if it were piped, so there tap runs the command itself.
const (
	powerShellCall = "& "
	pipeSuffix     = " tap | "
	runSuffix      = " tap --run '"
	printSuffix    = " tap --print"
)

// tapCommand is a status-line command that runs through loadline tap.
type tapCommand struct {
	// print means tap prints a status line of its own because the user had
	// none.
	print bool
	// original is the user's own command.
	original []char
}

// char is one character of a JSON string literal together with the bytes
// that encode it there. Moving a user's command in and out of the tap prefix
// character by character keeps whatever escapes the file used, so reverting
// restores the original bytes.
type char struct {
	raw string
	r   rune
}

// literal returns the command as a JSON string literal, for shell.
func (t tapCommand) literal(exe string, shell Shell) string {
	launcher := `"` + exe + `"`
	if shell == ShellPowerShell {
		launcher = powerShellCall + launcher
	}

	var b strings.Builder
	b.WriteByte('"')
	switch {
	case t.print:
		b.WriteString(escapeJSON(launcher + printSuffix))
	case shell == ShellPowerShell:
		b.WriteString(escapeJSON(launcher + runSuffix))
		for _, c := range t.original {
			b.WriteString(c.raw)
			if isPowerShellQuote(c.r) {
				b.WriteString(c.raw)
			}
		}
		b.WriteByte('\'')
	default:
		b.WriteString(escapeJSON(launcher + pipeSuffix))
		b.WriteString(raw(t.original))
	}
	b.WriteByte('"')
	return b.String()
}

// originalLiteral returns the user's own command as the JSON string literal
// it was before install.
func (t tapCommand) originalLiteral() string {
	return `"` + raw(t.original) + `"`
}

// parseTapCommand recognizes a status-line command, given as its JSON string
// literal, that runs Loadline's tap in any of the forms install writes, for
// any Loadline path or exe.
func parseTapCommand(literal, exe string) (tapCommand, bool) {
	chars := splitString(literal)
	rest, powerShell := cutText(chars, powerShellCall+`"`)
	if !powerShell {
		var quoted bool
		if rest, quoted = cutText(chars, `"`); !quoted {
			return tapCommand{}, false
		}
	}
	end := slices.IndexFunc(rest, func(c char) bool { return c.r == '"' })
	if end < 0 || !isLoadlinePath(text(rest[:end]), exe) {
		return tapCommand{}, false
	}
	rest = rest[end+1:]

	if text(rest) == printSuffix {
		return tapCommand{print: true}, true
	}
	if powerShell {
		quoted, ok := cutText(rest, runSuffix)
		if !ok {
			return tapCommand{}, false
		}
		original, ok := unquotePowerShell(quoted)
		return tapCommand{original: original}, ok
	}
	original, ok := cutText(rest, pipeSuffix)
	return tapCommand{original: original}, ok
}

// unquotePowerShell reads the rest of a single-quoted PowerShell string,
// after its opening quote, which must close at the very end. PowerShell
// takes the typographic single quotes as quotes too, and a doubled quote of
// any kind stands for the second one.
func unquotePowerShell(quoted []char) ([]char, bool) {
	original := []char{}
	for i := 0; i < len(quoted); i++ {
		switch {
		case !isPowerShellQuote(quoted[i].r):
			original = append(original, quoted[i])
		case i == len(quoted)-1:
			return original, true
		case isPowerShellQuote(quoted[i+1].r):
			original = append(original, quoted[i+1])
			i++
		default:
			return nil, false
		}
	}
	return nil, false
}

func isPowerShellQuote(r rune) bool {
	switch r {
	case '\'', '‘', '’', '‚', '‛':
		return true
	default:
		return false
	}
}

// cutText reports whether chars start with prefix, and returns the rest.
func cutText(chars []char, prefix string) ([]char, bool) {
	i := 0
	for _, r := range prefix {
		if i == len(chars) || chars[i].r != r {
			return nil, false
		}
		i++
	}
	return chars[i:], true
}

func text(chars []char) string {
	var b strings.Builder
	for _, c := range chars {
		b.WriteRune(c.r)
	}
	return b.String()
}

func raw(chars []char) string {
	var b strings.Builder
	for _, c := range chars {
		b.WriteString(c.raw)
	}
	return b.String()
}

// splitString splits a valid JSON string literal into its characters.
func splitString(literal string) []char {
	content := strings.TrimSuffix(strings.TrimPrefix(literal, `"`), `"`)
	var chars []char
	for len(content) > 0 {
		r, size := decodeChar(content)
		chars = append(chars, char{raw: content[:size], r: r})
		content = content[size:]
	}
	return chars
}

var shortEscapes = map[byte]rune{
	'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t',
}

// decodeChar decodes the character at the start of a JSON string's content
// and reports how many bytes encode it. A \u escape of a surrogate pair is
// one character.
func decodeChar(s string) (rune, int) {
	if s[0] != '\\' || len(s) < 2 {
		return utf8.DecodeRuneInString(s)
	}
	if s[1] != 'u' {
		if r, ok := shortEscapes[s[1]]; ok {
			return r, 2
		}
		return utf8.RuneError, 2
	}
	r, ok := hexRune(s[2:])
	if !ok {
		return utf8.RuneError, 2
	}
	if utf16.IsSurrogate(r) && len(s) >= 12 && s[6:8] == `\u` {
		if low, ok := hexRune(s[8:]); ok {
			if pair := utf16.DecodeRune(r, low); pair != utf8.RuneError {
				return pair, 12
			}
		}
	}
	return r, 6
}

func hexRune(s string) (rune, bool) {
	if len(s) < 4 {
		return 0, false
	}
	n, err := strconv.ParseUint(s[:4], 16, 16)
	return rune(n), err == nil
}

// decodeString returns the value of a valid JSON string literal.
func decodeString(literal string) string {
	return gjson.Parse(literal).Str
}
