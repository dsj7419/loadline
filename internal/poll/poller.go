// Package poll keeps the live state in step with `claude agents --json`,
// the source of truth for sessions. It polls every known config dir, often
// while any session is live and seldom while none is, and once more soon
// after a hook says something changed.
package poll

import (
	"context"
	"sync"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/timer"
)

const (
	// DefaultFast spaces polls while any session is live.
	DefaultFast = 5 * time.Second
	// DefaultSlow spaces polls while no session is live. They find the
	// sessions that started before any hook could report them.
	DefaultSlow = 30 * time.Second
	// DefaultDebounce is how long after a Kick the poll it asks for comes,
	// so that the session's process has caught up with what its hook said.
	DefaultDebounce = 500 * time.Millisecond
)

// Sink is the live state a Poller feeds. *tracker.Tracker is one.
type Sink interface {
	// ApplyPoll folds in one poll of dir, taken at at.
	ApplyPoll(dir domain.ConfigDir, at time.Time, polls []domain.Poll)
	// KnownDirs returns the config dirs to poll.
	KnownDirs() []domain.ConfigDir
	// HasLive reports whether any session is live.
	HasLive() bool
}

// Poller polls the sessions of every dir its Sink knows. The exported fields
// are set before Run is called and not changed after. Run and Kick are safe
// to call concurrently.
type Poller struct {
	// List returns the sessions of the account in dir, each stamped at.
	List func(ctx context.Context, dir domain.ConfigDir, at time.Time) ([]domain.Poll, error)
	Sink Sink
	// Fast and Slow space the polls while a session is live and while none
	// is, and Debounce is how long a Kick waits. Zero means DefaultFast,
	// DefaultSlow and DefaultDebounce.
	Fast, Slow, Debounce time.Duration
	// Clock reads the time. Nil means time.Now.
	Clock func() time.Time
	// AfterFunc schedules the next poll. Nil means wall-clock timers.
	AfterFunc timer.AfterFunc
	// OnPoll is called with the time of each poll of a dir that succeeded.
	// Nil means nothing is called.
	OnPoll func(at time.Time)

	kicksOnce sync.Once
	kicks     chan struct{}
}

// Run polls every known dir at once, and again after each interval or Kick,
// until ctx ends. The dirs of a round are polled side by side, so within a
// round a slow `claude` for one account holds up no other; the next round
// waits for the slowest. A poll that fails is skipped: it says nothing of
// which sessions are gone.
func (p *Poller) Run(ctx context.Context) {
	// A kick's poll outlives the round it was asked for in: a poll on
	// schedule just after a hook may still miss what the hook reported.
	var kick pendingKick
	defer kick.cancel()

	for {
		if ctx.Err() != nil {
			return
		}
		p.pollAll(ctx)
		if ctx.Err() != nil {
			return
		}

		// Each timer signals a channel of its own, so one that fires as it is
		// stopped cannot start a later round early.
		due := make(chan struct{}, 1)
		stopDue := p.AfterFunc.Start(p.interval(), timer.Notify(due))
		ok := p.wait(ctx, due, &kick)
		stopDue()
		if !ok {
			return
		}
	}
}

// pendingKick is the poll a Kick asked for, while it is still to come.
type pendingKick struct {
	// due is signaled when the poll is due; nil while no kick is pending.
	due  chan struct{}
	stop func() bool
}

func (k *pendingKick) cancel() {
	if k.stop != nil {
		k.stop()
	}
}

// wait blocks until the next poll is due, on schedule or because of a kick,
// and arms the debounce for the first Kick it sees. It reports false when
// ctx ended first.
func (p *Poller) wait(ctx context.Context, due <-chan struct{}, kick *pendingKick) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-due:
			return true
		case <-p.kicked():
			if kick.due == nil {
				kick.due = make(chan struct{}, 1)
				kick.stop = p.AfterFunc.Start(p.debounce(), timer.Notify(kick.due))
			}
		case <-kick.due:
			kick.due, kick.stop = nil, nil
			// The poll about to run covers every kick so far.
			select {
			case <-p.kicked():
			default:
			}
			return true
		}
	}
}

// Kick asks for a poll Debounce from now, to confirm what a hook reported.
// Kicks that come while one is pending add nothing. It never blocks.
func (p *Poller) Kick() {
	select {
	case p.kicked() <- struct{}{}:
	default:
	}
}

func (p *Poller) kicked() chan struct{} {
	p.kicksOnce.Do(func() { p.kicks = make(chan struct{}, 1) })
	return p.kicks
}

func (p *Poller) pollAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, dir := range p.Sink.KnownDirs() {
		wg.Go(func() { p.pollDir(ctx, dir) })
	}
	wg.Wait()
}

// pollDir polls one dir. The time is read before listing, so that a hook
// that arrives while `claude agents` runs counts as newer than the poll.
func (p *Poller) pollDir(ctx context.Context, dir domain.ConfigDir) {
	at := p.now()
	polls, err := p.List(ctx, dir, at)
	if err != nil {
		return
	}
	p.Sink.ApplyPoll(dir, at, polls)
	if p.OnPoll != nil {
		p.OnPoll(at)
	}
}

func (p *Poller) interval() time.Duration {
	if p.Sink.HasLive() {
		return orDefault(p.Fast, DefaultFast)
	}
	return orDefault(p.Slow, DefaultSlow)
}

func (p *Poller) debounce() time.Duration { return orDefault(p.Debounce, DefaultDebounce) }

func (p *Poller) now() time.Time {
	if p.Clock == nil {
		return time.Now()
	}
	return p.Clock()
}

func orDefault(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}
