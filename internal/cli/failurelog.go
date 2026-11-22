package cli

import (
	"log/slog"
	"sync"
	"time"
)

// failureRepeat is how long a failure goes unlogged after it was logged,
// for as long as it repeats exactly.
const failureRepeat = time.Minute

// failureLog logs the failures of work that repeats, such as polls, no more
// than once a minute for each failure and subject: a missing or broken
// claude fails every poll the same way.
type failureLog struct {
	log   *slog.Logger
	clock func() time.Time

	mu     sync.Mutex
	logged map[failure]time.Time
}

type failure struct{ what, subject, err string }

func newFailureLog(log *slog.Logger, clock func() time.Time) *failureLog {
	return &failureLog{log: log, clock: clock, logged: make(map[failure]time.Time)}
}

// note logs that what failed for subject with err, unless it logged the same
// failure less than a minute ago. A clock that stepped back counts the
// minute as over.
func (f *failureLog) note(what, subject string, err error) {
	key := failure{what: what, subject: subject, err: err.Error()}
	now := f.clock()
	f.mu.Lock()
	for k, at := range f.logged {
		if now.Sub(at) >= failureRepeat || now.Before(at) {
			delete(f.logged, k)
		}
	}
	_, recent := f.logged[key]
	if !recent {
		f.logged[key] = now
	}
	f.mu.Unlock()
	if !recent {
		f.log.Warn(what, "subject", subject, "err", err)
	}
}
