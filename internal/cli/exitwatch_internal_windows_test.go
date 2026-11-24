package cli

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

// openProcessAnswers makes every OpenProcess answer err, for the rest of the
// test.
func openProcessAnswers(t *testing.T, err error) {
	t.Helper()
	previous := openProcess
	openProcess = func(uint32, bool, uint32) (windows.Handle, error) { return 0, err }
	t.Cleanup(func() { openProcess = previous })
}

// A process Windows will not let be watched is told apart from a failure,
// keeping the cause.
func TestWatchExit_AProcessThatCannotBeOpenedIsUnwatched(t *testing.T) {
	openProcessAnswers(t, windows.ERROR_ACCESS_DENIED)

	watch, err := watchExit(4242)

	if watch != nil || !errors.Is(err, errCannotWatch) || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Errorf("watchExit = %v, %v; want errCannotWatch, keeping the cause", watch, err)
	}
}

// A process Windows knows nothing of has exited.
func TestWatchExit_AProcessWindowsDoesNotKnowHasExited(t *testing.T) {
	openProcessAnswers(t, windows.ERROR_INVALID_PARAMETER)

	watch, err := watchExit(4242)
	if err != nil {
		t.Fatalf("watchExit = %v", err)
	}
	defer watch.close()
	if exited, err := watch.exited(); !exited || err != nil {
		t.Errorf("exited = %v, %v; want true", exited, err)
	}
}
