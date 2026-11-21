package claudecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
