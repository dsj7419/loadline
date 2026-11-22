package store_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/store"
	"github.com/dsj7419/loadline/internal/tracker"
)

const (
	dirA domain.ConfigDir = "/home/dev/.claude"
	dirB domain.ConfigDir = "/home/dev/.claude-b"

	s1 domain.SessionID = "s1"
	s2 domain.SessionID = "s2"
	s3 domain.SessionID = "s3"
	s4 domain.SessionID = "s4"
	s5 domain.SessionID = "s5"

	opus   = "claude-opus-5-5"
	sonnet = "claude-sonnet-5-5"
)

// t0 is a whole minute, so that at(0) through at(59) share one.
var t0 = time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)

// at is sec seconds after t0.
func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

// openStore opens a new database in a temporary directory and closes it when
// the test ends.
func openStore(t *testing.T) (*store.SQLite, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "loadline.db")
	return reopen(t, path), path
}

// reopen opens the database at path and closes it when the test ends.
func reopen(t *testing.T, path string) *store.SQLite {
	t.Helper()
	s, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T, s *store.SQLite, since time.Time) tracker.Restored {
	t.Helper()
	restored, skipped, err := s.Load(t.Context(), since)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("Load skipped %d rows, want none", skipped)
	}
	return restored
}

// rawDB opens the database at path without the store, to look at or tamper
// with what is on disk.
func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
}

// fullAccount is an account with every field set.
func fullAccount(dir domain.ConfigDir) domain.Account {
	return domain.Account{
		Dir: dir, Label: "Main", Email: "dev@example.com", Plan: "max", Org: "Example Org",
		Auth: domain.AuthSubscription,
		Limits: domain.Limits{
			FiveHour:   &domain.Window{UsedPct: 12.5, ResetsAt: at(3 * 3600)},
			SevenDay:   &domain.Window{UsedPct: 61.25, ResetsAt: at(4 * 86400)},
			ObservedAt: at(-30),
		},
		ProbedAt: at(-600),
	}
}

// fullSession is a session with every exported field set, and running and
// stopped subagent IDs.
func fullSession(id domain.SessionID) domain.Session {
	s := domain.Session{
		ID: id, Dir: dirA, Kind: domain.KindBackground, PID: 4242, Name: "copper-lantern",
		ProjectDir: "/work/copper-lantern",
		State:      domain.StateNeedsYou, WaitReason: "permission prompt", EndReason: "resume",
		StateAt: at(-60), StartedAt: at(-7200), LastSeenAt: at(-5), LastStatusAt: at(-10), EndedAt: at(-3600),
		Lost: true, Subagents: 3,
		Metrics: domain.Metrics{
			Model: "Opus 5.5", ModelID: opus, Effort: "xhigh",
			ContextPct: 31.5, ContextSize: 1_000_000, ContextUsed: 315_000, CostUSD: 4.125,
			LinesAdded: 120, LinesRemoved: 7, CacheHitRatio: 0.875,
		},
	}
	return s.WithSubagentIDs(domain.SubagentIDs{Running: []string{"agent-1", "agent-2"}, Stopped: []string{"agent-9"}})
}

// assertEveryFieldSet fails the test when a field of v, or of a struct
// field of v, is its zero value, so that a field added to the domain later
// cannot slip past the round trip unstored.
func assertEveryFieldSet(t *testing.T, v any) {
	t.Helper()
	var walk func(rv reflect.Value, path string)
	walk = func(rv reflect.Value, path string) {
		if rv.Kind() == reflect.Pointer {
			rv = rv.Elem()
		}
		for i := range rv.NumField() {
			field, value := rv.Type().Field(i), rv.Field(i)
			if !field.IsExported() {
				continue
			}
			name := path + "." + field.Name
			if value.IsZero() {
				t.Fatalf("the fixture leaves %s unset", name)
			}
			if inner := reflect.Indirect(value); inner.Kind() == reflect.Struct && inner.Type() != reflect.TypeFor[time.Time]() {
				walk(value, name)
			}
		}
	}
	walk(reflect.ValueOf(v), reflect.TypeOf(v).Name())
}

