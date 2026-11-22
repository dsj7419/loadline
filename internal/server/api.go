package server

import (
	"net/http"
	"sync"
	"time"

	"github.com/dsj7419/loadline/internal/tracker"
)

const appName = "loadline"

// feed is one of the ways news of sessions reaches the server.
type feed int

const (
	feedHook feed = iota
	feedStatus
	feedOTLP
	feedPoll
	feedCount
)

var feedNames = [feedCount]string{
	feedHook:   "hook",
	feedStatus: "status",
	feedOTLP:   "otlp",
	feedPoll:   "poll",
}

// feedLog is when each feed last delivered something the server accepted.
type feedLog struct {
	mu   sync.Mutex
	last [feedCount]time.Time
}

func (l *feedLog) note(f feed, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last[f] = at
}

func (l *feedLog) times() [feedCount]time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}

// Feeds returns when each feed last delivered something the server accepted,
// keyed hook, status, otlp and poll. A feed not heard from has the zero time.
func (s *Server) Feeds() map[string]time.Time {
	last := s.feeds.times()
	feeds := make(map[string]time.Time, len(last))
	for f, at := range last {
		feeds[feedNames[f]] = at
	}
	return feeds
}

// NotePoll records that a poll of the sessions finished at at.
func (s *Server) NotePoll(at time.Time) { s.feeds.note(feedPoll, at) }

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, struct {
		App     string `json:"app"`
		Version string `json:"version"`
	}{App: appName, Version: s.version})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, s.state.Snapshot())
}

type diagResponse struct {
	App     string    `json:"app"`
	Version string    `json:"version"`
	Clients int       `json:"clients"`
	Feeds   diagFeeds `json:"feeds"`
}

// diagFeeds are the feeds' last receipt times, null for a feed not heard
// from.
type diagFeeds struct {
	Hook   tracker.Timestamp `json:"hook"`
	Status tracker.Timestamp `json:"status"`
	OTLP   tracker.Timestamp `json:"otlp"`
	Poll   tracker.Timestamp `json:"poll"`
}

func (s *Server) handleDiag(w http.ResponseWriter, r *http.Request) {
	last := s.feeds.times()
	s.writeJSON(w, r, http.StatusOK, diagResponse{
		App:     appName,
		Version: s.version,
		Clients: s.Clients(),
		Feeds: diagFeeds{
			Hook:   tracker.Timestamp(last[feedHook]),
			Status: tracker.Timestamp(last[feedStatus]),
			OTLP:   tracker.Timestamp(last[feedOTLP]),
			Poll:   tracker.Timestamp(last[feedPoll]),
		},
	})
}
