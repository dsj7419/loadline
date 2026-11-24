package termtext_test

import (
	"testing"

	"github.com/dsj7419/loadline/internal/termtext"
)

func TestEscape(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
	}{
		{name: "plain text", text: "0.1.1-rc.1+build 7", want: "0.1.1-rc.1+build 7"},
		{name: "letters of any script", text: "Zoë 東京 Ünïcödé", want: "Zoë 東京 Ünïcödé"},
		{name: "an escape sequence", text: "9.9\x1b]0;title\a\x1b[2K", want: `9.9\x1b]0;title\a\x1b[2K`},
		{name: "line breaks and a tab", text: "a\r\nb\tc", want: `a\r\nb\tc`},
		{name: "delete", text: "a\x7fb", want: `a\x7fb`},
		{name: "a C1 control", text: "a\u009b31m", want: `a\u009b31m`},
		{name: "a right-to-left override", text: "a\u202eb", want: `a\u202eb`},
		{name: "a zero-width space", text: "a\u200bb", want: `a\u200bb`},
		{name: "a byte order mark", text: "\ufeffa", want: `\ufeffa`},
		{name: "a line separator", text: "a\u2028b", want: `a\u2028b`},
		{name: "a paragraph separator", text: "a\u2029b", want: `a\u2029b`},
		{name: "nothing", text: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := termtext.Escape(tc.text); got != tc.want {
				t.Errorf("Escape(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}
