package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	// The pure-Go SQLite driver, registered as "sqlite": the binary stays
	// free of cgo.
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/tracker"
)

const (
	// maxTimeline is how many state changes Load returns for each session,
	// the number the tracker keeps.
	maxTimeline = 50
	// maxCount is the largest token count an INTEGER column holds.
	maxCount int64 = math.MaxInt64
)

// connOptions configure every connection: up to busyTimeout of waiting for
// another connection's lock; the syncs that write-ahead logging needs to
// survive a crash of the process, though a power cut may undo the last
// commits; foreign keys enforced; and write transactions that take the
// write lock as they begin, so that one never fails partway for want of it.
// Write-ahead logging itself is a property of the file, which migrate sets.
const connOptions = "?_txlock=immediate&_busy_timeout=5000&_foreign_keys=1&_synchronous=NORMAL"

const (
	// busyTimeout is connOptions' _busy_timeout.
	busyTimeout = 5 * time.Second
	// busyRetryEvery spaces the tries of what SQLite will not wait for.
	busyRetryEvery = 10 * time.Millisecond
)

const useWALSQL = `PRAGMA journal_mode = WAL`

// useWAL turns the database to write-ahead logging, so that a reader never
// waits on the writer. The switch needs the database to itself and, unlike
// SQLite's other locks, does not wait for it under the busy timeout: a
// switch that finds another connection at the database, as one opening it
// at the same moment is, is tried again until busyTimeout has passed. A
// file system that cannot hold the log leaves the database in the mode it
// had, which works as well, only with more waiting.
func useWAL(ctx context.Context, db *sql.DB) error {
	deadline := time.Now().Add(busyTimeout)
	for {
		var mode string
		err := db.QueryRowContext(ctx, useWALSQL).Scan(&mode)
		if err == nil {
			return nil
		}
		if !isBusy(err) || !time.Now().Before(deadline) {
			return fmt.Errorf("switch to write-ahead logging: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("switch to write-ahead logging: %w", ctx.Err())
		case <-time.After(busyRetryEvery):
		}
	}
}

// isBusy reports whether err is SQLite's SQLITE_BUSY, in any of its forms.
func isBusy(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && e.Code()&0xff == sqlite3.SQLITE_BUSY
}

// SQLite is the history database. It is safe for concurrent use: it holds
// one connection, which its calls take turns on.
type SQLite struct {
	db *sql.DB
}

// Open opens the database at path, creating it and its directory as
// needed, both private to the user, and migrates its schema. A database
// that a newer Loadline migrated further is refused with ErrNewerSchema,
// its schema left as it is.
func Open(ctx context.Context, path string) (*SQLite, error) {
	s, err := open(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("open history %s: %w", path, err)
	}
	return s, nil
}

func open(ctx context.Context, path string) (*SQLite, error) {
	// The driver reads everything after a '?' as its options.
	if strings.ContainsRune(path, '?') {
		return nil, errors.New("the path holds a '?'")
	}
	if err := createPrivate(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+connOptions)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	// One connection serializes the writes of this process, which then
	// never contend with each other for SQLite's single write lock.
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLite{db: db}, nil
}

// createPrivate creates the file at path, and its directory, readable by
// the user alone, and makes an existing file so. SQLite gives its
// write-ahead log the database file's mode.
func createPrivate(path string) error {
	if err := fsperm.MkdirAll(filepath.Dir(path)); err != nil {
		return fmt.Errorf("create the directory: %w", err)
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("create the file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("create the file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("make the file private: %w", err)
	}
	return nil
}

// Close closes the database.
func (s *SQLite) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close history: %w", err)
	}
	return nil
}

// UpsertSession stores the session as it now stands, and a state change
// when its state differs from the stored one or the session is new.
func (s *SQLite) UpsertSession(ctx context.Context, session domain.Session) error {
	return s.inTx(ctx, func(tx *sql.Tx) error { return upsertSession(ctx, tx, session) })
}

// UpsertAccount stores the account as it now stands.
func (s *SQLite) UpsertAccount(ctx context.Context, a domain.Account) error {
	return s.inTx(ctx, func(tx *sql.Tx) error { return upsertAccount(ctx, tx, a) })
}

// AddLimitSample stores a sample of each window limits holds, as of its
// ObservedAt.
func (s *SQLite) AddLimitSample(ctx context.Context, dir domain.ConfigDir, limits domain.Limits) error {
	return s.inTx(ctx, func(tx *sql.Tx) error { return addLimitSample(ctx, tx, dir, limits) })
}

