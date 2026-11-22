// Package accounts keeps each account's identity current: who its config
// dir is signed in as, and how, as `claude auth status` reports it.
package accounts

import (
	"cmp"
	"context"
	"sync"
	"time"

	"github.com/dsj7419/loadline/internal/claudecli"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/timer"
)

const (
	// DefaultEvery is how often each account is probed again.
	DefaultEvery = 6 * time.Hour
	// firstRetry is how soon a failed probe is tried again. A probe fails
	// for a moment, as while claude is being updated, more often than for
	// good.
	firstRetry = 5 * time.Minute
)

// Prober asks who each observed account is: once when its config dir is
// first observed, and again on a schedule: Every after a probe that worked,
// and sooner after one that failed, until it is forgotten. The exported
// fields are set before Run is called and not changed after. Run, Observe
// and Forget are safe to call concurrently.
type Prober struct {
	// Status asks claude who the account in dir is signed in as.
	Status func(ctx context.Context, dir domain.ConfigDir) (claudecli.AuthStatus, error)
	// Set receives the account each successful probe found.
	Set func(domain.Account)
	// Labels are the accounts' display names, keyed by normalized config
	// dir.
	Labels map[domain.ConfigDir]string
	// Every spaces the probes of one dir while they work, and bounds the
	// wait after one that failed. Zero means DefaultEvery.
	Every time.Duration
	// Clock reads the time. Nil means time.Now.
	Clock func() time.Time
	// AfterFunc schedules the next probe. Nil means wall-clock timers.
	AfterFunc timer.AfterFunc

	mu   sync.Mutex
	dirs map[domain.ConfigDir]*schedule

	wakeOnce sync.Once
	wake     chan struct{}
}

// schedule is when one dir is next probed.
type schedule struct {
	// next is when the dir is due; the zero time means at once.
	next time.Time
	// retry is how long to wait should the next probe fail; zero means
	// firstRetry.
	retry   time.Duration
	probing bool
}

// Observe notes dir as an account to probe. The first Observe of a dir has
// it probed at once; later ones change nothing. It never blocks: the probe
// runs on Run's behalf.
func (p *Prober) Observe(dir domain.ConfigDir) {
	p.mu.Lock()
	if p.dirs == nil {
		p.dirs = make(map[domain.ConfigDir]*schedule)
	}
	_, known := p.dirs[dir]
	if !known {
		p.dirs[dir] = &schedule{}
	}
	p.mu.Unlock()
	if !known {
		p.signal()
	}
}

// Forget stops probing dir until it is observed again, which probes it at
// once. A probe of it already running finishes, and schedules nothing.
func (p *Prober) Forget(dir domain.ConfigDir) {
	p.mu.Lock()
	delete(p.dirs, dir)
	p.mu.Unlock()
	// The wait for dir's next probe may be the one Run is in.
	p.signal()
}

// Run probes each observed dir when it is due, until ctx ends, and returns
// once every probe it started has returned. Dirs are probed side by side, so
// a slow `claude` for one account holds up no other. A probe that fails sets
// nothing, leaving the account as the last probe found it. It is tried again
// after five minutes, then after twice as long each time it fails again, up
// to Every.
func (p *Prober) Run(ctx context.Context) {
	var probes sync.WaitGroup
	defer probes.Wait()
	stop := func() bool { return false }
	defer func() { stop() }()

	for {
		wait, scheduled := p.startDue(ctx, &probes)
		stop()
		// A channel per timer, so that one that fires as it is stopped
		// cannot wake a later wait.
		due := make(chan struct{}, 1)
		stop = func() bool { return false }
		if scheduled {
			stop = p.AfterFunc.Start(wait, timer.Notify(due))
		}
		select {
		case <-ctx.Done():
			return
		case <-p.woken():
		case <-due:
		}
	}
}

// startDue starts a probe of every dir that is due and not already being
// probed, and returns how long until the next of the others is due.
// scheduled is false when no other is waiting.
func (p *Prober) startDue(ctx context.Context, probes *sync.WaitGroup) (wait time.Duration, scheduled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for dir, s := range p.dirs {
		if s.probing {
			continue
		}
		if !s.next.After(now) {
			s.probing = true
			probes.Go(func() { p.probe(ctx, dir) })
			continue
		}
		if left := s.next.Sub(now); !scheduled || left < wait {
			wait, scheduled = left, true
		}
	}
	return wait, scheduled
}

// probe asks who the account in dir is, sets what it finds, and schedules
// the next probe, counted from when this one returned.
func (p *Prober) probe(ctx context.Context, dir domain.ConfigDir) {
	status, err := p.Status(ctx, dir)
	at := p.now()
	if err == nil {
		p.Set(status.Account(dir, p.Labels[dir], at))
	}

	p.mu.Lock()
	s, known := p.dirs[dir]
	if !known {
		// Forgotten while it was being probed.
		p.mu.Unlock()
		return
	}
	s.probing = false
	if err == nil {
		s.next, s.retry = at.Add(p.every()), 0
	} else {
		wait := min(cmp.Or(s.retry, firstRetry), p.every())
		s.next, s.retry = at.Add(wait), min(2*wait, p.every())
	}
	p.mu.Unlock()
	p.signal()
}

func (p *Prober) signal() {
	select {
	case p.woken() <- struct{}{}:
	default:
	}
}

func (p *Prober) woken() chan struct{} {
	p.wakeOnce.Do(func() { p.wake = make(chan struct{}, 1) })
	return p.wake
}

func (p *Prober) every() time.Duration {
	if p.Every > 0 {
		return p.Every
	}
	return DefaultEvery
}

func (p *Prober) now() time.Time {
	if p.Clock == nil {
		return time.Now()
	}
	return p.Clock()
}
