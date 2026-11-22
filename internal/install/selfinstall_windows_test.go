package install_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// runningCopy writes the test binary to path and runs it until the test
// ends, as a hook or the server runs the installed loadline. It returns the
// binary's bytes.
func runningCopy(t *testing.T, path string) []byte {
	t.Helper()

	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary := readFile(t, testBinary)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, binary)

	process := exec.CommandContext(context.Background(), path, "-test.run=^$")
	process.Env = append(os.Environ(), helperEnv+"=sleep")
	if err := process.Start(); err != nil {
		t.Fatalf("start %s: %v", path, err)
	}
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
	})
	if err := os.Rename(writeBinary(t, "probe"), path); err == nil {
		t.Skip("this system lets a running executable be replaced, so there is nothing to rename aside")
	}
	return binary
}

// Windows refuses to overwrite an executable that is running, which is the
// state an upgrade finds: hooks and the server run the installed copy.
func TestSelfInstall_RenamesARunningTargetAside(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "bin", "loadline.exe")
	old := target + ".old"
	running := runningCopy(t, target)
	writeFile(t, old, []byte("a previous upgrade's leftover"))

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

// An upgrade can find the previous binary still running as .old too, when a
// long-lived server outlived the last upgrade.
func TestSelfInstall_FindsAnotherNameWhenTheOldBinaryRunsToo(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "bin", "loadline.exe")
	runningCopy(t, target+".old")
	running := runningCopy(t, target)
	writeFile(t, target+".old-1", []byte("taken"))

	got := selfInstall(t, writeBinary(t, "loadline v3"), dataDir)

	if got != filepath.ToSlash(target) {
		t.Errorf("SelfInstall = %s, want %s", got, filepath.ToSlash(target))
	}
	if content := readFile(t, target); string(content) != "loadline v3" {
		t.Errorf("target holds %d bytes, want the new binary", len(content))
	}
	if aside := readFile(t, target+".old-2"); !bytes.Equal(aside, running) {
		t.Errorf("%s.old-2 holds %d bytes, want the running binary", target, len(aside))
	}
	if taken := readFile(t, target+".old-1"); string(taken) != "taken" {
		t.Error("SelfInstall replaced a name that was already taken")
	}
}
