package cli_test

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/store"
)

// openHistory opens the server's history database beside the server, as
// another reader of it would.
func (f *serveFixture) openHistory() *store.SQLite {
	f.t.Helper()
	s, err := store.Open(f.t.Context(), f.paths.DBFile)
	if err != nil {
		f.t.Fatalf("open the history: %v", err)
	}
	f.t.Cleanup(func() { _ = s.Close() })
	return s
}

// usageMinutes returns the minutes of the model calls the history holds.
func (f *serveFixture) usageMinutes(s *store.SQLite) []time.Time {
	f.t.Helper()
	restored, err := s.Load(f.t.Context(), time.Time{})
	if err != nil {
		f.t.Fatalf("Load: %v", err)
	}
	minutes := make([]time.Time, 0, len(restored.Usage))
	for _, row := range restored.Usage {
		minutes = append(minutes, row.Minute)
	}
	return minutes
}

func TestServe_PrunesTheHistoryAtStartupAndDaily(t *testing.T) {
	f := newServeFixture(t)
	f.writeConfig(`{"retentionDays": 1, "idleExitMinutes": 0}`)
	history := f.openHistory()
	for _, age := range []time.Duration{25 * time.Hour, 23 * time.Hour} {
		call := domain.APIRequest{At: t0.Add(-age), Session: "s1", Model: "claude-opus-5-5", CostUSD: 1}
		if err := history.AddUsage(t.Context(), f.defaultDir(), call); err != nil {
			t.Fatalf("AddUsage: %v", err)
		}
	}

	f.start()
	waitFor(t, "the startup prune", func() bool {
		minutes := f.usageMinutes(history)
		return len(minutes) == 1 && minutes[0].Equal(t0.Add(-23*time.Hour))
	})
	f.waitArmed(24 * time.Hour)
	f.clock.Advance(24 * time.Hour)
	waitFor(t, "the next day's prune", func() bool { return len(f.usageMinutes(history)) == 0 })
}

// A retention longer than a time.Duration can count, some 292 years, keeps
// everything rather than wrapping around into a cut in the future.
func TestServe_KeepsTheHistoryForARetentionTooLongToCount(t *testing.T) {
	f := newServeFixture(t)
	f.writeConfig(`{"retentionDays": 106752, "idleExitMinutes": 0}`)
	history := f.openHistory()
	call := domain.APIRequest{At: t0.Add(-25 * time.Hour), Session: "s1", Model: "claude-opus-5-5", CostUSD: 1}
	if err := history.AddUsage(t.Context(), f.defaultDir(), call); err != nil {
		t.Fatalf("AddUsage: %v", err)
	}

	f.start()
	f.waitArmed(24 * time.Hour)

	if got := f.usageMinutes(history); len(got) != 1 {
		t.Errorf("usage minutes %v after the startup prune, want the one kept", got)
	}
}

// A database the server cannot use costs the history, never the dashboard.
func TestServe_RunsWithoutHistoryWhenTheDatabaseIsNewer(t *testing.T) {
	f := newServeFixture(t)
	if err := os.MkdirAll(f.paths.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	newer, err := sql.Open("sqlite", f.paths.DBFile)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(func() { _ = newer.Close() })
	if _, err := newer.ExecContext(t.Context(), "PRAGMA user_version = 99"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	r := f.start()
	f.hook(f.defaultDir(), "s1", "SessionStart")
	if _, ok := sessionByID(f.snapshot(), "s1"); !ok {
		t.Error("the dashboard does not show the session")
	}
	if code := r.stop(t); code != 0 {
		t.Errorf("Serve = %d, want 0", code)
	}

	if log := f.logText(); !strings.Contains(log, "history is off") || !strings.Contains(log, "newer loadline") {
		t.Errorf("the log does not say the history is off for a newer database:\n%s", log)
	}
	var version, objects int
	if err := newer.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := newer.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema").Scan(&objects); err != nil {
		t.Fatal(err)
	}
	if version != 99 || objects != 0 {
		t.Errorf("the database is at version %d with %d schema objects, want it left at 99 with none", version, objects)
	}
}

// The demo's made-up machine is never kept.
func TestServe_DemoKeepsNoHistory(t *testing.T) {
	f := newServeFixture(t)
	r := f.start(func(o *cli.ServeOptions) { o.Demo = true })
	f.snapshot()
	if code := r.stop(t); code != 0 {
		t.Errorf("Serve = %d, want 0", code)
	}

	if _, err := os.Stat(f.paths.DBFile); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the demo left a history database (%v), want none", err)
	}
}
