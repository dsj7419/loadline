package launch_test

import (
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/launch"
)

// wantServerHidden: the server is told to start hidden, so that not even a
// console window flashes.
const wantServerHidden = true

var getConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// inspect reports whether this process has no console at all, whether it
// was told to start hidden, and when it was created.
func inspect(r *helperReport) {
	// The list of the processes attached to this process's console is empty
	// only when it has none: a console without a window still lists it.
	var pid uint32
	attached, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&pid)), 1) //nolint:gosec // G103: the call's documented one-element buffer
	r.Detached = attached == 0
	var startup windows.StartupInfo
	if err := windows.GetStartupInfo(&startup); err == nil {
		r.Hidden = startup.Flags&windows.STARTF_USESHOWWINDOW != 0 && startup.ShowWindow == windows.SW_HIDE
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err == nil {
		r.Created = created.Nanoseconds()
	}
}

// A process object lives as long as any handle to it is open, and its ID
// stays taken until then. So once the program has exited, its ID names no
// process unless something still holds it.
func TestSpawnDetached_KeepsNoHandleToTheProgram(t *testing.T) {
	c := expectChild(t, false)
	if err := launch.SpawnDetached(testBinary(t)); err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}
	report := c.read(t)
	c.waitExit(t)

	// Other programs, such as a virus scanner, may hold the process a while.
	deadline := time.Now().Add(10 * time.Second)
	for processHeld(t, report) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is held open 10 s after it exited, want no handle to it kept", report.PID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// processHeld reports whether the process the report came from still
// exists, as a process that has not finished exiting or one that a handle
// keeps. A process created at another time has been given its ID since.
func processHeld(t *testing.T, report helperReport) bool {
	t.Helper()
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(report.PID)) //nolint:gosec // G115: a process ID is far below 2^32
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		t.Fatalf("read the times of process %d: %v", report.PID, err)
	}
	return created.Nanoseconds() == report.Created
}
