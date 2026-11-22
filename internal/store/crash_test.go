package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/dsj7419/loadline/internal/domain"
)

// When crashEnv is set, the test binary plays a server that dies partway
// through a write to crashDB in its working directory, and exits before any
// test runs.
const (
	crashEnv  = "LOADLINE_STORE_CRASH"
	crashDB   = "loadline.db"
	crashExit = 3
)

func TestMain(m *testing.M) {
	if os.Getenv(crashEnv) != "" {
		os.Exit(crashMidWrite(crashDB))
	}
	goleak.VerifyTestMain(m)
}

var crashT0 = time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)

// committedSession is what the crashing server stored before it died.
func committedSession() domain.Session {
	return domain.NewSession("kept", "/home/dev/.claude", crashT0)
}

// crashMidWrite commits a session, then dies with a batch that changes it
// and adds others half written: the page cache is kept so small that the
// batch's pages reach the write-ahead log before it can commit.
func crashMidWrite(path string) int {
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		return 1
	}
	if err := s.UpsertSession(ctx, committedSession()); err != nil {
		return 1
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA cache_size = 8"); err != nil {
		return 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 1
	}
	changed := committedSession()
	changed.State, changed.StateAt = domain.StateWorking, crashT0.Add(time.Minute)
	if err := upsertSession(ctx, tx, changed); err != nil {
		return 1
	}
	for i := range 500 {
		id := domain.SessionID(fmt.Sprintf("lost-%d", i))
		if err := upsertSession(ctx, tx, domain.NewSession(id, "/home/dev/.claude", crashT0)); err != nil {
			return 1
		}
		call := domain.APIRequest{At: crashT0, Session: id, Model: "claude-opus-5-5", CostUSD: 1}
		if err := addUsage(ctx, tx, "/home/dev/.claude", call); err != nil {
			return 1
		}
	}
	return crashExit
}

func TestSQLite_RestoresWhatWasCommittedBeforeACrash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, crashDB)
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$") //nolint:gosec // G204: re-executes the test binary as the crashing server
	child.Dir = dir
	child.Env = append(os.Environ(), crashEnv+"=1")
	var exit *exec.ExitError
	if err := child.Run(); !errors.As(err, &exit) || exit.ExitCode() != crashExit {
		t.Fatalf("the crashing server ended with %v, want exit code %d", err, crashExit)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("no write-ahead log left behind (%v): the server closed the database instead of dying", err)
	}

	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open after the crash: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var integrity string
	if err := s.db.QueryRowContext(t.Context(), "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity check after the crash: %q, %v", integrity, err)
	}
	got, err := s.Load(t.Context(), crashT0.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Load after the crash: %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0] != committedSession() {
		t.Errorf("sessions %+v, want only the committed %+v", got.Sessions, committedSession())
	}
	if len(got.Usage) != 0 || len(got.Timelines["kept"]) != 1 {
		t.Errorf("usage %+v and timeline %+v, want no usage and the committed change alone", got.Usage, got.Timelines["kept"])
	}
}

func TestOpen_SetsThePragmas(t *testing.T) {
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "loadline.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for pragma, want := range map[string]string{
		"PRAGMA journal_mode": "wal",
		"PRAGMA busy_timeout": "5000",
		"PRAGMA foreign_keys": "1",
		"PRAGMA synchronous":  "1",
		"PRAGMA user_version": fmt.Sprint(len(migrations)),
	} {
		var got string
		if err := s.db.QueryRowContext(t.Context(), pragma).Scan(&got); err != nil {
			t.Fatalf("%s: %v", pragma, err)
		}
		if got != want {
			t.Errorf("%s = %s, want %s", pragma, got, want)
		}
	}
}
