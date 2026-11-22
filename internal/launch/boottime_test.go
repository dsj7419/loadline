package launch_test

import (
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/launch"
)

func TestBootTime_IsAStableMomentInThePast(t *testing.T) {
	first, err := launch.BootTime()
	if err != nil {
		t.Fatalf("BootTime: %v", err)
	}
	second, err := launch.BootTime()
	if err != nil {
		t.Fatalf("BootTime: %v", err)
	}

	if now := time.Now(); !first.Before(now) || first.Before(time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("BootTime = %v, want a moment before now (%v)", first, now)
	}
	// Derived from the uptime on some systems, it may move by a little.
	if drift := second.Sub(first).Abs(); drift > time.Second {
		t.Errorf("BootTime moved by %v between two calls, want the same moment", drift)
	}
}
