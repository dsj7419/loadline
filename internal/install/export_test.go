package install

import (
	"testing"
	"time"
)

// DetectShellOn exposes the shell detection for any operating system to the
// external tests, which run on just one.
var DetectShellOn = detectShell

// SetRename replaces the rename SelfInstall uses for the rest of the test,
// so a test can make files look held by another process.
func SetRename(t *testing.T, f func(from, to string) error) {
	t.Helper()

	previous := rename
	rename = f
	t.Cleanup(func() { rename = previous })
}

// SetRenameBudget changes how long SelfInstall waits out a held file for
// the rest of the test.
func SetRenameBudget(t *testing.T, budget time.Duration) {
	t.Helper()

	previous := renameBudget
	renameBudget = budget
	t.Cleanup(func() { renameBudget = previous })
}
