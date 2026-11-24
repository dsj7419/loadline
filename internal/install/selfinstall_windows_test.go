package install_test

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/exetest"
	"github.com/dsj7419/loadline/internal/install"
)

// waitedIn runs f in a synctest bubble and returns how far the bubble's
// clock moved meanwhile. That clock moves only while f waits on time, as a
// pause between renames does, so the result is exactly the time f waited,
// however slow the machine. Only waits on Go's own clock count: one that
// blocks in a system call, such as a sleep the system does for it, reads as
// no time at all.
func waitedIn(t *testing.T, f func()) time.Duration {
	t.Helper()
	var waited time.Duration
	synctest.Test(t, func(*testing.T) {
		start := time.Now()
		f()
		waited = time.Since(start)
	})
	return waited
}

// runningCopy writes the test binary to path and runs it until the test
// ends, as a hook or the server runs the installed loadline. It returns the
// binary's bytes.
func runningCopy(t *testing.T, path string) []byte {
	t.Helper()

	binary := exetest.Run(t, path)
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
	const budget = 50 * time.Millisecond
	install.SetRenameBudget(t, budget)
	src := writeBinary(t, "loadline v2")

	var err error
	waited := waitedIn(t, func() { _, err = install.SelfInstall(src, dataDir) })

	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Errorf("err = %v, want the sharing violation", err)
	}
	// It spends its budget waiting, and gives up rather than pause past it.
	if waited <= budget/2 || waited > budget {
		t.Errorf("SelfInstall waited %v before it gave up, want most of its %v budget and no more", waited, budget)
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

// refusals scripts how renames into each destination fail. A destination in
// running holds a running binary while a file is there, so replacing it is
// refused with ERROR_ACCESS_DENIED for good; a destination in held refuses
// its next renames with the listed errors, one per attempt, as a scan of
// the file does for a moment. Every attempt is counted per destination, and
// the rest are renamed for real.
type refusals struct {
	running  map[string]bool
	held     map[string][]syscall.Errno
	attempts map[string]int
}

func (r *refusals) rename(from, to string) error {
	if r.attempts == nil {
		r.attempts = map[string]int{}
	}
	r.attempts[to]++
	if _, err := os.Lstat(to); err == nil && r.running[to] {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: windows.ERROR_ACCESS_DENIED}
	}
	if errnos := r.held[to]; len(errnos) > 0 {
		r.held[to] = errnos[1:]
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: errnos[0]}
	}
	return os.Rename(from, to)
}

// A first install renames the new copy to a name that does not exist yet,
// so any refusal is a brief hold, such as an antivirus scan of the new copy.
func TestSelfInstall_FirstInstallWaitsOutABriefHoldOnTheNewCopy(t *testing.T) {
	for name, errnos := range map[string][]syscall.Errno{
		"sharing violation": {windows.ERROR_SHARING_VIOLATION},
		"lock violation":    {windows.ERROR_LOCK_VIOLATION, windows.ERROR_LOCK_VIOLATION},
		"access denied":     {windows.ERROR_ACCESS_DENIED, windows.ERROR_ACCESS_DENIED, windows.ERROR_ACCESS_DENIED},
	} {
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			target := filepath.Join(dataDir, "bin", "loadline.exe")
			scripted := &refusals{held: map[string][]syscall.Errno{target: errnos}}
			install.SetRename(t, scripted.rename)

			if _, err := install.SelfInstall(writeBinary(t, "loadline v1"), dataDir); err != nil {
				t.Fatalf("SelfInstall gave up after %d rename(s) on a brief hold: %v", scripted.attempts[target], err)
			}
			if content := readFile(t, target); string(content) != "loadline v1" {
				t.Errorf("target holds %q, want the new binary", content)
			}
		})
	}
}

