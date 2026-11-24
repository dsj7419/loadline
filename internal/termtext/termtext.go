// Package termtext shows text that came from outside Loadline, such as what
// another process answered on its port or a name another program registered,
// on a terminal as text.
package termtext

import (
	"strconv"
	"strings"
	"unicode"
)

// Escape returns text with each character that would act on the terminal
// it is printed on, or change how the line around it reads, written as Go
// escapes it in a string literal (\x1b, \r, \u202e): a control character, a
// format character, such as a right-to-left override or a zero-width space,
// and a line or a paragraph separator. Every other character, a letter of
// any script among them, is left as it is.
func Escape(text string) string {
	var shown strings.Builder
	for _, r := range text {
		if !unicode.IsControl(r) && !unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			shown.WriteRune(r)
			continue
		}
		quoted := strconv.QuoteRune(r)
		shown.WriteString(quoted[1 : len(quoted)-1])
	}
	return shown.String()
}
