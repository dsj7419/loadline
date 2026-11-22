package cli

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/dsj7419/loadline/internal/store"
	"github.com/dsj7419/loadline/internal/timer"
	"github.com/dsj7419/loadline/internal/tracker"
)

const (
	day = 24 * time.Hour
	// pruneEvery spaces the prunes of the history after the one at startup.
	pruneEvery = day
)

// history keeps what the tracker learns across restarts, for the configured
// number of days. A nil history keeps nothing: the server runs without one
// when its database cannot be opened.
type history struct {
	path      string
	db        *store.SQLite
	rec       *store.AsyncRecorder
	retention time.Duration
	clock     func() time.Time
	after     timer.AfterFunc
	log       *slog.Logger
	failures  *failureLog
}

// openHistory opens the history database. One that cannot be opened is
// logged and left out: the dashboard works as well without it, and only
// forgets what it saw when the server restarts.
func (s *instance) openHistory(ctx context.Context, retentionDays int) *history {
	path := s.opts.Paths.DBFile
	db, err := store.Open(ctx, path)
	if err != nil {
		s.log.Warn("history is off: the dashboard forgets what it saw when the server restarts", "err", err)
		return nil
	}
	return &history{
		path:      path,
		db:        db,
		rec:       store.NewAsyncRecorder(db, s.log), //nolint:contextcheck // its writer outlives ctx: it writes what is queued after the server stops
		retention: retentionOf(retentionDays),
		clock:     s.clock,
		after:     s.after,
		log:       s.log,
		failures:  s.failures,
	}
}

// retentionOf is the span of days, or the longest time.Duration for a span
// longer than one counts: some 292 years, which keeps everything as well.
func retentionOf(days int) time.Duration {
	if int64(days) > math.MaxInt64/int64(day) {
		return math.MaxInt64
	}
	return time.Duration(days) * day
}

// recorder is what the tracker reports its changes to.
func (h *history) recorder() tracker.Recorder {
	if h == nil {
		return tracker.NopRecorder{}
	}
	return h.rec
}

// restore gives the tracker what the history kept from earlier runs, as far
// back as the retention reaches, which covers every total the view shows.
func (h *history) restore(ctx context.Context, t *tracker.Tracker) {
	if h == nil {
		return
	}
	restored, err := h.db.Load(ctx, h.clock().Add(-h.retention))
	if err != nil {
		h.log.Warn("history not restored", "err", err)
		return
	}
	t.Restore(restored)
	h.log.Info("history restored", "accounts", len(restored.Accounts), "sessions", len(restored.Sessions))
}

// pruneEvery forgets what is older than the retention, now and then every
// pruneEvery, until ctx ends.
func (h *history) pruneEvery(ctx context.Context) {
	if h == nil {
		return
	}
	for {
		if err := h.db.Prune(ctx, h.clock().Add(-h.retention)); err != nil && ctx.Err() == nil {
			h.failures.note("history prune failed", h.path, err)
		}
		if !h.after.Sleep(ctx, pruneEvery) {
			return
		}
	}
}

// close writes what the recorder still holds, until ctx ends, and closes
// the database. It logs what the history missed.
func (h *history) close(ctx context.Context) {
	if h == nil {
		return
	}
	if err := h.rec.Close(ctx); err != nil {
		h.log.Warn("history not all written at shutdown", "err", err)
	}
	if dropped, failed := h.rec.Dropped(), h.rec.Errors(); dropped > 0 || failed > 0 {
		h.log.Warn("history missed changes", "dropped", dropped, "failed", failed)
	}
	if err := h.db.Close(); err != nil {
		h.log.Warn("history close failed", "err", err)
	}
}
