package store

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/tracker"
)

const (
	// queueSize is how many changes wait to be written; what comes while
	// the queue is full is dropped.
	queueSize = 4096
	// batchSize and batchEvery bound how long a change waits: a batch is
	// written as soon as it holds batchSize changes, and whatever is queued
	// is written at each tick of batchEvery, so a change waits at most one
	// tick, plus any write still in progress.
	batchSize  = 100
	batchEvery = 250 * time.Millisecond
	// failureLogEvery spaces the warnings about writes that fail.
	failureLogEvery = time.Minute
)

// write is one change to the history, written inside a batch's transaction.
type write func(context.Context, *sql.Tx) error

// AsyncRecorder is the tracker's Recorder: it queues each change the
// tracker reports and writes the queue to the history in batches, one
// transaction a batch, from a goroutine of its own. A change never waits
// for the database; one that comes when the queue is full is dropped and
// counted. A batch that fails is logged, at most once a minute, and its
// changes are counted as lost.
type AsyncRecorder struct {
	store *SQLite
	log   *slog.Logger

	// mu guards closed and the sends on queue, which Close closes.
	mu     sync.RWMutex
	closed bool
	queue  chan write

	dropped, failed atomic.Int64

	// abandon ends the writes in progress, for a Close whose deadline
	// passed.
	abandon context.CancelFunc
	done    chan struct{}

	// The writer goroutine alone uses these.
	loggedAt time.Time
	unlogged int64
}

var _ tracker.Recorder = (*AsyncRecorder)(nil)

// NewAsyncRecorder returns a recorder that writes to s and logs to log, and
// starts its writer. Close stops it.
func NewAsyncRecorder(s *SQLite, log *slog.Logger) *AsyncRecorder {
	ctx, abandon := context.WithCancel(context.Background())
	r := &AsyncRecorder{
		store:   s,
		log:     log,
		queue:   make(chan write, queueSize),
		abandon: abandon,
		done:    make(chan struct{}),
	}
	go r.run(ctx)
	return r
}

// SessionChanged implements tracker.Recorder.
func (r *AsyncRecorder) SessionChanged(s domain.Session) {
	r.enqueue(func(ctx context.Context, tx *sql.Tx) error { return upsertSession(ctx, tx, s) })
}

// AccountChanged implements tracker.Recorder. Each upsert replaces the
// account's row, so however often the account is reported it is stored
// once.
func (r *AsyncRecorder) AccountChanged(a domain.Account) {
	a.Limits = ownLimits(a.Limits)
	r.enqueue(func(ctx context.Context, tx *sql.Tx) error { return upsertAccount(ctx, tx, a) })
}

// LimitSampled implements tracker.Recorder.
func (r *AsyncRecorder) LimitSampled(dir domain.ConfigDir, l domain.Limits) {
	l = ownLimits(l)
	r.enqueue(func(ctx context.Context, tx *sql.Tx) error { return addLimitSample(ctx, tx, dir, l) })
}

// ownLimits copies the windows of l, so that the queue shares none with the
// caller: they are written later, on another goroutine.
func ownLimits(l domain.Limits) domain.Limits {
	l.FiveHour, l.SevenDay = ownWindow(l.FiveHour), ownWindow(l.SevenDay)
	return l
}

func ownWindow(w *domain.Window) *domain.Window {
	if w == nil {
		return nil
	}
	c := *w
	return &c
}

// UsageRecorded implements tracker.Recorder.
func (r *AsyncRecorder) UsageRecorded(dir domain.ConfigDir, req domain.APIRequest) {
	r.enqueue(func(ctx context.Context, tx *sql.Tx) error { return addUsage(ctx, tx, dir, req) })
}

// Dropped returns how many changes were dropped, because the queue was full
// or the recorder closed.
func (r *AsyncRecorder) Dropped() int64 { return r.dropped.Load() }

// Errors returns how many changes were lost to writes that failed.
func (r *AsyncRecorder) Errors() int64 { return r.failed.Load() }

// enqueue queues w without waiting: the tracker holds its lock while it
// reports a change.
func (r *AsyncRecorder) enqueue(w write) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		r.dropped.Add(1)
		return
	}
	select {
	case r.queue <- w:
	default:
		r.dropped.Add(1)
	}
}

// Close stops accepting changes and waits for those queued to be written.
// When ctx ends first, it abandons the writes and returns ctx's error.
func (r *AsyncRecorder) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.queue)
	}
	r.mu.Unlock()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		r.abandon()
		return ctx.Err()
	}
}

// run writes the queue in batches until Close closes it and what was left
// in it is written.
func (r *AsyncRecorder) run(ctx context.Context) {
	defer close(r.done)
	defer r.abandon()
	tick := time.NewTicker(batchEvery)
	defer tick.Stop()
	batch := make([]write, 0, batchSize)
	for {
		select {
		case w, ok := <-r.queue:
			if !ok {
				r.flush(ctx, batch)
				return
			}
			if batch = append(batch, w); len(batch) == batchSize {
				r.flush(ctx, batch)
				batch = batch[:0]
			}
		case <-tick.C:
			r.flush(ctx, batch)
			batch = batch[:0]
		}
	}
}

// flush writes batch in one transaction. When one of its writes fails, the
// transaction takes the others with it, so they are written again one at a
// time, and only the write that fails is lost. A batch that fails as it
// begins or commits fails as a whole, as the database does then: written one
// at a time, each would wait out the busy timeout in turn.
func (r *AsyncRecorder) flush(ctx context.Context, batch []write) {
	if len(batch) == 0 {
		return
	}
	writeFailed := false
	err := r.store.inTx(ctx, func(tx *sql.Tx) error {
		for _, w := range batch {
			if err := w(ctx, tx); err != nil {
				writeFailed = true
				return err
			}
		}
		return nil
	})
	switch {
	case err == nil:
	case writeFailed:
		for _, w := range batch {
			if err := r.store.inTx(ctx, func(tx *sql.Tx) error { return w(ctx, tx) }); err != nil {
				r.noteFailure(err, 1)
			}
		}
	default:
		r.noteFailure(err, int64(len(batch)))
	}
}

// noteFailure counts the changes a failed write lost, and logs the failure
// unless it logged one less than failureLogEvery ago. A database that fails
// keeps failing: a disk that is full, or a file another program locked.
func (r *AsyncRecorder) noteFailure(err error, lost int64) {
	r.failed.Add(lost)
	r.unlogged += lost
	now := time.Now()
	if !r.loggedAt.IsZero() && now.Sub(r.loggedAt) < failureLogEvery {
		return
	}
	r.log.Warn("history write failed", "err", err, "lost", r.unlogged, "lostInAll", r.failed.Load())
	r.loggedAt, r.unlogged = now, 0
}
