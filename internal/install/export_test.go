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

// SetRemove replaces the removal RemoveAside uses for the rest of the test,
// so a test can make one fail.
func SetRemove(t *testing.T, f func(path string) error) {
	t.Helper()

	previous := remove
	remove = f
	t.Cleanup(func() { remove = previous })
}

// SetRenameBudget changes how long SelfInstall waits out a held file for
// the rest of the test.
func SetRenameBudget(t *testing.T, budget time.Duration) {
	t.Helper()

	previous := renameBudget
	renameBudget = budget
	t.Cleanup(func() { renameBudget = previous })
}

// PrivateMode exposes the mode Apply gives settings files and backups, whose
// bits the Windows tests cannot observe on disk.
var PrivateMode = privateMode

// SetBeforeRecheck runs f, for the rest of the test, just before Apply reads
// the settings file one last time ahead of replacing it.
func SetBeforeRecheck(t *testing.T, f func()) {
	t.Helper()

	previous := testHookBeforeRecheck
	testHookBeforeRecheck = f
	t.Cleanup(func() { testHookBeforeRecheck = previous })
}

// SetFinalPath replaces, for the rest of the test, how Apply asks the system
// where a settings file is, so a test can make it fail to say.
func SetFinalPath(t *testing.T, f func(path string) (string, error)) {
	t.Helper()

	previous := finalPath
	finalPath = f
	t.Cleanup(func() { finalPath = previous })
}
