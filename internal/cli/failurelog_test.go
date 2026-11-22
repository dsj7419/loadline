package cli

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/clocktest"
)

func TestFailureLog_LogsEachFailureOnceAMinute(t *testing.T) {
	var out bytes.Buffer
	clock := clocktest.New(idleT0)
	failures := newFailureLog(slog.New(slog.NewTextHandler(&out, nil)), clock.Now)
	missing := errors.New("locate claude: not found")
	count := func() int { return strings.Count(out.String(), "\n") }

	failures.note("poll failed", "/a", missing)
	clock.Advance(59 * time.Second)
	failures.note("poll failed", "/a", missing)
	if got := count(); got != 1 {
		t.Fatalf("%d lines for one failure twice within a minute, want 1", got)
	}

	failures.note("poll failed", "/b", missing)
	failures.note("probe failed", "/a", missing)
	failures.note("poll failed", "/a", errors.New("claude: exit status 3"))
	if got := count(); got != 4 {
		t.Fatalf("%d lines, want each new subject, kind and error logged: 4", got)
	}

	clock.Advance(time.Second)
	failures.note("poll failed", "/a", missing)
	if got := count(); got != 5 {
		t.Errorf("%d lines, want the failure logged again a minute after it was: 5", got)
	}
}

// A clock that steps back, as after a correction, must not keep a failure
// quiet until it catches up.
func TestFailureLog_LogsAgainAfterTheClockStepsBack(t *testing.T) {
	var out bytes.Buffer
	clock := clocktest.New(idleT0)
	stepped := idleT0
	now := func() time.Time { return stepped }
	failures := newFailureLog(slog.New(slog.NewTextHandler(&out, nil)), now)
	failed := errors.New("boom")

	failures.note("poll failed", "/a", failed)
	stepped = clock.Now().Add(-time.Hour)
	failures.note("poll failed", "/a", failed)

	if got := strings.Count(out.String(), "\n"); got != 2 {
		t.Errorf("%d lines, want the failure logged again after the clock stepped back", got)
	}
}
