package install

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// The status-line commands install writes. The user's own command stays in
// the settings file behind the tap prefix, so anything else that reads the
// same file gets the same status line.
//
//	bash, a plain command: "<exe>" tap | <original>
//	bash, anything else:   "<exe>" tap | { <original>
//	                       }
//	PowerShell:            & "<exe>" tap --run-b64 '<original as unpadded base64url>'
//	no status line of the user's own: "<exe>" tap --print, after & in PowerShell
//
// The group keeps the meaning of a list, a script of several lines or a
// command that starts with a reserved word: the pipe feeds all of it, as the
// status-line JSON fed all of the original. The closing brace goes on a line
// of its own so a trailing comment cannot swallow it; an original that a
// group would still break is left alone. PowerShell gets the original out of
// band, because it would re-encode JSON piped between commands and its
// quoting of native arguments differs between versions; tap decodes the
// original and runs it.
const (
	// maxPowerShellCommand bounds the PowerShell form, in UTF-16 code units:
	// parts of Windows cut a command line at 8191 characters.
	maxPowerShellCommand = 8000

	powerShellCall = "& "
	tapPipe        = " tap | "
	groupOpen      = "{ "
	groupClose     = "\n}"
	runB64Open     = " tap --run-b64 '"
	runB64Close    = "'"
	printSuffix    = " tap --print"
)

// reservedWords start a compound command. After a pipe, some of them, such
// as time and !, mean something else or nothing at all.
var reservedWords = []string{"time", "!", "if", "for", "while", "until", "case", "select", "function", "coproc", "{", "[["}

// tapCommand is a status-line command that runs through loadline tap.
type tapCommand struct {
	// print means tap prints a status line of its own because the user had
	// none.
	print bool
	// original is the user's own command.
	original []char
}

// char is one character of a JSON string literal together with the bytes
// that encode it there. Moving a user's command in and out of the bash forms
// character by character keeps whatever escapes the file used, so reverting
// restores the original bytes.
type char struct {
	raw string
	r   rune
}

// tapMatch says how a status-line command relates to Loadline.
type tapMatch uint8

const (
	// foreign commands do not start with Loadline.
	foreign tapMatch = iota
	// tapped commands are in one of the forms install writes.
	tapped
	// edited commands start with Loadline but in no form install writes,
	// most likely because one was changed by hand. Loadline leaves them
	// alone rather than guess.
	edited
)

