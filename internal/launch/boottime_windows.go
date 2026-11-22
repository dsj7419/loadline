package launch

import (
	"time"

	"golang.org/x/sys/windows"
)

// BootTime returns when the machine started: now less the time since then,
// which counts the time spent asleep too. Being derived, it can move by a
// few milliseconds from one call to the next.
func BootTime() (time.Time, error) {
	return time.Now().Add(-windows.DurationSinceBoot()), nil
}
