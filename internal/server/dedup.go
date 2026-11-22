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
// a ring.
type recentIDs struct {
	mu    sync.Mutex
	ring  []seenID
	start int // index of the oldest entry
	size  int
	known map[string]struct{}
}

type seenID struct {
	id string
	at time.Time
}

func newRecentIDs() *recentIDs {
	return &recentIDs{
		ring:  make([]seenID, recentIDLimit),
		known: make(map[string]struct{}, recentIDLimit),
	}
}

// add remembers id as applied at now, reporting false when it was applied
// recently and must not be applied again. A repeat leaves the first
// sighting's time as it was.
func (r *recentIDs) add(id string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (r *recentIDs) dropOldest() {
	delete(r.known, r.ring[r.start].id)
	r.ring[r.start] = seenID{}
	r.start = (r.start + 1) % len(r.ring)
	r.size--
}
