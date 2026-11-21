package tracker

import (
	"sync"
	"time"
)

const (
	// publishInterval spaces published views, so subscribers see at most
	// four a second however fast events arrive.
	publishInterval = 250 * time.Millisecond
	// needsYouRecheck is how often the alerts are evaluated again while a
	// session waits on the user, so a needs-you alert appears on time
	// without another event.
	needsYouRecheck = time.Second
)

// Ticker runs a function once after a delay.
type Ticker interface {
	// AfterFunc arranges for f to run once d has passed, and returns a
	// function that cancels it, reporting whether it stopped f from running.
	// f must not run within AfterFunc itself.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

// wallTicker is a Ticker on wall-clock timers.
type wallTicker struct{}

func (wallTicker) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// publisher is the tracker's subscribers and the timers that feed them. The
// tracker's lock guards it.
type publisher struct {
	subscribers map[chan View]struct{}
	lastAt      time.Time
	// flushing and rechecking say whether a coalesced publish and a needs-you
	// recheck are scheduled; stopFlush and stopRecheck cancel them.
	flushing, rechecking   bool
	stopFlush, stopRecheck func() bool
	closed                 bool
}

// Subscribe returns a channel that receives each view published after the
// call, and a function that ends the subscription and closes the channel.
// The channel holds one view: a subscriber that falls behind finds only the
// latest, and never holds up the tracker. To miss nothing, subscribe first
// and then take a Snapshot. After Close, the channel is closed at once.
func (t *Tracker) Subscribe() (<-chan View, func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ch := make(chan View, 1)
	if t.pub.closed {
		close(ch)
		return ch, func() {}
	}
	t.pub.subscribers[ch] = struct{}{}

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if _, ok := t.pub.subscribers[ch]; ok {
				delete(t.pub.subscribers, ch)
				close(ch)
			}
		})
	}
}

// Close stops the tracker's timers and ends every subscription. The tracker
// still applies events afterwards, but publishes nothing.
func (t *Tracker) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pub.closed {
		return
	}
	t.pub.closed = true
	if t.pub.stopFlush != nil {
		t.pub.stopFlush()
	}
	if t.pub.stopRecheck != nil {
		t.pub.stopRecheck()
	}
	for ch := range t.pub.subscribers {
		close(ch)
	}
	clear(t.pub.subscribers)
}

// changed notes that the state changed. The first change after a quiet
// interval goes out at once; later ones wait for the interval to end and go
// out together. The wait never exceeds the interval, even when the clock
// steps back.
func (t *Tracker) changed() {
	if t.pub.closed || t.pub.flushing {
		return
	}
	wait := min(publishInterval, t.pub.lastAt.Add(publishInterval).Sub(t.clock()))
	if wait <= 0 {
		t.publish()
		return
	}
	t.pub.flushing = true
	t.pub.stopFlush = t.ticker.AfterFunc(wait, t.flush)
}

func (t *Tracker) flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pub.closed || !t.pub.flushing {
		return
	}
	t.pub.flushing, t.pub.stopFlush = false, nil
	t.publish()
}

// publish builds the view and hands it to every subscriber, replacing any
// view a subscriber has not taken yet.
func (t *Tracker) publish() {
	now := t.clock()
	v := t.buildView(now)
	t.pub.lastAt = now
	for ch := range t.pub.subscribers {
		select {
		case <-ch:
		default:
		}
		// Only the tracker sends, and it holds the lock, so the slot just
		// emptied is still free.
		ch <- v
	}
	t.scheduleRecheck()
}

func (t *Tracker) scheduleRecheck() {
	if t.pub.rechecking || !t.anyNeedsYou() {
		return
	}
	t.pub.rechecking = true
	t.pub.stopRecheck = t.ticker.AfterFunc(needsYouRecheck, t.recheck)
}

func (t *Tracker) recheck() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pub.closed || !t.pub.rechecking {
		return
	}
	t.pub.rechecking, t.pub.stopRecheck = false, nil
	t.changed()
}