// Renames to a name that does not exist yet wait out access denied too:
// nothing at the destination can be refusing, so it is a hold on the file.
func TestSelfInstall_WaitsOutAccessDeniedOnAFreshName(t *testing.T) {
	denied := []syscall.Errno{windows.ERROR_ACCESS_DENIED, windows.ERROR_ACCESS_DENIED, windows.ERROR_ACCESS_DENIED}
	tests := map[string]struct {
		oldRunning bool
		heldAt     string // the destination that is held
		wantAside  string
	}{
		"moving the running binary to a fresh .old-1": {oldRunning: true, heldAt: ".old-1", wantAside: ".old-1"},
		"moving the new binary into the emptied name": {heldAt: "", wantAside: ".old"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			selfInstall(t, writeBinary(t, "loadline v1"), dataDir)
			target := filepath.Join(dataDir, "bin", "loadline.exe")
			scripted := &refusals{
				running: map[string]bool{target: true},
				held:    map[string][]syscall.Errno{target + tt.heldAt: slices.Clone(denied)},
			}
			if tt.oldRunning {
				writeFile(t, target+".old", []byte("an older binary that still runs"))
				scripted.running[target+".old"] = true
			}
			install.SetRename(t, scripted.rename)

			if _, err := install.SelfInstall(writeBinary(t, "loadline v2"), dataDir); err != nil {
				t.Fatalf("SelfInstall gave up on access denied at a fresh name: %v", err)
			}
			if content := readFile(t, target); string(content) != "loadline v2" {
				t.Errorf("target holds %q, want the new binary", content)
			}
			if aside := readFile(t, target+tt.wantAside); string(aside) != "loadline v1" {
				t.Errorf("%s holds %q, want the previous binary", tt.wantAside, aside)
			}
			if left := scripted.held[target+tt.heldAt]; len(left) != 0 {
				t.Errorf("%d refusals were never tried", len(left))
			}
		})
	}
}

// The running binary can move aside to any of .old-1 to .old-100, so the
// hundredth is used when it alone is free, and an upgrade that finds all of
// them taken says so and leaves the previous binary in place.
func TestSelfInstall_TriesAHundredNamesForTheRunningBinary(t *testing.T) {
	for name, taken := range map[string]int{"the hundredth free": 99, "all taken": 100} {
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			selfInstall(t, writeBinary(t, "loadline v1"), dataDir)
			target := filepath.Join(dataDir, "bin", "loadline.exe")
			writeFile(t, target+".old", []byte("an older binary that still runs"))
			for n := 1; n <= taken; n++ {
				writeFile(t, fmt.Sprintf("%s.old-%d", target, n), []byte("taken"))
			}
			install.SetRename(t, (&refusals{running: map[string]bool{target: true, target + ".old": true}}).rename)

			_, err := install.SelfInstall(writeBinary(t, "loadline v2"), dataDir)

			if taken == 100 {
				if err == nil || !strings.Contains(err.Error(), ".old-1 to -100 are taken") {
					t.Errorf("err = %v, want it to say .old-1 to -100 are taken", err)
				}
				if content := readFile(t, target); string(content) != "loadline v1" {
					t.Errorf("target holds %q, want the previous binary left in place", content)
				}
				return
			}
			if err != nil {
				t.Fatalf("SelfInstall: %v", err)
			}
			if aside := readFile(t, target+".old-100"); string(aside) != "loadline v1" {
				t.Errorf(".old-100 holds %q, want the previous binary", aside)
			}
			if content := readFile(t, target); string(content) != "loadline v2" {
				t.Errorf("target holds %q, want the new binary", content)
			}
		})
	}
}

// The pause between attempts doubles, so a hold that outlasts the budget
// costs a handful of renames rather than a busy loop.
func TestSelfInstall_PausesLongerBeforeEachAttempt(t *testing.T) {
	dataDir := t.TempDir()
	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)
	target := filepath.Join(dataDir, "bin", "loadline.exe")
	attempts := 0
	install.SetRename(t, func(from, to string) error {
		switch {
		case from == target:
			attempts++
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: windows.ERROR_SHARING_VIOLATION}
		case to == target:
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: windows.ERROR_ACCESS_DENIED}
		}
		return os.Rename(from, to)
	})
	install.SetRenameBudget(t, 50*time.Millisecond)

	if _, err := install.SelfInstall(writeBinary(t, "loadline v2"), dataDir); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("err = %v, want the sharing violation", err)
	}

	// Pauses of 1, 2, 4, 8 and 16 ms let .old be tried six times within the
	// 50 ms, and .old-1 at most five more in what is left of it. The bound
	// leaves room for a coarse clock, which can fit in a few more; without the
	// pauses the same 50 ms hold over a million attempts.
	if attempts > 20 {
		t.Errorf("tried to move the held binary %d times in 50ms, want at most 20", attempts)
	}
}

