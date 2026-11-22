package install_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/install"
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

	ready := filepath.Join(t.TempDir(), "ready")
	process := exec.CommandContext(context.Background(), path, "-test.run=^$")
	process.Env = append(os.Environ(), helperEnv+"=sleep", helperReadyEnv+"="+ready)
	if err := process.Start(); err != nil {
		t.Fatalf("start %s: %v", path, err)
	}
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
	})
	waitForFile(t, ready)
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

// waitForFile waits until the file at path exists.
func waitForFile(t *testing.T, path string) {
	t.Helper()

	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("the helper never created %s", path)
}

// heldFiles simulates a running binary at target that another process also
// holds open for a moment, as an antivirus scan or a starting process does.
// Replacing it fails with ERROR_ACCESS_DENIED until it has been renamed
// aside, and renaming it fails with errno for the first holds attempts, or
// for all of them when holds is negative. Everything else is renamed for
// real.
type heldFiles struct {
	target string
	errno  syscall.Errno
	holds  int
	moved  bool
	calls  []string
}

func (h *heldFiles) rename(from, to string) error {
	h.calls = append(h.calls, filepath.Base(from)+" -> "+filepath.Base(to))
	switch {
	case to == h.target && !h.moved:
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: windows.ERROR_ACCESS_DENIED}
	case from == h.target && h.holds != 0:
		h.holds--
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: h.errno}
	}
	err := os.Rename(from, to)
	h.moved = h.moved || err == nil && from == h.target
	return err
}

func TestSelfInstall_WaitsOutABriefHoldOnTheRunningBinary(t *testing.T) {
	for name, errno := range map[string]syscall.Errno{
		"sharing violation": windows.ERROR_SHARING_VIOLATION,
		"lock violation":    windows.ERROR_LOCK_VIOLATION,
	} {
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			selfInstall(t, writeBinary(t, "loadline v1"), dataDir)
			target := filepath.Join(dataDir, "bin", "loadline.exe")
			held := &heldFiles{target: target, errno: errno, holds: 3}
			install.SetRename(t, held.rename)

			got := selfInstall(t, writeBinary(t, "loadline v2"), dataDir)

			if got != filepath.ToSlash(target) {
				t.Errorf("SelfInstall = %s, want %s", got, filepath.ToSlash(target))
			}
			if content := readFile(t, target); string(content) != "loadline v2" {
				t.Errorf("target holds %q, want the new binary", content)
			}
			if aside := readFile(t, target+".old"); string(aside) != "loadline v1" {
				t.Errorf(".old holds %q, want the previous binary", aside)
			}
			if held.holds != 0 {
				t.Errorf("SelfInstall gave up with %d holds left; calls %q", held.holds, held.calls)
			}
		})
	}
}

func TestSelfInstall_GivesUpOnAHoldThatOutlastsTheBudget(t *testing.T) {
	dataDir := t.TempDir()
	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)
	target := filepath.Join(dataDir, "bin", "loadline.exe")
	install.SetRename(t, (&heldFiles{
		target: target, errno: windows.ERROR_SHARING_VIOLATION, holds: -1,
	}).rename)
	install.SetRenameBudget(t, 50*time.Millisecond)

	start := time.Now()
	_, err := install.SelfInstall(writeBinary(t, "loadline v2"), dataDir)

	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Errorf("err = %v, want the sharing violation", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("SelfInstall took %s to give up, want about the 50ms budget", elapsed)
	}
	if content := readFile(t, target); string(content) != "loadline v1" {
		t.Errorf("target holds %q, want the previous binary untouched", content)
	}
	if entries := dirEntries(t, filepath.Dir(target)); !slices.Equal(entries, []string{"loadline.exe"}) {
		t.Errorf("bin dir holds %q, want only the previous binary", entries)
	}
}

func TestSelfInstall_DoesNotWaitOnOtherErrors(t *testing.T) {
	dataDir := t.TempDir()
	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)
	target := filepath.Join(dataDir, "bin", "loadline.exe")
	held := &heldFiles{target: target, errno: windows.ERROR_INVALID_PARAMETER, holds: -1}
	install.SetRename(t, held.rename)

	_, err := install.SelfInstall(writeBinary(t, "loadline v2"), dataDir)

	if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		t.Errorf("err = %v, want the invalid-parameter error", err)
	}
	if len(held.calls) != 3 {
		t.Errorf("renames %q, want one each: over the target, to .old and to .old-1", held.calls)
	}
}

func TestSelfInstall_PutsThePreviousBinaryBackWhenTheNewOneCannotMoveIn(t *testing.T) {
	dataDir := t.TempDir()
	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)
	target := filepath.Join(dataDir, "bin", "loadline.exe")
	moved := false
	install.SetRename(t, func(from, to string) error {
		if to == target && strings.HasSuffix(from, ".tmp") {
			// Over the running binary first, then into its emptied place.
			if moved {
				return &os.LinkError{Op: "rename", Old: from, New: to, Err: windows.ERROR_DISK_FULL}
			}
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: windows.ERROR_ACCESS_DENIED}
		}
		moved = moved || from == target
		return os.Rename(from, to)
	})

	if _, err := install.SelfInstall(writeBinary(t, "loadline v2"), dataDir); !errors.Is(err, windows.ERROR_DISK_FULL) {
		t.Fatalf("err = %v, want the disk-full error", err)
	}

	if content := readFile(t, target); string(content) != "loadline v1" {
		t.Errorf("target holds %q, want the previous binary back in place", content)
	}
	if entries := dirEntries(t, filepath.Dir(target)); !slices.Equal(entries, []string{"loadline.exe"}) {
		t.Errorf("bin dir holds %q, want only the previous binary", entries)
	}
}