func TestSQLite_RoundTripRestored(t *testing.T) {
	s, path := openStore(t)
	ctx := t.Context()
	account, session := fullAccount(dirA), fullSession(s1)
	assertEveryFieldSet(t, account)
	assertEveryFieldSet(t, session)
	request := domain.APIRequest{
		At: at(17), Session: s1, Model: opus, Input: 1200, Output: 340, CacheRead: 56_000, CacheWrite: 7800, CostUSD: 0.4275,
	}

	must(t, s.UpsertAccount(ctx, account))
	must(t, s.UpsertSession(ctx, session))
	must(t, s.AddLimitSample(ctx, dirA, account.Limits))
	must(t, s.AddLimitSample(ctx, dirA, domain.Limits{SevenDay: &domain.Window{UsedPct: 62, ResetsAt: at(4 * 86400)}, ObservedAt: at(30)}))
	must(t, s.AddUsage(ctx, dirA, request))
	must(t, s.Close())

	got := load(t, reopen(t, path), at(-3600))

	if len(got.Accounts) != 1 || !reflect.DeepEqual(got.Accounts[0], account) {
		t.Errorf("accounts\n got %+v\nwant [%+v]", got.Accounts, account)
	}
	if len(got.Sessions) != 1 || got.Sessions[0] != session {
		t.Errorf("sessions\n got %+v\nwant [%+v]", got.Sessions, session)
	}
	if ids := got.Sessions[0].SubagentIDs(); !reflect.DeepEqual(ids, session.SubagentIDs()) {
		t.Errorf("subagent IDs %+v, want %+v", ids, session.SubagentIDs())
	}
	wantSamples := map[domain.ConfigDir]domain.WindowSamples{dirA: {
		FiveHour: []domain.Sample{{At: at(-30), UsedPct: 12.5, ResetsAt: at(3 * 3600)}},
		SevenDay: []domain.Sample{
			{At: at(-30), UsedPct: 61.25, ResetsAt: at(4 * 86400)},
			{At: at(30), UsedPct: 62, ResetsAt: at(4 * 86400)},
		},
	}}
	if !reflect.DeepEqual(got.Samples, wantSamples) {
		t.Errorf("samples\n got %+v\nwant %+v", got.Samples, wantSamples)
	}
	wantUsage := []tracker.UsageRow{{
		Minute: at(0), Dir: dirA, Session: s1, Model: opus,
		Input: 1200, Output: 340, CacheRead: 56_000, CacheWrite: 7800, CostUSD: 0.4275, Requests: 1,
	}}
	if !reflect.DeepEqual(got.Usage, wantUsage) {
		t.Errorf("usage\n got %+v\nwant %+v", got.Usage, wantUsage)
	}
	wantTimelines := map[domain.SessionID][]domain.Transition{
		s1: {{At: session.StateAt, State: domain.StateNeedsYou, Reason: "permission prompt"}},
	}
	if !reflect.DeepEqual(got.Timelines, wantTimelines) {
		t.Errorf("timelines\n got %+v\nwant %+v", got.Timelines, wantTimelines)
	}
}

// Times are kept to the millisecond. A restored time is never later than the
// one recorded, so that it cannot make an event that followed look stale.
func TestSQLite_TimesComeBackNoLaterThanTheyWere(t *testing.T) {
	s, _ := openStore(t)
	session := domain.NewSession(s1, dirA, at(0).Add(999_999_999*time.Nanosecond))
	must(t, s.UpsertSession(t.Context(), session))

	got := load(t, s, at(-60)).Sessions
	if len(got) != 1 {
		t.Fatalf("sessions %+v, want s1", got)
	}
	if want := at(0).Add(999 * time.Millisecond); !got[0].StateAt.Equal(want) || !got[0].StartedAt.Equal(want) {
		t.Errorf("state at %s and started at %s, want both %s", got[0].StateAt, got[0].StartedAt, want)
	}
}

