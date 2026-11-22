package server

import (
	"sync"
	"time"
)

const (
	// recentIDLimit and recentIDAge bound the event IDs kept to recognize a
	// repeat: the most recent ones, none older than the age. A client
	// resends a spooled event within seconds of the server coming back.
	recentIDLimit = 4096
	recentIDAge   = 15 * time.Minute
)

// recentIDs remembers the IDs of the events applied lately, oldest first, in
// a ring. Each ID it knows is held by exactly one entry of the ring.
type recentIDs struct {
	clock func() time.Time

	mu    sync.Mutex
	ring  []seenID
	start int // index of the oldest entry
	size  int
	known map[string]struct{}
}

// seenID is an entry of the ring. A forgotten entry keeps its place, and
// its time, with an empty id.
type seenID struct {
	id string
	at time.Time
}

func newRecentIDs(clock func() time.Time) *recentIDs {
	return &recentIDs{
		clock: clock,
		ring:  make([]seenID, recentIDLimit),
		known: make(map[string]struct{}, recentIDLimit),
	}
}

// add remembers id as applied now, reporting false when it was applied
// recently and must not be applied again. A repeat leaves the first
// sighting's time as it was.
func (r *recentIDs) add(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	for r.size > 0 && now.Sub(r.ring[r.start].at) >= recentIDAge {
		r.dropOldest()
	}
	if _, seen := r.known[id]; seen {
		return false
	}
	if r.size == len(r.ring) {
		r.dropOldest()
	}
	r.ring[(r.start+r.size)%len(r.ring)] = seenID{id: id, at: now}
	r.size++
	r.known[id] = struct{}{}
	return true
}

// forget undoes add for an event that was not applied after all, so that
// the event is applied when it comes again. Its entry is blanked rather than
// removed, so that the entry leaving the ring later does not take a newer
// entry's ID with it.
func (r *recentIDs) forget(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.known, id)
	for i := r.size - 1; i >= 0; i-- {
		entry := &r.ring[(r.start+i)%len(r.ring)]
		if entry.id == id {
			entry.id = ""
			return
		}
	}
}

func (r *recentIDs) dropOldest() {
	if id := r.ring[r.start].id; id != "" {
		delete(r.known, id)
	}
	r.ring[r.start] = seenID{}
	r.start = (r.start + 1) % len(r.ring)
	r.size--
}
