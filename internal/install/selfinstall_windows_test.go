package install_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Windows refuses to overwrite an executable that is running, which is the
// state an upgrade finds: hooks and the server run the installed copy.
func TestSelfInstall_RenamesARunningTargetAside(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "bin", "loadline.exe")
	old := target + ".old"
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	running := readFile(t, testBinary)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, target, running)
	writeFile(t, old, []byte("a previous upgrade's leftover"))

	process := exec.CommandContext(context.Background(), target, "-test.run=^$") //nolint:gosec // G204: runs a copy of the test binary as the installed loadline
	process.Env = append(os.Environ(), helperEnv+"=sleep")
	if err := process.Start(); err != nil {
		t.Fatalf("start the installed copy: %v", err)
	}
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
	})
	if err := os.Rename(writeBinary(t, "probe"), target); err == nil {
		t.Skip("this system lets a running executable be replaced, so there is nothing to rename aside")
	}

	got := selfInstall(t, writeBinary(t, "loadline v2"), dataDir)

	if got != filepath.ToSlash(target) {
		t.Errorf("SelfInstall = %s, want %s", got, filepath.ToSlash(target))
	}
	if content := readFile(t, target); string(content) != "loadline v2" {
		t.Errorf("target holds %d bytes, want the new binary", len(content))
	}
	if aside := readFile(t, old); !bytes.Equal(aside, running) {
		t.Errorf("%s holds %d bytes, want the running binary in place of the leftover", old, len(aside))
	}
	if entries := dirEntries(t, filepath.Dir(target)); len(entries) != 2 {
		t.Errorf("bin dir holds %q, want the binary and its .old", entries)
	}
}
