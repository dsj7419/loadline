package server

import (
	"sync"
	"time"
)

// SetPingInterval sets how often an event stream sends its heartbeat
// comment. Call it before the server handles a request.
func SetPingInterval(s *Server, d time.Duration) { s.pingEvery = d }

// SetStreamWriteTimeout sets how long one write to an event stream may take.
// Call it before the server handles a request.
func SetStreamWriteTimeout(s *Server, d time.Duration) { s.streamWriteTimeout = d }

// ResetNoDeadlineWarning lets the next stream without write deadlines warn
// again. Call it only while no stream is being served.
func ResetNoDeadlineWarning() { noDeadlineWarning = sync.Once{} }
