package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/tracker"
)

const (
	snapshotPath = "/api/snapshot"
	// statusTimeout bounds the status command's request for the snapshot.
	statusTimeout = 5 * time.Second
	// maxSnapshot is the largest snapshot status reads, far more than the
	// sessions of any machine make.
	maxSnapshot = 32 << 20
	// shortIDLength is how much of an unnamed session's ID the table shows.
	shortIDLength = 8
	// absent fills a cell that has nothing to show.
	absent = "-"
)

// RunStatus prints the accounts and sessions the running server sees, as a
// table, and exits 1 with "loadline is not running" when no loadline
// answers on the configured port.
func RunStatus(ctx context.Context, stdio IO) int {
	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stdio, err)
	}
	// Without a home directory, Load refuses only an account that names its
	// config dir from home, and says so.
	home, _ := os.UserHomeDir()
	cfg, err := config.Load(paths.ConfigFile, home)
	if err != nil {
		return fail(stdio, err)
	}

	client := forward.Client{BaseURL: forward.LoopbackURL(cfg.Port)}
	if !client.Healthy(ctx) {
		_, _ = io.WriteString(stdio.Err, "loadline is not running\n")
		return exitFailure
	}
	if client.Token, err = config.EnsureToken(paths.TokenFile); err != nil {
		return fail(stdio, err)
	}
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	body, err := client.Get(ctx, snapshotPath, maxSnapshot)
	if err != nil {
		return fail(stdio, err)
	}
	var view tracker.View
	if err := json.Unmarshal(body, &view); err != nil {
		return fail(stdio, fmt.Errorf("read the server's snapshot: %w", err))
	}
	return printStdout(stdio, statusTable(view, time.Local))
}

// statusTable lays view out in columns: a row for each session that has not
// ended, under its account, which repeats on each of its rows, and a row for
// each account that has no such session. Reset times are shown in loc.
func statusTable(view tracker.View, loc *time.Location) string {
	var out strings.Builder
	table := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	row := func(cells ...string) {
		for i, c := range cells {
			cells[i] = printable(c)
		}
		// A strings.Builder takes every write.
		_, _ = io.WriteString(table, strings.Join(cells, "\t")+"\n")
	}

	row("ACCOUNT", "WEEK", "RESETS", "SESSION", "STATE", "MODEL", "CTX")
	for _, account := range view.Accounts {
		week, resets := weekCells(account, loc)
		listed := false
		for _, s := range view.Sessions {
			if s.Dir != account.Dir || s.State == domain.StateEnded {
				continue
			}
			row(account.Label, week, resets, sessionCell(s), s.State.String(), orAbsent(s.Model), contextCell(s))
			listed = true
		}
		if !listed {
			row(account.Label, week, resets, absent, absent, absent, absent)
		}
	}
	_ = table.Flush()
	return out.String()
}

// weekCells are the account's seven-day usage and when it resets.
func weekCells(account tracker.AccountView, loc *time.Location) (week, resets string) {
	window := account.SevenDay
	if window == nil {
		return absent, absent
	}
	week = fmt.Sprintf("%.0f%%", window.UsedPct)
	resetsAt := time.Time(window.ResetsAt)
	if resetsAt.IsZero() {
		return week, absent
	}
	return week, resetsAt.In(loc).Format("Mon 15:04")
}

// sessionCell names the session, or shows the start of its ID when it has
// no name.
func sessionCell(s tracker.SessionView) string {
	if s.Name != "" {
		return s.Name
	}
	id := string(s.ID)
	if len(id) > shortIDLength {
		return id[:shortIDLength]
	}
	return orAbsent(id)
}

func contextCell(s tracker.SessionView) string {
	if s.ContextPct <= 0 {
		return absent
	}
	return fmt.Sprintf("%.0f%%", s.ContextPct)
}

func orAbsent(s string) string {
	if s == "" {
		return absent
	}
	return s
}

// printable replaces the control characters in s with spaces. Session names
// and labels come from outside, and a newline, tab or escape sequence in one
// would break the table or drive the terminal.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
