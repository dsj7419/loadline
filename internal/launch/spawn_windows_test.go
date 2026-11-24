package launch_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
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

func TestChildRead_WaitsOutTheRenameThatPublishedTheReport(t *testing.T) {
	c := &child{report: filepath.Join(t.TempDir(), "report.json")}
	if err := writeReport(c.report, helperReport{PID: 4242}); err != nil {
		t.Fatalf("write the report: %v", err)
	}
	release := holdAsRenaming(t, c.report)
	if _, err := os.ReadFile(c.report); !isSharingViolation(err) {
		t.Fatalf("reading a held report gave %v, want the sharing violation this test is about", err)
	}
	released := time.AfterFunc(100*time.Millisecond, release)
	t.Cleanup(func() { released.Stop() })

	report := c.read(t)

	if report.PID != 4242 {
		t.Errorf("read the report as %+v, want the one written", report)
	}
}

// A browser given by path may be a batch file that starts the real browser
// with flags of the user's own. Windows runs a batch file through cmd.exe,
// which reads its command line again, and Go quotes an argument only when it
// holds a blank: a profile path such as C:\Users\R&D\...\browser would end the
// batch file's arguments at the & and start the rest as a command of its own.
// So a batch file gets the one argument it got before the dashboard had a
// profile of its own, --app=<url>, whole, which holds nothing cmd.exe acts on.
func TestOpen_ABatchFileBrowserGetsTheAppWindowAloneWhateverTheProfilePath(t *testing.T) {
	const url = "http://127.0.0.1:8289/login?code=0123456789abcdef"
	profile := filepath.Join(t.TempDir(), "R&D", "loadline", "browser")
	for _, name := range []string{"browser.cmd", "BROWSER.BAT"} {
		t.Run(name, func(t *testing.T) {
			c := expectChild(t, false)
			// The batch file passes its arguments on to the program that
			// reports them, as one that starts a browser passes them on.
			batch := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(batch, []byte("@\""+testBinary(t)+"\" %*\r\n"), 0o700); err != nil {
				t.Fatal(err)
			}

			if err := c.open(url, launch.Browser{Pref: batch, AppWindow: true, ProfileDir: profile}); err != nil {
				t.Fatalf("Open: %v", err)
			}

			report := c.read(t)
			if want := []string{"--app=" + url}; !slices.Equal(report.Args, want) {
				t.Errorf("the browser got arguments %q, want %q", report.Args, want)
			}
		})
	}
}