// An account the store keeps with no windows comes back with none.
func TestSQLite_AccountWithoutWindowsKeepsNone(t *testing.T) {
	s, _ := openStore(t)
	bare := domain.Account{Dir: dirB}
	must(t, s.UpsertAccount(t.Context(), fullAccount(dirB)))
	must(t, s.UpsertAccount(t.Context(), bare))

	got := load(t, s, at(-60)).Accounts
	if len(got) != 1 || !reflect.DeepEqual(got[0], bare) {
		t.Errorf("accounts %+v, want the bare %+v that replaced the full one", got, bare)
	}
}

func TestSQLite_UsageMinuteUpsertAccumulates(t *testing.T) {
	s, _ := openStore(t)
	ctx := t.Context()
	call := func(sec int, model string, input int64, cost float64) domain.APIRequest {
		return domain.APIRequest{At: at(sec), Session: s1, Model: model, Input: input, Output: 2 * input, CacheRead: 3 * input, CacheWrite: 4 * input, CostUSD: cost}
	}
	must(t, s.AddUsage(ctx, dirA, call(5, opus, 100, 0.25)))
	must(t, s.AddUsage(ctx, dirA, call(65, opus, 1, 0.125)))
	must(t, s.AddUsage(ctx, dirA, call(50, opus, 10, 0.5)))
	must(t, s.AddUsage(ctx, dirA, call(59, sonnet, 1000, 1)))

	want := []tracker.UsageRow{
		{Minute: at(0), Dir: dirA, Session: s1, Model: opus, Input: 110, Output: 220, CacheRead: 330, CacheWrite: 440, CostUSD: 0.75, Requests: 2},
		{Minute: at(60), Dir: dirA, Session: s1, Model: opus, Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, CostUSD: 0.125, Requests: 1},
		{Minute: at(0), Dir: dirA, Session: s1, Model: sonnet, Input: 1000, Output: 2000, CacheRead: 3000, CacheWrite: 4000, CostUSD: 1, Requests: 1},
	}
	if got := load(t, s, at(-60)).Usage; !reflect.DeepEqual(got, want) {
		t.Errorf("usage, in the order each minute was first written\n got %+v\nwant %+v", got, want)
	}
}

// sessionAt is s in state as of sec, last seen then.
func sessionAt(id domain.SessionID, state domain.State, sec int) domain.Session {
	s := domain.NewSession(id, dirA, at(-7200))
	s.State, s.StateAt, s.LastSeenAt = state, at(sec), at(sec)
	if state == domain.StateEnded {
		s.EndedAt, s.EndReason = at(sec), "prompt_input_exit"
	}
	return s
}

func TestSQLite_RecordsAStateChangeOnlyWhenTheStateChanges(t *testing.T) {
	s, _ := openStore(t)
	ctx := t.Context()
	must(t, s.UpsertSession(ctx, sessionAt(s1, domain.StateStarting, 0)))
	must(t, s.UpsertSession(ctx, sessionAt(s1, domain.StateStarting, 5)))
	must(t, s.UpsertSession(ctx, sessionAt(s1, domain.StateWorking, 10)))
	must(t, s.UpsertSession(ctx, sessionAt(s1, domain.StateWorking, 15)))
	must(t, s.UpsertSession(ctx, sessionAt(s1, domain.StateEnded, 20)))

	want := []domain.Transition{
		{At: at(0), State: domain.StateStarting},
		{At: at(10), State: domain.StateWorking},
		{At: at(20), State: domain.StateEnded, Reason: "prompt_input_exit"},
	}
	if got := load(t, s, at(-60)).Timelines[s1]; !reflect.DeepEqual(got, want) {
		t.Errorf("timeline\n got %+v\nwant %+v", got, want)
	}
}

func TestSQLite_TimelineKeepsTheLast50Changes(t *testing.T) {
	s, _ := openStore(t)
	for i := range 60 {
		state := domain.StateWorking
		if i%2 == 1 {
			state = domain.StateIdle
		}
		must(t, s.UpsertSession(t.Context(), sessionAt(s1, state, i)))
	}

	got := load(t, s, at(-60)).Timelines[s1]
	if len(got) != 50 || !got[0].At.Equal(at(10)) || !got[49].At.Equal(at(59)) {
		t.Errorf("timeline of %d changes from %v to %v, want the 50 from +10s to +59s, oldest first", len(got), first(got), last(got))
	}
}

