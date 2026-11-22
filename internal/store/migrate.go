package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNewerSchema is the error Open returns for a database that a newer
// Loadline has migrated beyond the schema this one knows.
var ErrNewerSchema = errors.New("the history database was written by a newer loadline")

// migrations build the schema step by step. The database's user_version is
// the number of steps applied, and each step sets it to its own number, as
// its last statement, inside the transaction that applies it. A step, once
// released, never changes: a change to the schema is a new step.
var migrations = []string{
	`CREATE TABLE accounts (
		dir                 TEXT PRIMARY KEY,
		label               TEXT NOT NULL,
		email               TEXT NOT NULL,
		plan                TEXT NOT NULL,
		org                 TEXT NOT NULL,
		auth                TEXT NOT NULL,
		probed_at           INTEGER,
		five_hour_pct       REAL,
		five_hour_resets_at INTEGER,
		seven_day_pct       REAL,
		seven_day_resets_at INTEGER,
		limits_observed_at  INTEGER
	) STRICT;

	CREATE TABLE sessions (
		id             TEXT PRIMARY KEY,
		dir            TEXT NOT NULL,
		kind           TEXT NOT NULL,
		pid            INTEGER NOT NULL,
		name           TEXT NOT NULL,
		project_dir    TEXT NOT NULL,
		state          TEXT NOT NULL,
		wait_reason    TEXT NOT NULL,
		end_reason     TEXT NOT NULL,
		state_at       INTEGER,
		started_at     INTEGER,
		last_seen_at   INTEGER,
		last_status_at INTEGER,
		ended_at       INTEGER,
		lost           INTEGER NOT NULL,
		subagents      INTEGER NOT NULL,
		subagent_ids   TEXT NOT NULL,
		metrics_json   TEXT NOT NULL
	) STRICT;

	CREATE TABLE state_changes (
		id      INTEGER PRIMARY KEY,
		session TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
		at      INTEGER,
		state   TEXT NOT NULL,
		reason  TEXT NOT NULL
	) STRICT;
	CREATE INDEX state_changes_by_session ON state_changes (session, at);

	CREATE TABLE limit_samples (
		dir       TEXT NOT NULL,
		window    TEXT NOT NULL CHECK (window IN ('five_hour', 'seven_day')),
		at        INTEGER NOT NULL,
		used_pct  REAL NOT NULL,
		resets_at INTEGER
	) STRICT;
	CREATE INDEX limit_samples_by_window ON limit_samples (dir, window, at);

	CREATE TABLE usage_minutes (
		minute      INTEGER NOT NULL,
		session     TEXT NOT NULL,
		model       TEXT NOT NULL,
		dir         TEXT NOT NULL,
		input       INTEGER NOT NULL,
		output      INTEGER NOT NULL,
		cache_read  INTEGER NOT NULL,
		cache_write INTEGER NOT NULL,
		cost_usd    REAL NOT NULL,
		requests    INTEGER NOT NULL,
		PRIMARY KEY (minute, session, model)
	) STRICT;

	PRAGMA user_version = 1;`,
}

const readVersionSQL = `PRAGMA user_version`

// migrate brings db's schema up to date, applying each step it lacks in a
// transaction of its own. It refuses a database a newer Loadline migrated
// further, before it changes anything. Another process may migrate the same
// database at once: each step reads the version again once it holds the
// write lock, and skips what the other applied.
func migrate(ctx context.Context, db *sql.DB) error {
	version, err := schemaVersion(ctx, db)
	if err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("%w: its schema is version %d, and this one knows up to %d", ErrNewerSchema, version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		if err := applyStep(ctx, db, i+1); err != nil {
			return err
		}
	}
	return nil
}

func applyStep(ctx context.Context, db *sql.DB, step int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", step, err)
	}
	defer func() { _ = tx.Rollback() }()

	version, err := schemaVersion(ctx, tx)
	if err != nil {
		return err
	}
	if version >= step {
		return nil
	}
	if _, err := tx.ExecContext(ctx, migrations[step-1]); err != nil {
		return fmt.Errorf("apply migration %d: %w", step, err)
	}
	if version, err = schemaVersion(ctx, tx); err != nil {
		return err
	}
	if version != step {
		return fmt.Errorf("migration %d left the schema at version %d", step, version)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", step, err)
	}
	return nil
}

// queryRower is a database or a transaction.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func schemaVersion(ctx context.Context, q queryRower) (int, error) {
	var version int
	if err := q.QueryRowContext(ctx, readVersionSQL).Scan(&version); err != nil {
		return 0, fmt.Errorf("read the schema version: %w", err)
	}
	return version, nil
}
