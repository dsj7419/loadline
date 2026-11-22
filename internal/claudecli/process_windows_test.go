package claudecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// helperExeEnv names the executable that a shim runs: the test binary.
const helperExeEnv = "LOADLINE_CLAUDECLI_EXE"

// processAlive reports whether the process with the given ID is running.
func processAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid)) //nolint:gosec // G115: a process ID is far below 2^32
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	event, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && event == uint32(windows.WAIT_TIMEOUT)
}

// TestExec_RunKillsWhatACmdShimStarted is the case of an npm install: claude
// is a .cmd file, cmd.exe runs it, and the real program is cmd.exe's child.
func TestExec_RunKillsWhatACmdShimStarted(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "claude.cmd")
	script := "@echo off\r\n\"%" + helperExeEnv + "%\"\r\n"
	if err := os.WriteFile(shim, []byte(script), 0o700); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	pidFile := filepath.Join(dir, "descendant.pid")
	t.Setenv(helperExeEnv, os.Args[0])
	t.Setenv(helperPIDEnv, pidFile)
	t.Setenv(helperModeEnv, "sleeper")

	err := runUntilDescendantStarts(t, Exec{Path: shim, DefaultDir: defaultDir}, pidFile)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	awaitGone(t, descendantPID(t, pidFile))
}

// isSharingViolation reports whether err is Windows refusing to open a file
// that another handle holds in a way that excludes the open.
func isSharingViolation(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

// holdAsRenaming opens path with delete access, sharing everything, as a
// rename that has just moved the file into place still holds it. Go opens
// files to read without sharing delete access, so until the handle closes,
// reading the file fails with a sharing violation. The test's end closes it.
func holdAsRenaming(t *testing.T, path string) (release func()) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatalf("hold %s: %v", path, err)
	}
	var once sync.Once
	release = func() { once.Do(func() { _ = windows.CloseHandle(handle) }) }
	t.Cleanup(release)
	return release
}

func TestReadPublished_WaitsOutTheRenameThatPublishedTheFile(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	if err := writePIDFile(pidFile); err != nil {
		t.Fatalf("write the pid file: %v", err)
	}
	release := holdAsRenaming(t, pidFile)
	if _, err := os.ReadFile(pidFile); !isSharingViolation(err) {
		t.Fatalf("reading a held pid file gave %v, want the sharing violation this test is about", err)
	}
	released := time.AfterFunc(100*time.Millisecond, release)
	t.Cleanup(func() { released.Stop() })

	data := readPublished(t, pidFile, "the pid file")

	if want := strconv.Itoa(os.Getpid()); string(data) != want {
		t.Errorf("read %q, want the pid written, %q", data, want)
	}
}