// literal returns the command for shell as a JSON string literal; a blank
// original gets the print form, never an empty group. When the command
// cannot be built it returns instead why, as a phrase that follows
// "statusLine.command".
func (t tapCommand) literal(exe string, shell Shell) (string, string) {
	launcher := `"` + exe + `"`
	if shell == ShellPowerShell {
		launcher = powerShellCall + launcher
	}
	original := text(t.original)

	var body string
	switch {
	case t.print || strings.TrimSpace(original) == "":
		body = escapeJSON(launcher + printSuffix)
	case hasNoCommand(original):
		// Only an earlier build wrapped such an original: a group of nothing
		// but comments does not parse, and the print form would lose the
		// user's lines, so it stays as it is for uninstall to restore.
		return "", "holds only comments, which Loadline no longer wraps; uninstall restores them"
	case shell == ShellPowerShell:
		command := launcher + runB64Open + base64.RawURLEncoding.EncodeToString([]byte(original)) + runB64Close
		if utf16Len(command) > maxPowerShellCommand {
			return "", fmt.Sprintf("would be longer than the %d characters a Windows command line safely holds "+
				"once wrapped for PowerShell", maxPowerShellCommand)
		}
		body = escapeJSON(command)
	case isPlainCommand(original):
		body = escapeJSON(launcher+tapPipe) + raw(t.original)
	case strings.HasSuffix(original, `\`):
		// Escaped or not: on Windows the command line that reaches Git Bash
		// may have one backslash where the file has two.
		return "", "ends in a backslash, which can join it to the line Loadline adds after it"
	case holdsHereDocument(original):
		return "", "holds a here-document, which would take the line Loadline adds after it as its own " +
			"unless it is terminated"
	default:
		body = escapeJSON(launcher+tapPipe+groupOpen) + raw(t.original) + escapeJSON(groupClose)
	}
	return `"` + body + `"`, ""
}

// hasNoCommand reports whether every line of command is blank or a comment,
// in bash and PowerShell alike.
func hasNoCommand(command string) bool {
	for line := range strings.SplitSeq(command, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			return false
		}
	}
	return true
}

// holdsHereDocument reports whether command uses the here-document operator
// << or <<-, as opposed to the here-string <<<. It errs towards yes: a << in
// quotes counts too.
func holdsHereDocument(command string) bool {
	for i := strings.Index(command, "<<"); i >= 0; i = strings.Index(command, "<<") {
		if !strings.HasPrefix(command[i:], "<<<") {
			return true
		}
		command = command[i+3:]
	}
	return false
}

// originalLiteral returns the user's own command as a JSON string literal:
// the bytes it had before install, in the bash forms.
func (t tapCommand) originalLiteral() string {
	return `"` + raw(t.original) + `"`
}

// isPlainCommand reports whether original can follow the pipe as it is: it
// holds no character the shell treats specially and does not start with a
// reserved word, so the pipe feeds all of it.
func isPlainCommand(original string) bool {
	trimmed := strings.TrimSpace(original)
	if trimmed == "" || strings.ContainsFunc(trimmed, func(r rune) bool { return !isPlainRune(r) }) {
		return false
	}
	first, _, _ := strings.Cut(trimmed, " ")
	return !slices.Contains(reservedWords, first)
}

func isPlainRune(r rune) bool {
	switch {
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		return true
	default:
		return strings.ContainsRune(` _./~:=+@%,-'"`, r)
	}
}

// parseTapCommand tells whether a status-line command, given as its JSON
// string literal, is one install wrote, for any Loadline path or exe, and if
// so recovers the user's own command.
func parseTapCommand(literal, exe string) (tapCommand, tapMatch) {
	chars := splitString(literal)
	rest, powerShell := cutText(chars, powerShellCall)
	if !powerShell {
		rest = chars
	}
	path, rest, quoted := cutLauncher(rest)
	if !isLoadlinePath(text(path), exe) {
		return tapCommand{}, foreign
	}
	if !quoted {
		return tapCommand{}, edited
	}
	if text(rest) == printSuffix {
		return tapCommand{print: true}, tapped
	}
	if powerShell {
		return parseRunB64(rest)
	}
	return parsePipe(rest)
}

// UsesPowerShellWrapper reports whether the status line in settings is the
// PowerShell form install writes around a user's own command, for which tap
// starts PowerShell to run that command on every refresh. Settings that are
// not valid JSON report false.
func UsesPowerShellWrapper(settings []byte, exe string) bool {
	body := bytes.TrimPrefix(settings, []byte(utf8BOM))
	if !gjson.ValidBytes(body) {
		return false
	}
	command := gjson.GetBytes(body, "statusLine.command")
	if command.Type != gjson.String || !strings.HasPrefix(command.Str, powerShellCall) {
		return false
	}
	// Behind the PowerShell call, only the wrapper and the print form are
	// install's.
	tap, match := parseTapCommand(command.Raw, exe)
	return match == tapped && !tap.print
}

// cutLauncher splits off the program a command starts with: a quoted path,
// or else its first word.
func cutLauncher(chars []char) (path, rest []char, quoted bool) {
	if after, ok := cutText(chars, `"`); ok {
		end := slices.IndexFunc(after, func(c char) bool { return c.r == '"' })
		if end < 0 {
			return nil, nil, false
		}
		return after[:end], after[end+1:], true
	}
	end := slices.IndexFunc(chars, func(c char) bool { return c.r == ' ' || c.r == '\t' })
	if end < 0 {
		end = len(chars)
	}
	return chars[:end], chars[end:], false
}

func parsePipe(rest []char) (tapCommand, tapMatch) {
	after, ok := cutText(rest, tapPipe)
	if !ok {
		return tapCommand{}, edited
	}
	inner, grouped := cutText(after, groupOpen)
	if !grouped {
		return tappedOriginal(after)
	}
	original, closed := cutSuffix(inner, groupClose)
	if !closed {
		return tapCommand{}, edited
	}
	return tappedOriginal(original)
}

// tappedOriginal is a tapped command with the user's own original. Nothing
// but blanks behind the prefix means there was no status line of the user's
// own, so tap prints its own. Comments are the user's own lines, which an
// earlier build wrapped, so they are kept for uninstall to restore.
func tappedOriginal(original []char) (tapCommand, tapMatch) {
	if strings.TrimSpace(text(original)) == "" {
		return tapCommand{print: true}, tapped
	}
	return tapCommand{original: original}, tapped
}

func parseRunB64(rest []char) (tapCommand, tapMatch) {
	after, ok := cutText(rest, runB64Open)
	if !ok {
		return tapCommand{}, edited
	}
	encoded, closed := cutSuffix(after, runB64Close)
	if !closed {
		return tapCommand{}, edited
	}
	decoded, err := base64.RawURLEncoding.DecodeString(text(encoded))
	// Only the encoding install writes counts: no padding, no line breaks.
	if err != nil || !utf8.Valid(decoded) || base64.RawURLEncoding.EncodeToString(decoded) != text(encoded) {
		return tapCommand{}, edited
	}
	return tappedOriginal(splitString(jsonString(string(decoded))))
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

// cutSuffix reports whether chars end with suffix, and returns what precedes
// it.
func cutSuffix(chars []char, suffix string) ([]char, bool) {
	runes := []rune(suffix)
	if len(chars) < len(runes) {
		return nil, false
	}
	start := len(chars) - len(runes)
	for i, r := range runes {
		if chars[start+i].r != r {
			return nil, false
		}
	}
	return chars[:start], true
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

var escapedRunes = map[byte]rune{
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
		if r, ok := escapedRunes[s[1]]; ok {
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

// utf16Len is the length of s in UTF-16 code units, as Windows counts a
// command line.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}
