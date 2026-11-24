package fsperm_test

import (
	"testing"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// A path is unsafe to paste into a command when a shell an administrator may
// paste it into reads part of it as something other than itself, even
// inside quotes, or a terminal shows it otherwise than it is: then a command
// that names it could act on another path than the one it seems to. Each
// caller adds what its own shells read besides. The characters are given by
// their code points, so that none of them hides in this file.
func TestPasteUnsafe_TellsAPathAShellOrATerminalWouldTurnIntoAnother(t *testing.T) {
	holding := func(r rune) string { return "/srv/a" + string(r) + "b/loadline" }
	for name, r := range map[string]rune{
		"a double quote":            '"',
		"a percent sign":            '%',
		"a dollar sign":             '$',
		"an exclamation mark":       '!',
		"a backtick":                '`',
		"a left typographic quote":  0x201C,
		"a right typographic quote": 0x201D,
		"a low typographic quote":   0x201E,
		"a tab":                     0x09,
		"a line feed":               0x0A,
		"a delete":                  0x7F,
		"a zero-width space":        0x200B,
		"a right-to-left override":  0x202E,
		"a left-to-right isolate":   0x2066,
		"a pop directional isolate": 0x2069,
		"a byte order mark":         0xFEFF,
		"a line separator":          0x2028,
		"a paragraph separator":     0x2029,
	} {
		if path := holding(r); !fsperm.PasteUnsafe(path) {
			t.Errorf("PasteUnsafe(%q), with %s = false, want true", path, name)
		}
	}
	for name, r := range map[string]rune{
		"a space":                  ' ',
		"an ampersand":             '&',
		"a single quote":           '\'',
		"a parenthesis":            '(',
		"a letter with a mark":     0x00EB,
		"a letter of a script":     0x5C71,
		"a backslash":              0x5C,
		"a non-breaking space":     0x00A0,
		"a typographic apostrophe": 0x2019,
	} {
		if path := holding(r); fsperm.PasteUnsafe(path) {
			t.Errorf("PasteUnsafe(%q), with %s = true, want false", path, name)
		}
	}
}
