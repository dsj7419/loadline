package server

import "time"

// SetPingInterval sets how often an event stream sends its heartbeat
// comment. Call it before the server handles a request.
func SetPingInterval(s *Server, d time.Duration) { s.pingEvery = d }
