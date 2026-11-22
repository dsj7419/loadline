package tracker

import (
	"maps"
	"slices"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

// maxClockStepBack is how far the clock may go back before the tracker takes
// it for the system clock being set back, as a time sync does to a clock
// that ran fast, rather than for clocks that disagree a little.
const maxClockStepBack = 10 * time.Minute

// now reads the clock. A reading more than maxClockStepBack before the newest
// one taken so far means the system clock was set back. Everything stamped
// before then is dated ahead of what is stamped after, and would make it all
// look stale, so the tracker moves the times it holds back by the step. It
// logs a warning when it does.
func (t *Tracker) now() time.Time {
	// The times the tracker holds are wall times, and a step of the system
	// clock moves the wall time alone: the monotonic reading must not hide
	// it.
	now := t.clock().Round(0)
	switch step := t.newest.Sub(now); {
	case step < 0:
		t.newest = now
	case step > maxClockStepBack:
		t.log.Warn("the system clock was set back; moving the times observed before it back to match", "by", step)
		t.rebase(-step)
		t.newest = now
	}
	return now
}

// notAhead returns at, or now when at is more than maxClockStepBack after
// it: a time stamped before the clock was set back.
func notAhead(at, now time.Time) time.Time {
	if at.After(now.Add(maxClockStepBack)) {
		return now
	}
	return at
}

// rebase moves every time the tracker holds by d, unset ones aside, and
// reports each session and account to the Recorder as it then stands. Usage
// stays on the local day its calls were made on.
func (t *Tracker) rebase(d time.Duration) {
	for _, id := range slices.Sorted(maps.Keys(t.sessions)) {
		e := t.sessions[id]
		e.session = e.session.Shifted(d)
		e.firstSeen = e.firstSeen.Add(d)
		for i := range e.timeline {
			e.timeline[i].At = e.timeline[i].At.Add(d)
		}
		t.rec.SessionChanged(e.session)
	}
	for _, dir := range slices.Sorted(maps.Keys(t.accounts)) {
		entry := t.accounts[dir]
		entry.shift(d)
		t.rec.AccountChanged(entry.account)
	}
	t.usage.shift(d)
	t.pending.shift(d)
	t.alerts.shift(d)
}

// shift moves the times the account was probed and observed at, those its
// windows' readings were taken at, and the times of the calls and report its
// credits are worked out from, by d. Reset times stay as they are: Claude
// Code reports them from the service's clock.
func (a *accountEntry) shift(d time.Duration) {
	a.account.ProbedAt = shifted(a.account.ProbedAt, d)
	a.account.Limits.ObservedAt = shifted(a.account.Limits.ObservedAt, d)
	for _, r := range []*heldReading{&a.readings.FiveHour, &a.readings.SevenDay} {
		r.takenAt = shifted(r.takenAt, d)
	}
	for _, samples := range [][]domain.Sample{a.samples.FiveHour, a.samples.SevenDay} {
		for i := range samples {
			samples[i].At = samples[i].At.Add(d)
		}
	}
	a.credits.shift(d)
}

func (b *usageBook) shift(d time.Duration) {
	for i := range b.spend {
		b.spend[i].At = b.spend[i].At.Add(d)
	}
}

func (p *pendingRequests) shift(d time.Duration) {
	for _, waiting := range p.bySession {
		for i := range waiting {
			waiting[i].receivedAt = waiting[i].receivedAt.Add(d)
			waiting[i].request.At = waiting[i].request.At.Add(d)
		}
	}
}

func (d alertDates) shift(by time.Duration) {
	for key, since := range d {
		d[key] = since.Add(by)
	}
}

func shifted(t time.Time, d time.Duration) time.Time {
	if t.IsZero() {
		return t
	}
	return t.Add(d)
}
