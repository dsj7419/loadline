package store_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/store"
)

// queueSize is how many records an AsyncRecorder holds while it writes, and
// batchSize how many it writes at a time.
const (
	queueSize = 4096
	batchSize = 100
)

// logBuffer collects what a recorder logs.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newRecorder(t *testing.T, s *store.SQLite) (*store.AsyncRecorder, *logBuffer) {
	t.Helper()
	logs := &logBuffer{}
	rec := store.NewAsyncRecorder(s, slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = rec.Close(ctx)
	})
	return rec, logs
}

func closeRecorder(t *testing.T, rec *store.AsyncRecorder) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := rec.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// holdWriteLock takes the database's write lock from another connection, as
// a second process writing to it would, until the returned function is
// called.
func holdWriteLock(t *testing.T, path string) (release func()) {
	t.Helper()
	db := rawDB(t, path)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("take the write lock: %v", err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			_ = conn.Close()
		})
	}
	t.Cleanup(release)
	return release
}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func modelCall(sec int) domain.APIRequest {
	return domain.APIRequest{At: at(sec), Session: s1, Model: opus, Input: 10, CostUSD: 0.5}
}

// requestsKept returns how many model calls the database holds.
func requestsKept(t *testing.T, s *store.SQLite) int {
	t.Helper()
	n := 0
	for _, row := range load(t, s, at(-3600)).Usage {
		n += row.Requests
	}
	return n
}

// The tracker calls the recorder with its lock held, so a database that is
// slow or locked must never hold it up: what does not fit is dropped and
// counted.
func TestAsyncRecorder_NeverBlocksWhenFull(t *testing.T) {
	s, path := openStore(t)
	release := holdWriteLock(t, path)
	rec, _ := newRecorder(t, s)
	const total = queueSize + 1000

	recorded := make(chan struct{})
	go func() {
		defer close(recorded)
		for range total {
			rec.UsageRecorded(dirA, modelCall(0))
		}
	}()
	select {
	case <-recorded:
	case <-time.After(3 * time.Second):
		t.Fatal("recording blocked while the database was locked")
	}

	// The writer holds at most one batch beside a full queue.
	if dropped := rec.Dropped(); dropped < total-queueSize-batchSize || dropped > total-queueSize {
		t.Errorf("dropped %d of %d, want between %d and %d", dropped, total, total-queueSize-batchSize, total-queueSize)
	}
	release()
	closeRecorder(t, rec)
	if kept := requestsKept(t, s); int64(kept)+rec.Dropped() != total || rec.Errors() != 0 {
		t.Errorf("kept %d and dropped %d of %d with %d errors, want every call either kept or dropped",
			kept, rec.Dropped(), total, rec.Errors())
	}
}

func TestAsyncRecorder_CloseFlushes(t *testing.T) {
	s, _ := openStore(t)
	rec, logs := newRecorder(t, s)
	account, session := fullAccount(dirA), fullSession(s1)
	call := modelCall(5)

	rec.AccountChanged(account)
	rec.SessionChanged(session)
	rec.LimitSampled(dirA, account.Limits)
	rec.UsageRecorded(dirA, call)
	closeRecorder(t, rec)

	got := load(t, s, at(-3600))
	if len(got.Accounts) != 1 || !reflect.DeepEqual(got.Accounts[0], account) {
		t.Errorf("accounts %+v, want [%+v]", got.Accounts, account)
	}
	if len(got.Sessions) != 1 || got.Sessions[0] != session {
		t.Errorf("sessions %+v, want [%+v]", got.Sessions, session)
	}
	if samples := got.Samples[dirA]; len(samples.FiveHour) != 1 || len(samples.SevenDay) != 1 {
		t.Errorf("samples %+v, want one of each window", samples)
	}
	if len(got.Usage) != 1 || got.Usage[0].Requests != 1 || got.Usage[0].CostUSD != call.CostUSD {
		t.Errorf("usage %+v, want the call", got.Usage)
	}
	if logs.String() != "" {
		t.Errorf("logged %q, want nothing", logs.String())
	}
}

func TestAsyncRecorder_WritesWithoutWaitingForAFullBatch(t *testing.T) {
	s, _ := openStore(t)
	rec, _ := newRecorder(t, s)

	rec.UsageRecorded(dirA, modelCall(0))

	eventually(t, 5*time.Second, "the call to be written", func() bool { return requestsKept(t, s) == 1 })
}

func TestAsyncRecorder_DropsWhatComesAfterClose(t *testing.T) {
	s, _ := openStore(t)
	rec, _ := newRecorder(t, s)
	closeRecorder(t, rec)

	rec.UsageRecorded(dirA, modelCall(0))

	if rec.Dropped() != 1 || requestsKept(t, s) != 0 {
		t.Errorf("dropped %d and kept %d, want the late call dropped", rec.Dropped(), requestsKept(t, s))
	}
	closeRecorder(t, rec)
}

