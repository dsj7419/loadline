package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/exetest"
)

// A hook or a server that outlived an upgrade still runs the binary it moved
// aside, which Windows will not let anything delete. Removing that binary is
// housekeeping, never a change, so install has nothing to do and nothing to
// ask, and leaves the binary for later without a word.
func TestInstall_SaysAlreadyInstalledWhileAnAsideBinaryStillRuns(t *testing.T) {
	home, dir := sandbox(t, ownerSettings)
	if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
		t.Fatalf("install: %s", got.stderr)
	}
	aside := filepath.FromSlash(installedExe(home)) + ".old"
	exetest.Run(t, aside)

	got := run(t, "install", "--config-dir", dir)

	if want := "Already installed.\n"; got.code != 0 || got.stdout != want || got.stderr != "" {
		t.Errorf("install = %d, stdout %q, stderr %q; want 0 and %q", got.code, got.stdout, got.stderr, want)
	}
	if _, err := os.Lstat(aside); err != nil {
		t.Errorf("install removed %s, which still runs: %v", aside, err)
	}
}
