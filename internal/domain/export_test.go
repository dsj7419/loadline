package domain

import "time"

// ObservedAt exposes the unexported staleness reference to the external
// tests.
func ObservedAt(s Session) time.Time { return s.observedAt }