// A timeline is the order the changes came in, whatever their times say: a
// clock set back dates the changes after it before those ahead of it.
func TestSQLite_TimelineFollowsTheOrderOfTheChanges(t *testing.T) {
	s, _ := openStore(t)
	for i := range 60 {
		state := domain.StateWorking
		if i%2 == 1 {
			state = domain.StateIdle
		}
		must(t, s.UpsertSession(t.Context(), sessionAt(s1, state, 100-i)))
	}

	got := load(t, s, at(-60)).Timelines[s1]
	if len(got) != 50 || !got[0].At.Equal(at(90)) || !got[49].At.Equal(at(41)) {
		t.Errorf("timeline of %d changes from %v to %v, want the last 50 written, from +90s to +41s", len(got), first(got), last(got))
	}
}

// Earlier versions also kept a session's total duration, cache warmth and
// fast mode in its metrics. A row they wrote still loads, with the metrics
// this version keeps, so dropping those needs no migration.
func TestSQLite_LoadsMetricsAnEarlierVersionWrote(t *testing.T) {
	s, path := openStore(t)
	ctx := t.Context()
	must(t, s.UpsertSession(ctx, sessionAt(s1, domain.StateWorking, 0)))
	earlier := `{"model":"Opus 5.5","modelId":"` + opus + `","effort":"xhigh","contextPct":31.5,` +
		`"contextSize":1000000,"contextUsed":315000,"costUSD":4.125,"durationMS":845000,"linesAdded":120,` +
		`"linesRemoved":7,"cacheWarm":true,"cacheHitRatio":0.875,"fastMode":true}`
	if _, err := rawDB(t, path).ExecContext(ctx, `UPDATE sessions SET metrics_json = ? WHERE id = ?`, earlier, s1); err != nil {
		t.Fatalf("write an earlier version's metrics: %v", err)
	}

	got := load(t, s, at(-60)).Sessions

	want := domain.Metrics{
		Model: "Opus 5.5", ModelID: opus, Effort: "xhigh",
		ContextPct: 31.5, ContextSize: 1_000_000, ContextUsed: 315_000, CostUSD: 4.125,
		LinesAdded: 120, LinesRemoved: 7, CacheHitRatio: 0.875,
	}
	if len(got) != 1 || got[0].Metrics != want {
		t.Errorf("sessions %+v, want one with the metrics %+v", got, want)
	}
}

