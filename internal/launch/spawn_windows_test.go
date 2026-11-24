package launch_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// batchURL is the URL a batch file given as the browser gets in these tests:
// a login URL of the shape Loadline opens, its code in hex, on dashboardURL's
// port, which browsers refuse to fetch from. Unlike dashboardURL it holds
// nothing cmd.exe acts on. Go quotes an argument that holds a blank, and
// cmd.exe strips the first and the last quote of a command line that holds
// more than two, which leaves the & of dashboardURL bare once the batch
// file's own name is quoted too.
const batchURL = "http://127.0.0.1:1/login?code=0123456789abcdef"

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
// That holds however the config names it: by the file Windows runs, not by
// how the name is written.
func TestOpen_ABatchFileBrowserGetsTheAppWindowAloneWhateverTheProfilePath(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "R&D", "loadline", "browser")
	for _, tc := range []struct {
		name string
		// file is the batch file's name on disk, and pref how the config
		// names it.
		file, pref string
	}{
		{name: "a .cmd file", file: "browser.cmd", pref: "browser.cmd"},
		{name: "a .BAT file", file: "BROWSER.BAT", pref: "BROWSER.BAT"},
		{name: "named without its extension, which PATHEXT adds", file: "browser.cmd", pref: "browser"},
		{name: "named with a blank after it, which Windows drops", file: "browser.bat", pref: "browser.bat "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := expectChild(t, false)
			// The batch file passes its arguments on to the program that
			// reports them, as one that starts a browser passes them on.
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte("@\""+testBinary(t)+"\" %*\r\n"), 0o700); err != nil {
				t.Fatal(err)
			}

			if err := c.open(batchURL, launch.Browser{Pref: filepath.Join(dir, tc.pref), AppWindow: true, ProfileDir: profile}); err != nil {
				t.Fatalf("Open: %v", err)
			}

			report := c.read(t)
			if want := []string{"--app=" + batchURL}; !slices.Equal(report.Args, want) {
				t.Errorf("the browser got arguments %q, want %q", report.Args, want)
			}
		})
	}
}

// Windows reads a program's name as it reads any path, without the dots and
// blanks at its end and without a "." folder after it, so browser.cmd\. runs
// browser.cmd, through cmd.exe. That cuts a profile path such as
// C:\Users\R&D\... at the & and runs the rest as a command of its own, from
// the home directory, even where cmd.exe then cannot find the batch file
// under the name it was given. So a batch file gets no profile however the
// config names it. Each row names one batch file another way, with a profile
// under R&D; a stand-in where the text after the & leads leaves a mark if
// cmd.exe runs it. Some names start nothing at all, and under others cmd.exe
// cannot find the batch file; the rows whose batch file runs say so.
func TestOpen_ABatchFileHoweverItIsNamedGetsNoProfile(t *testing.T) {
	for _, tc := range []struct {
		name string
		// file is the batch file's name on disk: a name longer than 8.3 has
		// a short name too, on a volume that keeps them.
		file string
		// pref names the batch file at path as the config does.
		pref func(t *testing.T, path string) string
		// mustRun says that Windows runs the batch file under that name, so
		// that a change that kept it from running at all fails the row.
		mustRun bool
	}{
		{name: "as it is", mustRun: true, pref: func(_ *testing.T, path string) string { return path }},
		{name: "with a dot after it", pref: func(_ *testing.T, path string) string { return path + "." }},
		{name: `with \. after it`, pref: func(_ *testing.T, path string) string { return path + `\.` }},
		{name: `with /. after it`, pref: func(_ *testing.T, path string) string { return path + `/.` }},
		{name: `with \.\. after it`, pref: func(_ *testing.T, path string) string { return path + `\.\.` }},
		{name: `with \. and a blank after it`, pref: func(_ *testing.T, path string) string { return path + `\. ` }},
		{name: `with a blank and \. after it`, mustRun: true, pref: func(_ *testing.T, path string) string { return path + ` \.` }},
		{name: `with \... after it`, pref: func(_ *testing.T, path string) string { return path + `\...` }},
		{name: `with a folder and .. after it`, pref: func(_ *testing.T, path string) string { return path + `\sub\..` }},
		{name: "with its data stream named", pref: func(_ *testing.T, path string) string { return path + "::$DATA" }},
		{name: "with forward slashes", mustRun: true, pref: func(_ *testing.T, path string) string { return filepath.ToSlash(path) }},
		{name: `through \\?\`, pref: func(_ *testing.T, path string) string { return `\\?\` + path }},
		{name: "by a folder and .. on the way", mustRun: true, pref: func(_ *testing.T, path string) string {
			return filepath.Dir(path) + `\sub\..\` + filepath.Base(path)
		}},
		{name: "by its short name", file: "browser launcher.cmd", mustRun: true, pref: func(t *testing.T, path string) string {
			t.Helper()
			long, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			short := make([]uint16, windows.MAX_PATH)
			n, err := windows.GetShortPathName(long, &short[0], windows.MAX_PATH)
			if err != nil || n == 0 {
				t.Skipf("no short name for %s: %v", path, err)
			}
			name := windows.UTF16ToString(short[:n])
			if strings.EqualFold(name, path) {
				t.Skip("this volume keeps no short names")
			}
			return name
		}},
		{name: "by a hard link named .exe", pref: func(t *testing.T, path string) string {
			t.Helper()
			link := filepath.Join(filepath.Dir(path), "linked.exe")
			if err := os.Link(path, link); err != nil {
				t.Skipf("link: %v", err)
			}
			return link
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			file := tc.file
			if file == "" {
				file = "browser.cmd"
			}
			batch, ran := filepath.Join(dir, file), filepath.Join(dir, "ran.txt")
			if err := os.WriteFile(batch, []byte("@>\""+ran+"\" echo %*\r\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
				t.Fatal(err)
			}
			// The text after the & in the profile path below names
			// D\loadline\browser, from the home directory the browser starts
			// in: a stand-in there leaves a mark if cmd.exe runs it.
			home, marker := filepath.Join(dir, "home"), filepath.Join(dir, "marker.txt")
			standIn := filepath.Join(home, "D", "loadline")
			if err := os.MkdirAll(standIn, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(standIn, "browser.cmd"), []byte("@>\""+marker+"\" echo ran\r\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("USERPROFILE", home)
			profile := filepath.Join(dir, "R&D", "loadline", "browser")
			pref := tc.pref(t, batch)

			reaped, err := launch.OpenBrowser(batchURL, launch.Browser{Pref: pref, AppWindow: true, ProfileDir: profile})

			if err != nil {
				t.Logf("Open(%q) started nothing: %v", pref, err)
			} else {
				select {
				case <-reaped:
				case <-time.After(reapLimit()):
					t.Fatalf("what Open(%q) started still ran %v later", pref, reapLimit())
				}
			}
			args, err := os.ReadFile(ran)
			switch {
			case err == nil && strings.Contains(string(args), "--user-data-dir"):
				t.Errorf("the batch file named %q got a profile through cmd.exe: %s", pref, args)
			case tc.mustRun && err != nil:
				t.Errorf("the batch file named %q never ran: %v", pref, err)
			case tc.mustRun && !strings.HasSuffix(strings.TrimSpace(string(args)), "--app="+batchURL):
				t.Errorf("the batch file named %q got %q, want the app window's URL last", pref, args)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Errorf("with the batch file named %q, cmd.exe ran the text after the ampersand in the profile path as a command of its own", pref)
			}
		})
	}
}