// A running .old refuses to be replaced for as long as it runs, so waiting
// on it would only spend the budget; the next free name is tried at once.
func TestSelfInstall_MovesPastARunningOldAtOnce(t *testing.T) {
	dataDir := t.TempDir()
	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)
	target := filepath.Join(dataDir, "bin", "loadline.exe")
	writeFile(t, target+".old", []byte("an older binary that still runs"))
	scripted := &refusals{running: map[string]bool{target: true, target + ".old": true}}
	install.SetRename(t, scripted.rename)
	install.SetRenameBudget(t, 2*time.Second)
	src := writeBinary(t, "loadline v2")

	var err error
	waited := waitedIn(t, func() { _, err = install.SelfInstall(src, dataDir) })

	if err != nil {
		t.Fatalf("SelfInstall: %v", err)
	}
	if waited != 0 {
		t.Errorf("SelfInstall waited %v, want it to move past the running .old at once", waited)
	}
	if n := scripted.attempts[target+".old"]; n != 1 {
		t.Errorf("tried .old %d times, want once", n)
	}
	if aside := readFile(t, target+".old-1"); string(aside) != "loadline v1" {
		t.Errorf(".old-1 holds %q, want the previous binary", aside)
	}
}

func TestSelfInstall_SaysWhereThePreviousBinaryIsWhenPuttingItBackFails(t *testing.T) {
	dataDir := t.TempDir()
	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)
	target := filepath.Join(dataDir, "bin", "loadline.exe")
	aside := target + ".old"
	install.SetRename(t, func(from, to string) error {
		switch {
		case from == target:
			return os.Rename(from, to)
		case to == target && strings.HasSuffix(from, ".tmp"):
			if _, err := os.Lstat(target); err == nil {
				return &os.LinkError{Op: "rename", Old: from, New: to, Err: windows.ERROR_ACCESS_DENIED}
			}
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: windows.ERROR_DISK_FULL}
		default:
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: windows.ERROR_INVALID_PARAMETER}
		}
	})

	_, err := install.SelfInstall(writeBinary(t, "loadline v2"), dataDir)

	if !errors.Is(err, windows.ERROR_DISK_FULL) || !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		t.Errorf("err = %v, want both the failed install and the failed rollback", err)
	}
	if err == nil || !strings.Contains(err.Error(), aside) {
		t.Errorf("err = %v, want it to say the previous binary is at %s", err, aside)
	}
	if content := readFile(t, aside); string(content) != "loadline v1" {
		t.Errorf("%s holds %q, want the previous binary", aside, content)
	}
}

// A long-lived server can outlive the upgrade that moved its binary aside.
// Windows refuses to delete a binary while it runs, so RemoveAside leaves it
// for a later run, and says nothing of it.
func TestRemoveAside_KeepsABinaryThatIsRunning(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "bin", "loadline.exe")
	running := runningCopy(t, target+".old")
	writeFile(t, target+".old-1", []byte("a binary nothing runs"))

	removed, err := install.RemoveAside(dataDir)

	if err != nil || !slices.Equal(removed, []string{target + ".old-1"}) {
		t.Errorf("RemoveAside = %q, %v; want only %s.old-1 and no error for the binary that runs", removed, err, target)
	}
	if aside := readFile(t, target+".old"); !bytes.Equal(aside, running) {
		t.Errorf("%s.old holds %d bytes, want the running binary kept", target, len(aside))
	}
}

// An antivirus scan or a starting process holds a binary for a moment, as
// a running one holds it for good; RemoveAside leaves it for a later run
// all the same.
func TestRemoveAside_LeavesAHeldBinaryForLater(t *testing.T) {
	for name, errno := range map[string]syscall.Errno{
		"access denied":     windows.ERROR_ACCESS_DENIED,
		"sharing violation": windows.ERROR_SHARING_VIOLATION,
		"lock violation":    windows.ERROR_LOCK_VIOLATION,
	} {
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			target := filepath.Join(dataDir, "bin", "loadline.exe")
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, target+".old", []byte("a held binary"))
			install.SetRemove(t, func(path string) error {
				return &fs.PathError{Op: "remove", Path: path, Err: errno}
			})

			removed, err := install.RemoveAside(dataDir)

			if err != nil || len(removed) != 0 {
				t.Errorf("RemoveAside = %q, %v; want nothing removed and no error", removed, err)
			}
		})
	}
}