// A row this version cannot read, such as one a damaged file or another
// program left, costs that row alone.
func TestSQLite_LoadSkipsTheRowsItCannotRead(t *testing.T) {
	s, path := openStore(t)
	ctx := t.Context()
	for _, id := range []domain.SessionID{s1, s2, s3} {
		must(t, s.UpsertSession(ctx, sessionAt(id, domain.StateWorking, 0)))
	}
	must(t, s.UpsertAccount(ctx, domain.Account{Dir: dirA}))
	must(t, s.UpsertAccount(ctx, domain.Account{Dir: dirB}))
	db := rawDB(t, path)
	for _, damage := range []string{
		`UPDATE sessions SET state = 'asleep' WHERE id = 's2'`,
		`UPDATE sessions SET metrics_json = '{' WHERE id = 's3'`,
		`UPDATE accounts SET auth = 'magic' WHERE dir = '/home/dev/.claude-b'`,
		`UPDATE state_changes SET state = 'asleep' WHERE session = 's1'`,
	} {
		if _, err := db.ExecContext(ctx, damage); err != nil {
			t.Fatalf("%s: %v", damage, err)
		}
	}

	got, skipped, err := s.Load(ctx, at(-60))
	if err != nil {
		t.Fatalf("Load = %v, want the readable rows", err)
	}
	if skipped != 4 {
		t.Errorf("skipped %d rows, want the 4 damaged", skipped)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != s1 || len(got.Accounts) != 1 || got.Accounts[0].Dir != dirA {
		t.Errorf("sessions %+v and accounts %+v, want s1 and %s", got.Sessions, got.Accounts, dirA)
	}
	if len(got.Timelines[s1]) != 0 {
		t.Errorf("s1's timeline %+v, want its damaged change left out", got.Timelines[s1])
	}
}

func first(ts []domain.Transition) domain.Transition {
	if len(ts) == 0 {
		return domain.Transition{}
	}
	return ts[0]
}

func last(ts []domain.Transition) domain.Transition {
	if len(ts) == 0 {
		return domain.Transition{}
	}
	return ts[len(ts)-1]
}

func TestSQLite_LoadReturnsWhatTheViewNeedsSince(t *testing.T) {
	s, _ := openStore(t)
	ctx := t.Context()
	since := at(0)
	must(t, s.UpsertAccount(ctx, domain.Account{Dir: dirA}))
	must(t, s.UpsertAccount(ctx, domain.Account{Dir: dirB}))
	must(t, s.UpsertSession(ctx, sessionAt(s1, domain.StateWorking, -100)))
	must(t, s.UpsertSession(ctx, sessionAt(s2, domain.StateEnded, -100)))
	must(t, s.UpsertSession(ctx, sessionAt(s3, domain.StateEnded, 10)))
	must(t, s.UpsertSession(ctx, sessionAt(s4, domain.StateParked, -100)))
	for _, sec := range []int{-1, 0} {
		must(t, s.AddLimitSample(ctx, dirA, domain.Limits{SevenDay: &domain.Window{UsedPct: float64(50 + sec)}, ObservedAt: at(sec)}))
	}
	for _, sec := range []int{-60, 0} {
		must(t, s.AddUsage(ctx, dirA, domain.APIRequest{At: at(sec), Session: s1, Model: opus, CostUSD: 1}))
	}

	got := load(t, s, since)

	if len(got.Accounts) != 2 {
		t.Errorf("accounts %+v, want both: accounts are kept whatever their age", got.Accounts)
	}
	var ids []domain.SessionID
	for _, session := range got.Sessions {
		ids = append(ids, session.ID)
	}
	if want := []domain.SessionID{s1, s3, s4}; !reflect.DeepEqual(ids, want) {
		t.Errorf("sessions %v, want %v: the live and parked ones, and the one that ended since", ids, want)
	}
	if _, ok := got.Timelines[s2]; ok || len(got.Timelines) != 3 {
		t.Errorf("timelines for %d sessions, s2's included %v; want the three loaded", len(got.Timelines), ok)
	}
	if want := []domain.Sample{{At: at(0), UsedPct: 50}}; !reflect.DeepEqual(got.Samples[dirA].SevenDay, want) {
		t.Errorf("samples %+v, want only %+v", got.Samples[dirA].SevenDay, want)
	}
	if len(got.Usage) != 1 || !got.Usage[0].Minute.Equal(at(0)) {
		t.Errorf("usage %+v, want only the minute at %s", got.Usage, at(0))
	}
}

func TestSQLite_PruneRemovesOld(t *testing.T) {
	s, _ := openStore(t)
	ctx := t.Context()
	before := at(0)
	must(t, s.UpsertAccount(ctx, fullAccount(dirB)))
	// s1 ended, and s2 was last seen, before the cut; s3 changed state either
	// side of it.
	must(t, s.UpsertSession(ctx, sessionAt(s1, domain.StateWorking, -200)))
	must(t, s.UpsertSession(ctx, sessionAt(s1, domain.StateEnded, -100)))
	must(t, s.UpsertSession(ctx, sessionAt(s2, domain.StateIdle, -1)))
	must(t, s.UpsertSession(ctx, sessionAt(s3, domain.StateWorking, -100)))
	must(t, s.UpsertSession(ctx, sessionAt(s3, domain.StateIdle, 10)))
	// s4's state change is undated, so only its session's removal takes it.
	undated := sessionAt(s4, domain.StateIdle, -100)
	undated.StateAt = time.Time{}
	must(t, s.UpsertSession(ctx, undated))
	// s5 was last seen before the cut, but a poll that missed it ended it
	// after: it ended since, so it stays.
	lost := sessionAt(s5, domain.StateEnded, 10)
	lost.LastSeenAt, lost.Lost, lost.EndReason = at(-40), true, "lost"
	must(t, s.UpsertSession(ctx, lost))
	for _, sec := range []int{-1, 0} {
		must(t, s.AddLimitSample(ctx, dirA, domain.Limits{FiveHour: &domain.Window{UsedPct: 10}, ObservedAt: at(sec)}))
	}
	for _, sec := range []int{-60, 0} {
		must(t, s.AddUsage(ctx, dirA, domain.APIRequest{At: at(sec), Session: s3, Model: opus, CostUSD: 1}))
	}

	must(t, s.Prune(ctx, before))

	got := load(t, s, at(-86400))
	if len(got.Accounts) != 1 || got.Accounts[0].Dir != dirB {
		t.Errorf("accounts %+v, want %s kept", got.Accounts, dirB)
	}
	if len(got.Sessions) != 2 || got.Sessions[0].ID != s3 || got.Sessions[1].ID != s5 {
		t.Errorf("sessions %+v, want s3 and s5", got.Sessions)
	}
	wantTimelines := map[domain.SessionID][]domain.Transition{
		s3: {{At: at(10), State: domain.StateIdle}},
		s5: {{At: at(10), State: domain.StateEnded, Reason: "lost"}},
	}
	if !reflect.DeepEqual(got.Timelines, wantTimelines) {
		t.Errorf("timelines %+v, want %+v", got.Timelines, wantTimelines)
	}
	if want := []domain.Sample{{At: at(0), UsedPct: 10}}; !reflect.DeepEqual(got.Samples[dirA].FiveHour, want) {
		t.Errorf("samples %+v, want only %+v", got.Samples[dirA].FiveHour, want)
	}
	if len(got.Usage) != 1 || !got.Usage[0].Minute.Equal(at(0)) {
		t.Errorf("usage %+v, want only the minute at %s", got.Usage, at(0))
	}
}

// historyAcrossACut stores samples either side of at(0). dirA's 5-hour
// window reaches its limit at -300 and is still there at the cut, its reset
// moved on at -100; its 7-day window comes off its limit before the cut.
// dirB's 7-day window is at its limit with no known reset, and its 5-hour
// window's span ended at its reset before the cut.
func historyAcrossACut(t *testing.T, s *store.SQLite) {
	t.Helper()
	type line struct {
		dir                domain.ConfigDir
		sec                int
		fiveHour, sevenDay *domain.Window
	}
	w := func(pct float64, resetsSec int) *domain.Window {
		if resetsSec == 0 {
			return &domain.Window{UsedPct: pct}
		}
		return &domain.Window{UsedPct: pct, ResetsAt: at(resetsSec)}
	}
	for _, l := range []line{
		{dirA, -400, w(90, 2000), w(100, 100_000)},
		{dirA, -300, w(100, 2000), w(100, 100_000)},
		{dirA, -200, w(103, 2000), w(60, 100_000)},
		{dirA, -100, w(100, 3000), w(61, 100_000)},
		{dirA, 10, w(30, 3000), w(62, 100_000)},
		{dirB, -600, w(100, -10), nil},
		{dirB, -500, nil, w(100, 0)},
		{dirB, -50, nil, w(100, 0)},
	} {
		must(t, s.AddLimitSample(t.Context(), l.dir, domain.Limits{FiveHour: l.fiveHour, SevenDay: l.sevenDay, ObservedAt: at(l.sec)}))
	}
}

// Of each window's samples before a cut, the history keeps one, standing for
// the span on credits they leave open at the cut: dated at the span's start,
// with the reset time the span had come to. A restart then rebuilds the span,
// whether or not a prune came first.
func TestSQLite_KeepsTheStartOfASpanOnCreditsOpenAtTheCut(t *testing.T) {
	want := map[domain.ConfigDir]domain.WindowSamples{
		dirA: {
			FiveHour: []domain.Sample{{At: at(-300), UsedPct: 100, ResetsAt: at(3000)}, {At: at(10), UsedPct: 30, ResetsAt: at(3000)}},
			SevenDay: []domain.Sample{{At: at(10), UsedPct: 62, ResetsAt: at(100_000)}},
		},
		dirB: {SevenDay: []domain.Sample{{At: at(-500), UsedPct: 100}}},
	}
	for name, read := range map[string]func(t *testing.T, s *store.SQLite) tracker.Restored{
		"loaded from the cut on": func(t *testing.T, s *store.SQLite) tracker.Restored {
			t.Helper()
			return load(t, s, at(0))
		},
		"pruned at the cut": func(t *testing.T, s *store.SQLite) tracker.Restored {
			t.Helper()
			must(t, s.Prune(t.Context(), at(0)))
			return load(t, s, at(-86400))
		},
		"pruned at the cut twice": func(t *testing.T, s *store.SQLite) tracker.Restored {
			t.Helper()
			must(t, s.Prune(t.Context(), at(0)))
			must(t, s.Prune(t.Context(), at(0)))
			return load(t, s, at(-86400))
		},
		"pruned earlier, then loaded from the cut on": func(t *testing.T, s *store.SQLite) tracker.Restored {
			t.Helper()
			must(t, s.Prune(t.Context(), at(-250)))
			return load(t, s, at(0))
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := openStore(t)
			historyAcrossACut(t, s)

			if got := read(t, s).Samples; !reflect.DeepEqual(got, want) {
				t.Errorf("samples\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

// Once a span on credits has closed before the cut, nothing of it is kept.
func TestSQLite_PruneForgetsASpanOnCreditsThatClosedBeforeTheCut(t *testing.T) {
	s, _ := openStore(t)
	historyAcrossACut(t, s)
	must(t, s.Prune(t.Context(), at(0)))

	must(t, s.Prune(t.Context(), at(5000)))

	want := map[domain.ConfigDir]domain.WindowSamples{dirB: {SevenDay: []domain.Sample{{At: at(-500), UsedPct: 100}}}}
	if got := load(t, s, at(-86400)).Samples; !reflect.DeepEqual(got, want) {
		t.Errorf("samples\n got %+v\nwant %+v: only the span still open at the cut", got, want)
	}
}

func TestSQLite_MigrationsIdempotent(t *testing.T) {
	s, path := openStore(t)
	must(t, s.UpsertSession(t.Context(), sessionAt(s1, domain.StateWorking, 0)))
	must(t, s.Close())
	first := userVersion(t, rawDB(t, path))

	again := reopen(t, path)
	must(t, again.Close())

	if second := userVersion(t, rawDB(t, path)); first < 1 || second != first {
		t.Errorf("user_version %d after the first open and %d after the second, want the same version, at least 1", first, second)
	}
	if got := load(t, reopen(t, path), at(-60)).Sessions; len(got) != 1 {
		t.Errorf("sessions %+v after reopening, want s1", got)
	}
}

// A database a newer Loadline wrote is left alone: its schema might mean
// something this version would get wrong.
func TestOpen_RefusesANewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loadline.db")
	db := rawDB(t, path)
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version = 99"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	s, err := store.Open(t.Context(), path)
	if err == nil {
		_ = s.Close()
		t.Fatal("Open succeeded on a database from a newer version")
	}
	if !errors.Is(err, store.ErrNewerSchema) || !strings.Contains(err.Error(), "99") {
		t.Errorf("Open = %v, want ErrNewerSchema naming version 99", err)
	}
	if v := userVersion(t, db); v != 99 {
		t.Errorf("user_version %d after the refused open, want 99", v)
	}
	var tables int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Errorf("the refused open created %d schema objects, want none", tables)
	}
}

// Not a byte of it changes, its journal mode included: the newer Loadline
// may rely on the mode it chose.
func TestOpen_LeavesANewerDatabaseAsItIs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loadline.db")
	db := rawDB(t, path)
	if _, err := db.ExecContext(t.Context(), "PRAGMA journal_mode = DELETE; CREATE TABLE future (x); PRAGMA user_version = 7"); err != nil {
		t.Fatal(err)
	}
	must(t, db.Close())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if s, err := store.Open(t.Context(), path); !errors.Is(err, store.ErrNewerSchema) {
		if err == nil {
			_ = s.Close()
		}
		t.Fatalf("Open = %v, want ErrNewerSchema", err)
	}

	if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
		t.Errorf("the refused open changed the database file (%v)", err)
	}
	if _, err := os.Stat(path + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refused open left a write-ahead log beside the database (%v)", err)
	}
}

// Two servers on different ports, or a server and a doctor, may open one new
// database at the same moment.
func TestOpen_ManyAtOnceOnANewDatabase(t *testing.T) {
	for round := range 20 {
		path := filepath.Join(t.TempDir(), "loadline.db")
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i := range errs {
			wg.Go(func() {
				s, err := store.Open(context.Background(), path)
				if err == nil {
					err = s.Close()
				}
				errs[i] = err
			})
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("round %d: open %d: %v", round, i, err)
			}
		}
	}
}

// A sum of token counts too large to hold stops at the largest it can.
func TestSQLite_UsageSumsStopAtTheLargestCount(t *testing.T) {
	s, _ := openStore(t)
	huge := domain.APIRequest{
		At: at(5), Session: s1, Model: opus,
		Input: math.MaxInt64, Output: math.MaxInt64, CacheRead: math.MaxInt64, CacheWrite: math.MaxInt64, CostUSD: 1,
	}
	must(t, s.AddUsage(t.Context(), dirA, huge))
	must(t, s.AddUsage(t.Context(), dirA, huge))

	want := []tracker.UsageRow{{
		Minute: at(0), Dir: dirA, Session: s1, Model: opus,
		Input: math.MaxInt64, Output: math.MaxInt64, CacheRead: math.MaxInt64, CacheWrite: math.MaxInt64, CostUSD: 2, Requests: 2,
	}}
	if got := load(t, s, at(-60)).Usage; !reflect.DeepEqual(got, want) {
		t.Errorf("usage\n got %+v\nwant %+v", got, want)
	}
}

func TestOpen_RefusesAFileThatIsNotADatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loadline.db")
	junk := []byte(strings.Repeat("not a database; ", 64))
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatal(err)
	}

	if s, err := store.Open(t.Context(), path); err == nil {
		_ = s.Close()
		t.Fatal("Open succeeded on a file that is not a database")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(junk) {
		t.Errorf("the file changed under the refused open: %v", err)
	}
}

