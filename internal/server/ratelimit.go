package server

import (
	"net/http"
	"sync"
	"time"
)

const (
	// ingestBurst is how many ingest requests may arrive at once, and
	// ingestEvery how long the limit takes to earn one back: 500 a second.
	ingestBurst = 1000
	ingestEvery = time.Second / 500
)

// rateLimit refuses a request when the ingest routes have used up their
// shared allowance.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.take() {
			w.Header().Set("Retry-After", "1")
			s.reject(w, r, http.StatusTooManyRequests, "rate limited")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenBucket holds up to ingestBurst tokens and earns one every ingestEvery.
// It counts whole tokens and whole intervals, so no rounding lets a request
// through early. It reads the clock under its lock: requests racing for the
// lock then see the time in the order they are served, and none of them looks
// like the clock stepping back.
type tokenBucket struct {
	clock func() time.Time

	mu     sync.Mutex
	tokens int64
	// earnedAt is when the bucket last earned its tokens. Time short of a
	// whole interval since then is carried over.
	earnedAt time.Time
}

func newTokenBucket(clock func() time.Time) *tokenBucket {
	return &tokenBucket{clock: clock, tokens: ingestBurst, earnedAt: clock()}
}

// take spends a token, reporting false when there is none.
func (b *tokenBucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	switch elapsed := now.Sub(b.earnedAt); {
	case elapsed < 0:
		// The clock stepped back. Count from now instead of waiting for it
		// to catch up, which would stop ingest for as long as it stepped.
		b.earnedAt = now
	case elapsed >= ingestEvery:
		earned := int64(elapsed / ingestEvery)
		if b.tokens+earned >= ingestBurst {
			b.tokens, b.earnedAt = ingestBurst, now
		} else {
			b.tokens += earned
			b.earnedAt = b.earnedAt.Add(time.Duration(earned) * ingestEvery)
		}
	}
	if b.tokens == 0 {
		return false
	}
	b.tokens--
	return true
}