// AddUsage adds a model call to the usage of its minute, session and model.
func (s *SQLite) AddUsage(ctx context.Context, dir domain.ConfigDir, r domain.APIRequest) error {
	return s.inTx(ctx, func(tx *sql.Tx) error { return addUsage(ctx, tx, dir, r) })
}

// inTx runs write in a transaction, which it commits when write succeeds.
func (s *SQLite) inTx(ctx context.Context, write func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := write(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

const (
	sessionStateSQL = `SELECT state FROM sessions WHERE id = ?`

	upsertSessionSQL = `INSERT INTO sessions (
		id, dir, kind, pid, name, project_dir, state, wait_reason, end_reason,
		state_at, started_at, last_seen_at, last_status_at, ended_at,
		lost, subagents, subagent_ids, metrics_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (id) DO UPDATE SET
		dir = excluded.dir, kind = excluded.kind, pid = excluded.pid, name = excluded.name,
		project_dir = excluded.project_dir, state = excluded.state, wait_reason = excluded.wait_reason,
		end_reason = excluded.end_reason, state_at = excluded.state_at, started_at = excluded.started_at,
		last_seen_at = excluded.last_seen_at, last_status_at = excluded.last_status_at,
		ended_at = excluded.ended_at, lost = excluded.lost, subagents = excluded.subagents,
		subagent_ids = excluded.subagent_ids, metrics_json = excluded.metrics_json`

	insertStateChangeSQL = `INSERT INTO state_changes (session, at, state, reason) VALUES (?, ?, ?, ?)`

	upsertAccountSQL = `INSERT INTO accounts (
		dir, label, email, plan, org, auth, probed_at,
		five_hour_pct, five_hour_resets_at, seven_day_pct, seven_day_resets_at, limits_observed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (dir) DO UPDATE SET
		label = excluded.label, email = excluded.email, plan = excluded.plan, org = excluded.org,
		auth = excluded.auth, probed_at = excluded.probed_at,
		five_hour_pct = excluded.five_hour_pct, five_hour_resets_at = excluded.five_hour_resets_at,
		seven_day_pct = excluded.seven_day_pct, seven_day_resets_at = excluded.seven_day_resets_at,
		limits_observed_at = excluded.limits_observed_at`

	insertSampleSQL = `INSERT INTO limit_samples (dir, window, at, used_pct, resets_at) VALUES (?, ?, ?, ?, ?)`

	// A sum of counts too large for its column stops at maxCount, the tenth
	// parameter: SQLite turns a sum that overflows into a float, which an
	// INTEGER column refuses.
	addUsageSQL = `INSERT INTO usage_minutes (
		minute, session, model, dir, input, output, cache_read, cache_write, cost_usd, requests
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
	ON CONFLICT (minute, session, model) DO UPDATE SET
		input = min(input + excluded.input, ?10), output = min(output + excluded.output, ?10),
		cache_read = min(cache_read + excluded.cache_read, ?10), cache_write = min(cache_write + excluded.cache_write, ?10),
		cost_usd = cost_usd + excluded.cost_usd, requests = requests + 1`
)

func upsertSession(ctx context.Context, tx *sql.Tx, s domain.Session) error {
	state, err := textOf(s.State)
	if err != nil {
		return fmt.Errorf("store session %s: %w", s.ID, err)
	}
	kind, err := textOf(s.Kind)
	if err != nil {
		return fmt.Errorf("store session %s: %w", s.ID, err)
	}
	subagentIDs, err := encodeSubagentIDs(s.SubagentIDs())
	if err != nil {
		return fmt.Errorf("store session %s: %w", s.ID, err)
	}
	metrics, err := encodeMetrics(s.Metrics)
	if err != nil {
		return fmt.Errorf("store session %s: %w", s.ID, err)
	}

	var stored string
	err = tx.QueryRowContext(ctx, sessionStateSQL, s.ID).Scan(&stored)
	isNew := errors.Is(err, sql.ErrNoRows)
	if err != nil && !isNew {
		return fmt.Errorf("read session %s: %w", s.ID, err)
	}
	if _, err := tx.ExecContext(ctx, upsertSessionSQL,
		s.ID, s.Dir, kind, s.PID, s.Name, s.ProjectDir, state, s.WaitReason, s.EndReason,
		millis(s.StateAt), millis(s.StartedAt), millis(s.LastSeenAt), millis(s.LastStatusAt), millis(s.EndedAt),
		s.Lost, s.Subagents, subagentIDs, metrics,
	); err != nil {
		return fmt.Errorf("store session %s: %w", s.ID, err)
	}
	if !isNew && stored == state {
		return nil
	}
	if _, err := tx.ExecContext(ctx, insertStateChangeSQL, s.ID, millis(s.StateAt), state, cmp.Or(s.WaitReason, s.EndReason)); err != nil {
		return fmt.Errorf("store a state change of session %s: %w", s.ID, err)
	}
	return nil
}

func upsertAccount(ctx context.Context, tx *sql.Tx, a domain.Account) error {
	auth, err := textOf(a.Auth)
	if err != nil {
		return fmt.Errorf("store account %s: %w", a.Dir, err)
	}
	fiveHourPct, fiveHourResets := windowColumns(a.Limits.FiveHour)
	sevenDayPct, sevenDayResets := windowColumns(a.Limits.SevenDay)
	if _, err := tx.ExecContext(ctx, upsertAccountSQL,
		a.Dir, a.Label, a.Email, a.Plan, a.Org, auth, millis(a.ProbedAt),
		fiveHourPct, fiveHourResets, sevenDayPct, sevenDayResets, millis(a.Limits.ObservedAt),
	); err != nil {
		return fmt.Errorf("store account %s: %w", a.Dir, err)
	}
	return nil
}

// windowColumns are a window's usage and reset time; both are NULL for a
// window that was not reported.
func windowColumns(w *domain.Window) (sql.NullFloat64, sql.NullInt64) {
	if w == nil {
		return sql.NullFloat64{}, sql.NullInt64{}
	}
	return sql.NullFloat64{Float64: w.UsedPct, Valid: true}, millis(w.ResetsAt)
}

func addLimitSample(ctx context.Context, tx *sql.Tx, dir domain.ConfigDir, l domain.Limits) error {
	for _, sample := range []struct {
		window string
		w      *domain.Window
	}{{fiveHourWindow, l.FiveHour}, {sevenDayWindow, l.SevenDay}} {
		if sample.w == nil {
			continue
		}
		if err := insertSample(ctx, tx, dir, sample.window, domain.Sample{At: l.ObservedAt, UsedPct: sample.w.UsedPct, ResetsAt: sample.w.ResetsAt}); err != nil {
			return err
		}
	}
	return nil
}

func insertSample(ctx context.Context, tx *sql.Tx, dir domain.ConfigDir, window string, s domain.Sample) error {
	if _, err := tx.ExecContext(ctx, insertSampleSQL, dir, window, s.At.UnixMilli(), s.UsedPct, millis(s.ResetsAt)); err != nil {
		return fmt.Errorf("store a %s sample of %s: %w", window, dir, err)
	}
	return nil
}

func addUsage(ctx context.Context, tx *sql.Tx, dir domain.ConfigDir, r domain.APIRequest) error {
	if _, err := tx.ExecContext(ctx, addUsageSQL,
		minuteOf(r.At), r.Session, r.Model, dir, r.Input, r.Output, r.CacheRead, r.CacheWrite, r.CostUSD, maxCount,
	); err != nil {
		return fmt.Errorf("store usage of session %s: %w", r.Session, err)
	}
	return nil
}

const (
	loadAccountsSQL = `SELECT
		dir, label, email, plan, org, auth, probed_at,
		five_hour_pct, five_hour_resets_at, seven_day_pct, seven_day_resets_at, limits_observed_at
	FROM accounts ORDER BY dir`

	// A session is loaded when it has not ended or was seen since the cut,
	// and so are its state changes. The first parameter is the ended state.
	loadSessionsSQL = `SELECT
		id, dir, kind, pid, name, project_dir, state, wait_reason, end_reason,
		state_at, started_at, last_seen_at, last_status_at, ended_at,
		lost, subagents, subagent_ids, metrics_json
	FROM sessions WHERE state != ? OR last_seen_at >= ? ORDER BY id`

	// A timeline runs in the order its changes were written, which a clock
	// set back puts out of the order of their times.
	loadTimelinesSQL = `SELECT session, at, state, reason FROM (
		SELECT c.id, c.session, c.at, c.state, c.reason,
			row_number() OVER (PARTITION BY c.session ORDER BY c.id DESC) AS newness
		FROM state_changes AS c JOIN sessions AS s ON s.id = c.session
		WHERE s.state != ? OR s.last_seen_at >= ?
	) WHERE newness <= ? ORDER BY session, id`

	// Each window's samples come in the order they were taken, and those
	// taken at the same millisecond in the order they were written.
	loadSamplesSQL       = `SELECT dir, window, at, used_pct, resets_at FROM limit_samples WHERE at >= ? ORDER BY dir, at, rowid`
	loadSamplesBeforeSQL = `SELECT dir, window, at, used_pct, resets_at FROM limit_samples WHERE at < ? ORDER BY dir, at, rowid`

	loadUsageSQL = `SELECT minute, dir, session, model, input, output, cache_read, cache_write, cost_usd, requests
	FROM usage_minutes WHERE minute >= ? ORDER BY rowid`
)

// Load returns what a new run of the tracker needs: every account, the
// sessions that have not ended or were seen at or after since, with their
// last 50 state changes, and the samples and usage from since on. Each
// window's samples begin with the one that stands for a span on credits the
// earlier samples leave open at since, as Prune keeps it, so the spans come
// back the same whether or not a prune came first. Usage comes in the order
// its minutes were first written, the order the tracker sums it in. A row
// that cannot be read is left out and counted in skipped: one that a damaged
// file or another program left costs that row alone.
func (s *SQLite) Load(ctx context.Context, since time.Time) (restored tracker.Restored, skipped int, err error) {
	ended, err := textOf(domain.StateEnded)
	if err != nil {
		return tracker.Restored{}, 0, fmt.Errorf("load history: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return tracker.Restored{}, 0, fmt.Errorf("load history: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	l := &loader{tx: tx}
	cut := since.UnixMilli()
	var r tracker.Restored
	if r.Accounts, err = l.accounts(ctx); err != nil {
		return tracker.Restored{}, 0, fmt.Errorf("load history: %w", err)
	}
	if r.Sessions, err = l.sessions(ctx, ended, cut); err != nil {
		return tracker.Restored{}, 0, fmt.Errorf("load history: %w", err)
	}
	if r.Timelines, err = l.timelines(ctx, ended, cut); err != nil {
		return tracker.Restored{}, 0, fmt.Errorf("load history: %w", err)
	}
	if r.Samples, err = l.samplesFrom(ctx, cut); err != nil {
		return tracker.Restored{}, 0, fmt.Errorf("load history: %w", err)
	}
	if r.Usage, err = l.usage(ctx, cut); err != nil {
		return tracker.Restored{}, 0, fmt.Errorf("load history: %w", err)
	}
	return r, l.skipped, nil
}

// loader reads rows within one transaction, and counts those it cannot read.
type loader struct {
	tx      *sql.Tx
	skipped int
}

// each runs query and reads each row it returns with read, skipping and
// counting a row that read fails on.
func (l *loader) each(ctx context.Context, what, query string, read func(*sql.Rows) error, args ...any) error {
	rows, err := l.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("read %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if read(rows) != nil {
			l.skipped++
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read %s: %w", what, err)
	}
	return nil
}

func (l *loader) accounts(ctx context.Context) ([]domain.Account, error) {
	var accounts []domain.Account
	err := l.each(ctx, "accounts", loadAccountsSQL, func(rows *sql.Rows) error {
		var a domain.Account
		var auth string
		var probedAt, fiveHourResets, sevenDayResets, observedAt sql.NullInt64
		var fiveHourPct, sevenDayPct sql.NullFloat64
		if err := rows.Scan(&a.Dir, &a.Label, &a.Email, &a.Plan, &a.Org, &auth, &probedAt,
			&fiveHourPct, &fiveHourResets, &sevenDayPct, &sevenDayResets, &observedAt); err != nil {
			return err
		}
		if err := a.Auth.UnmarshalText([]byte(auth)); err != nil {
			return err
		}
		a.ProbedAt = fromMillis(probedAt)
		a.Limits = domain.Limits{
			FiveHour:   windowOf(fiveHourPct, fiveHourResets),
			SevenDay:   windowOf(sevenDayPct, sevenDayResets),
			ObservedAt: fromMillis(observedAt),
		}
		accounts = append(accounts, a)
		return nil
	})
	return accounts, err
}

func windowOf(pct sql.NullFloat64, resetsAt sql.NullInt64) *domain.Window {
	if !pct.Valid {
		return nil
	}
	return &domain.Window{UsedPct: pct.Float64, ResetsAt: fromMillis(resetsAt)}
}

func (l *loader) sessions(ctx context.Context, ended string, cut int64) ([]domain.Session, error) {
	var sessions []domain.Session
	err := l.each(ctx, "sessions", loadSessionsSQL, func(rows *sql.Rows) error {
		var s domain.Session
		var kind, state, subagentIDs, metrics string
		var stateAt, startedAt, lastSeenAt, lastStatusAt, endedAt sql.NullInt64
		if err := rows.Scan(&s.ID, &s.Dir, &kind, &s.PID, &s.Name, &s.ProjectDir, &state, &s.WaitReason, &s.EndReason,
			&stateAt, &startedAt, &lastSeenAt, &lastStatusAt, &endedAt,
			&s.Lost, &s.Subagents, &subagentIDs, &metrics); err != nil {
			return err
		}
		if err := s.Kind.UnmarshalText([]byte(kind)); err != nil {
			return err
		}
		if err := s.State.UnmarshalText([]byte(state)); err != nil {
			return err
		}
		var err error
		if s.Metrics, err = decodeMetrics(metrics); err != nil {
			return err
		}
		ids, err := decodeSubagentIDs(subagentIDs)
		if err != nil {
			return err
		}
		s.StateAt, s.StartedAt, s.LastSeenAt = fromMillis(stateAt), fromMillis(startedAt), fromMillis(lastSeenAt)
		s.LastStatusAt, s.EndedAt = fromMillis(lastStatusAt), fromMillis(endedAt)
		sessions = append(sessions, s.WithSubagentIDs(ids))
		return nil
	}, ended, cut)
	return sessions, err
}

func (l *loader) timelines(ctx context.Context, ended string, cut int64) (map[domain.SessionID][]domain.Transition, error) {
	timelines := make(map[domain.SessionID][]domain.Transition)
	err := l.each(ctx, "state changes", loadTimelinesSQL, func(rows *sql.Rows) error {
		var session domain.SessionID
		var at sql.NullInt64
		var state string
		var tr domain.Transition
		if err := rows.Scan(&session, &at, &state, &tr.Reason); err != nil {
			return err
		}
		if err := tr.State.UnmarshalText([]byte(state)); err != nil {
			return err
		}
		tr.At = fromMillis(at)
		timelines[session] = append(timelines[session], tr)
		return nil
	}, ended, cut, maxTimeline)
	return timelines, err
}

// samplesFrom returns each account's samples from cut on, after the one that
// stands for a span on credits the earlier samples leave open at cut.
func (l *loader) samplesFrom(ctx context.Context, cut int64) (map[domain.ConfigDir]domain.WindowSamples, error) {
	samples, err := l.carried(ctx, cut)
	if err != nil {
		return nil, err
	}
	later, err := l.samples(ctx, loadSamplesSQL, cut)
	if err != nil {
		return nil, err
	}
	for dir, w := range later {
		c := samples[dir]
		samples[dir] = domain.WindowSamples{FiveHour: slices.Concat(c.FiveHour, w.FiveHour), SevenDay: slices.Concat(c.SevenDay, w.SevenDay)}
	}
	return samples, nil
}

// carried returns what the tracker keeps of each window's samples before
// cut: at most one, which stands for the span on credits they leave open
// there, dated at the span's start (see domain.ForgetOnCreditsBefore).
func (l *loader) carried(ctx context.Context, cut int64) (map[domain.ConfigDir]domain.WindowSamples, error) {
	earlier, err := l.samples(ctx, loadSamplesBeforeSQL, cut)
	if err != nil {
		return nil, err
	}
	carried := make(map[domain.ConfigDir]domain.WindowSamples)
	for dir, w := range earlier {
		var kept domain.WindowSamples
		kept.FiveHour = append(kept.FiveHour, carriedBefore(w.FiveHour, cut)...)
		kept.SevenDay = append(kept.SevenDay, carriedBefore(w.SevenDay, cut)...)
		if len(kept.FiveHour) > 0 || len(kept.SevenDay) > 0 {
			carried[dir] = kept
		}
	}
	return carried, nil
}

// carriedBefore reduces one window's samples, all taken before cut and in
// time order, to what the tracker keeps of them.
func carriedBefore(samples []domain.Sample, cut int64) []domain.Sample {
	// OnCreditsMarks mirrors the tracker's pruneWindow; it changes nothing here, as every row is before the cut.
	return domain.ForgetOnCreditsBefore(domain.OnCreditsMarks(samples), instant(cut))
}

// samples returns the limit samples query reads, each window's in time order.
func (l *loader) samples(ctx context.Context, query string, cut int64) (map[domain.ConfigDir]domain.WindowSamples, error) {
	samples := make(map[domain.ConfigDir]domain.WindowSamples)
	err := l.each(ctx, "limit samples", query, func(rows *sql.Rows) error {
		var dir domain.ConfigDir
		var window string
		var at int64
		var usedPct float64
		var resetsAt sql.NullInt64
		if err := rows.Scan(&dir, &window, &at, &usedPct, &resetsAt); err != nil {
			return err
		}
		sample := domain.Sample{At: instant(at), UsedPct: usedPct, ResetsAt: fromMillis(resetsAt)}
		windows := samples[dir]
		switch window {
		case fiveHourWindow:
			windows.FiveHour = append(windows.FiveHour, sample)
		case sevenDayWindow:
			windows.SevenDay = append(windows.SevenDay, sample)
		default:
			return fmt.Errorf("unknown window %q", window)
		}
		samples[dir] = windows
		return nil
	}, cut)
	return samples, err
}

func (l *loader) usage(ctx context.Context, cut int64) ([]tracker.UsageRow, error) {
	var usage []tracker.UsageRow
	err := l.each(ctx, "usage", loadUsageSQL, func(rows *sql.Rows) error {
		var r tracker.UsageRow
		var minute int64
		if err := rows.Scan(&minute, &r.Dir, &r.Session, &r.Model,
			&r.Input, &r.Output, &r.CacheRead, &r.CacheWrite, &r.CostUSD, &r.Requests); err != nil {
			return err
		}
		r.Minute = instant(minute)
		usage = append(usage, r)
		return nil
	}, cut)
	return usage, err
}

const (
	pruneSamplesSQL      = `DELETE FROM limit_samples WHERE at < ?`
	pruneUsageSQL        = `DELETE FROM usage_minutes WHERE minute < ?`
	pruneStateChangesSQL = `DELETE FROM state_changes WHERE at < ?`
	// A session goes once it was neither seen nor ended since the cut; its
	// state changes go with it.
	pruneSessionsSQL = `DELETE FROM sessions WHERE max(coalesce(last_seen_at, 0), coalesce(ended_at, 0)) < ?`
)

// Prune deletes what is older than before: limit samples, usage minutes and
// state changes, and the sessions last seen or ended before it, with their
// state changes. Accounts are kept, and so is, for each window, the one
// sample that stands for a span on credits still open at before, dated at the
// span's start: a span that began before the retention still holds the calls
// made within it, and still dates the account going on credits, after a
// restart.
func (s *SQLite) Prune(ctx context.Context, before time.Time) error {
	cut := before.UnixMilli()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		// A row that cannot be read is pruned all the same, and stands for
		// nothing.
		carried, err := (&loader{tx: tx}).carried(ctx, cut)
		if err != nil {
			return fmt.Errorf("prune limit samples: %w", err)
		}
		for _, prune := range []struct{ what, query string }{
			{"limit samples", pruneSamplesSQL},
			{"usage", pruneUsageSQL},
			{"state changes", pruneStateChangesSQL},
			{"sessions", pruneSessionsSQL},
		} {
			if _, err := tx.ExecContext(ctx, prune.query, cut); err != nil {
				return fmt.Errorf("prune %s: %w", prune.what, err)
			}
		}
		return keepCarried(ctx, tx, carried)
	})
}

// keepCarried stores the samples that stand for the spans on credits a prune
// cut across.
func keepCarried(ctx context.Context, tx *sql.Tx, carried map[domain.ConfigDir]domain.WindowSamples) error {
	for _, dir := range slices.Sorted(maps.Keys(carried)) {
		for _, window := range []struct {
			name    string
			samples []domain.Sample
		}{{fiveHourWindow, carried[dir].FiveHour}, {sevenDayWindow, carried[dir].SevenDay}} {
			for _, sample := range window.samples {
				if err := insertSample(ctx, tx, dir, window.name, sample); err != nil {
					return fmt.Errorf("prune limit samples: %w", err)
				}
			}
		}
	}
	return nil
}