func TestOpen_KeepsTheDatabasePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes do not govern access on Windows")
	}
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "loadline.db")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A file another program left readable is made private.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	s := reopen(t, path)
	must(t, s.UpsertSession(t.Context(), sessionAt(s1, domain.StateWorking, 0)))

	for _, name := range []string{path, path + "-wal"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s has mode %v, want 0600", filepath.Base(name), mode)
		}
	}
}

func TestOpen_CreatesAPrivateDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes do not govern access on Windows")
	}
	dir := filepath.Join(t.TempDir(), "data")
	reopen(t, filepath.Join(dir, "loadline.db"))

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("the data directory has mode %v, want 0700", mode)
	}
}

func TestOpen_RefusesAPathTheDriverWouldMisread(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows path cannot hold a '?'")
	}
	path := filepath.Join(t.TempDir(), "odd?_pragma=journal_mode(OFF)", "loadline.db")
	if s, err := store.Open(t.Context(), path); err == nil {
		_ = s.Close()
		t.Fatal("Open succeeded on a path holding a '?'")
	}
}

func TestSQLite_CallsEndWithTheirContext(t *testing.T) {
	s, _ := openStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := s.UpsertSession(ctx, sessionAt(s1, domain.StateWorking, 0)); !errors.Is(err, context.Canceled) {
		t.Errorf("UpsertSession on an ended context = %v, want context.Canceled", err)
	}
	if _, _, err := s.Load(ctx, at(0)); !errors.Is(err, context.Canceled) {
		t.Errorf("Load on an ended context = %v, want context.Canceled", err)
	}
}