// A shutdown must not wait on a database that never frees up.
func TestAsyncRecorder_CloseGivesUpAtItsDeadline(t *testing.T) {
	s, path := openStore(t)
	release := holdWriteLock(t, path)
	rec, _ := newRecorder(t, s)
	rec.UsageRecorded(dirA, modelCall(0))

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	err := rec.Close(ctx)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close = %v, want the deadline's error", err)
	}
	if waited := time.Since(began); waited > 2*time.Second {
		t.Errorf("Close took %v, want it back at its deadline", waited)
	}
	eventually(t, 2*time.Second, "the abandoned write to give up its wait for the lock", func() bool { return rec.Errors() == 1 })
	release()
}

// A database that keeps failing is logged once a minute, not once a write.
func TestAsyncRecorder_CountsWriteErrorsAndLogsThemOncePerMinute(t *testing.T) {
	s, _ := openStore(t)
	rec, logs := newRecorder(t, s)
	must(t, s.Close())

	rec.UsageRecorded(dirA, modelCall(0))
	eventually(t, 5*time.Second, "the first failed write", func() bool { return rec.Errors() == 1 })
	rec.SessionChanged(fullSession(s1))
	eventually(t, 5*time.Second, "the second failed write", func() bool { return rec.Errors() == 2 })
	closeRecorder(t, rec)

	text := logs.String()
	if n := strings.Count(text, "level=WARN"); n != 1 || !strings.Contains(text, "database is closed") {
		t.Errorf("logged %d warnings, want one naming the failure:\n%s", n, text)
	}
}

// The recorder keeps its own copy of the windows it is handed, so that what
// it writes is what was reported, whatever becomes of the caller's.
func TestAsyncRecorder_KeepsItsOwnCopyOfTheWindows(t *testing.T) {
	s, path := openStore(t)
	release := holdWriteLock(t, path)
	rec, _ := newRecorder(t, s)
	account := fullAccount(dirA)
	limits := domain.Limits{FiveHour: &domain.Window{UsedPct: 10}, SevenDay: &domain.Window{UsedPct: 20}, ObservedAt: at(0)}

	rec.AccountChanged(account)
	rec.LimitSampled(dirA, limits)
	account.Limits.FiveHour.UsedPct, account.Limits.SevenDay.UsedPct = 99, 99
	limits.FiveHour.UsedPct, limits.SevenDay.UsedPct = 99, 99
	release()
	closeRecorder(t, rec)

	got := load(t, s, at(-3600))
	if a := got.Accounts[0].Limits; a.FiveHour.UsedPct != 12.5 || a.SevenDay.UsedPct != 61.25 {
		t.Errorf("the account's windows were stored at %v%% and %v%%, want 12.5%% and 61.25%% as reported",
			a.FiveHour.UsedPct, a.SevenDay.UsedPct)
	}
	samples := got.Samples[dirA]
	if len(samples.FiveHour) != 1 || samples.FiveHour[0].UsedPct != 10 || len(samples.SevenDay) != 1 || samples.SevenDay[0].UsedPct != 20 {
		t.Errorf("samples %+v, want 10%% and 20%% as reported", samples)
	}
}

// One write that fails costs that write alone, not the batch it was queued
// in.
func TestAsyncRecorder_LosesOnlyTheWriteThatFails(t *testing.T) {
	s, path := openStore(t)
	release := holdWriteLock(t, path)
	rec, logs := newRecorder(t, s)
	unwritable := fullSession(s2)
	unwritable.State = domain.State(99)

	rec.SessionChanged(fullSession(s1))
	rec.SessionChanged(unwritable)
	rec.AccountChanged(fullAccount(dirA))
	rec.UsageRecorded(dirA, modelCall(0))
	release()
	closeRecorder(t, rec)

	got := load(t, s, at(-3600))
	if len(got.Sessions) != 1 || got.Sessions[0].ID != s1 || len(got.Accounts) != 1 || len(got.Usage) != 1 {
		t.Errorf("kept sessions %+v, %d accounts and %d usage rows; want s1, the account and the call", got.Sessions, len(got.Accounts), len(got.Usage))
	}
	if rec.Errors() != 1 || strings.Count(logs.String(), "level=WARN") != 1 {
		t.Errorf("counted %d lost and logged %q, want the one write lost and logged once", rec.Errors(), logs.String())
	}
}

// A model call with a token count at the largest a count can be costs the
// changes queued beside it nothing.
func TestAsyncRecorder_AHugeModelCallCostsNothingElse(t *testing.T) {
	s, _ := openStore(t)
	rec, _ := newRecorder(t, s)
	huge := domain.APIRequest{At: at(5), Session: s1, Model: opus, Input: math.MaxInt64, CostUSD: 0.5}

	rec.UsageRecorded(dirA, huge)
	rec.UsageRecorded(dirA, huge)
	rec.SessionChanged(fullSession("unrelated"))
	rec.AccountChanged(fullAccount(dirA))
	closeRecorder(t, rec)

	got := load(t, s, at(-3600))
	if len(got.Sessions) != 1 || len(got.Accounts) != 1 || len(got.Usage) != 1 || rec.Errors() != 0 {
		t.Errorf("kept %d sessions, %d accounts and %d usage rows with %d lost; want all of them",
			len(got.Sessions), len(got.Accounts), len(got.Usage), rec.Errors())
	}
}
