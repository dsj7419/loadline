package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/tracker"
)

// A session's name is whatever its user typed. Printed raw, a newline would
// forge a row and an escape sequence would drive the terminal.
func TestStatusTable_PrintsNoControlCharacters(t *testing.T) {
	view := tracker.View{
		Accounts: []tracker.AccountView{{Dir: "/a", Label: "Main\tB"}},
		Sessions: []tracker.SessionView{{
			Dir: "/a", ID: "s1", Name: "evil\x1b[2J\nB  -  -  forged", State: domain.StateWorking,
			Model: "Opus\u0085 5.5",
		}},
	}

	got := statusTable(view, time.UTC)

	if strings.ContainsFunc(strings.ReplaceAll(got, "\n", ""), func(r rune) bool { return r < 0x20 || r == 0x7f || r == 0x85 }) {
		t.Errorf("table holds control characters: %q", got)
	}
	if lines := strings.Count(got, "\n"); lines != 2 {
		t.Errorf("table has %d lines, want the header and one row:\n%s", lines, got)
	}
}

func TestStatusTable_ShowsAWindowWithoutAResetTime(t *testing.T) {
	view := tracker.View{
		Accounts: []tracker.AccountView{{Dir: "/a", Label: "Main", SevenDay: &tracker.WindowView{UsedPct: 41.6}}},
	}

	got := statusTable(view, time.UTC)

	want := "ACCOUNT  WEEK  RESETS  SESSION  STATE  MODEL  CTX\n" +
		"Main     42%   -       -        -      -      -\n"
	if got != want {
		t.Errorf("table:\n%s\nwant:\n%s", got, want)
	}
}
