package cli_test

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/exetest"
)

// fakeAt starts a fake on port, which must be free, as edit sets it up.
func fakeAt(t *testing.T, port int, edit ...func(*fakeLoadline)) *fakeLoadline {
	t.Helper()
	f := &fakeLoadline{t: t, version: "0.1.1", gone: make(chan struct{}), port: port}
	for _, e := range edit {
		e(f)
	}
	if f.process == nil {
		f.process = exetest.Start(t)
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listen on port %d: %v", port, err)
	}
	f.serve(listener)
	return f
}

// serverStarts records the servers install starts, in place of starting
// them.
type serverStarts struct {
	mu     sync.Mutex
	starts []string
}

// record notes a start of exe with args.
func (s *serverStarts) record(exe string, args ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts = append(s.starts, strings.Join(append([]string{exe}, args...), " "))
}

func (s *serverStarts) started() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.starts)
}

// noStarts fails the test for any server install starts.
func noStarts(t *testing.T) *serverStarts {
	t.Helper()
	starts := &serverStarts{}
	cli.SetServerSpawn(t, func(exe string, args ...string) error {
		starts.record(exe, args...)
		t.Errorf("install started %s %q, want no server started", exe, args)
		return nil
	})
	return starts
}

// handOverHome is an installation in a sandbox whose config names the port
// of a fake running server, made as edit says, and that takes the
// installation's token unless edit says otherwise. Install runs as from a
// normal shell, whatever shell runs the test.
func handOverHome(t *testing.T, edit ...func(*fakeLoadline)) (home, dir string, fake *fakeLoadline) {
	t.Helper()
	cli.SetElevated(t, false)
	home, dir = sandbox(t, ownerSettings)
	fake = newFakeLoadline(t, append([]func(*fakeLoadline){ours(installToken(t, home))}, edit...)...)
	writeConfigPort(t, home, fake.port)
	return home, dir, fake
}

// The old server keeps running the binary it started from, and would never
// take up the new one while sessions keep it busy. Install asks it to stop,
// waits until its process is gone, and starts the binary it put in place.
func TestInstall_HandsARunningServerOverToTheNewBinary(t *testing.T) {
	setVersion(t, "0.1.1-test")
	home, dir, fake := handOverHome(t)
	starts := &serverStarts{}
	var newServer *fakeLoadline
	cli.SetServerSpawn(t, func(exe string, args ...string) error {
		starts.record(exe, args...)
		if !exited(t, fake.process.PID()) {
			t.Error("install started the new server while the old one's process still ran")
		}
		newServer = fakeAt(t, fake.port, func(f *fakeLoadline) { f.version = "0.1.1-test" })
		return nil
	})

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 0 || got.stderr != "" {
		t.Fatalf("install = %d, stderr %q; want 0 and nothing", got.code, got.stderr)
	}
	want := "Restarted the Loadline server on 0.1.1-test; reload an open dashboard.\n" + restartNotice + "\n"
	if !strings.HasSuffix(got.stdout, want) {
		t.Errorf("stdout does not end with %q:\n%s", want, got.stdout)
	}
	if want := []string{filepath.FromSlash(installedExe(home)) + " serve --no-open"}; !slices.Equal(starts.started(), want) {
		t.Errorf("install started %q, want %q", starts.started(), want)
	}
	if n := fake.stopsAsked(); n != 1 {
		t.Errorf("the old server was asked to stop %d times, want once", n)
	}
	if newServer == nil || !newServer.answers() {
		t.Error("no server answers once install has returned")
	}
	if !gjson.Get(readSettings(t, dir), "hooks.SessionStart").Exists() {
		t.Error("the settings were not wired")
	}
}

// A server started from an elevated shell would run elevated for as long as
// it lives, and it runs the claude of a config the user can write. Install
// stops the old one, and leaves the start to a normal shell.
func TestInstall_DoesNotStartAServerWhenElevated(t *testing.T) {
	_, dir, fake := handOverHome(t)
	cli.SetElevated(t, true)
	starts := noStarts(t)

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 0 || got.stderr != "" {
		t.Fatalf("install = %d, stderr %q; want 0 and nothing", got.code, got.stderr)
	}
	if want := "Stopped the old server; run loadline open from a normal shell to start the new one.\n"; !strings.Contains(got.stdout, want) {
		t.Errorf("stdout lacks %q:\n%s", want, got.stdout)
	}
	if fake.answers() || !exited(t, fake.process.PID()) {
		t.Error("the old server still runs, want it stopped")
	}
	if len(starts.started()) != 0 {
		t.Errorf("install started %q", starts.started())
	}
}

// A server from 0.1.0 has no stop route. Install says how to end it once,
// and ends nothing itself.
func TestInstall_PrintsHowToStopAServerThatCannotBeAsked(t *testing.T) {
	home, dir, fake := handOverHome(t, earlier(http.StatusMethodNotAllowed))
	noStarts(t)

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 0 || got.stderr != "" {
		t.Fatalf("install = %d, stderr %q; want 0 and nothing", got.code, got.stderr)
	}
	instruction := cli.OldServerInstruction(runtime.GOOS, filepath.Dir(filepath.FromSlash(installedExe(home))), fake.port)
	if !strings.Contains(got.stdout, instruction) {
		t.Errorf("stdout lacks the instruction %q:\n%s", instruction, got.stdout)
	}
	if !fake.answers() || exited(t, fake.process.PID()) {
		t.Error("the old server was ended, want it left running")
	}
}

func TestInstall_LeavesAServerOfAnotherInstallationAlone(t *testing.T) {
	_, dir, fake := handOverHome(t, ours(tokenFor(2)))
	noStarts(t)

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 0 || got.stderr != "" {
		t.Fatalf("install = %d, stderr %q; want 0 and nothing", got.code, got.stderr)
	}
	want := fmt.Sprintf("Note: the server on port %d did not take this installation's token: it belongs to another installation, and was left running.\n", fake.port)
	if !strings.Contains(got.stdout, want) {
		t.Errorf("stdout lacks %q:\n%s", want, got.stdout)
	}
	if !fake.answers() || exited(t, fake.process.PID()) {
		t.Error("the other installation's server was stopped, want it left running")
	}
}

// Windows may refuse to let the old server's process be watched. Once that
// server is silent, the new one is started all the same: it waits for the
// old one to let go of the history.
func TestInstall_HandsOverAServerWhoseProcessCannotBeWatched(t *testing.T) {
	cli.CannotWatchProcesses(t)
	_, dir, fake := handOverHome(t, func(f *fakeLoadline) { f.keepsProcess = true })
	starts := &serverStarts{}
	cli.SetServerSpawn(t, func(exe string, args ...string) error {
		starts.record(exe, args...)
		fakeAt(t, fake.port)
		return nil
	})

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 0 || got.stderr != "" || !strings.Contains(got.stdout, "Restarted the Loadline server on ") {
		t.Errorf("install = %d, stdout %q, stderr %q; want 0 and the server restarted", got.code, got.stdout, got.stderr)
	}
	if len(starts.started()) != 1 {
		t.Errorf("install started %q, want the new server once", starts.started())
	}
}

// What triggers the hand-over is a binary that install replaced, not a
// version: with the binary as it was, the running server runs it already.
func TestInstall_DoesNotHandOverWhenTheBinaryIsUnchanged(t *testing.T) {
	home, dir := sandbox(t, ownerSettings)
	if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
		t.Fatalf("install: %s", got.stderr)
	}
	fake := newFakeLoadline(t, ours(installToken(t, home)))
	writeConfigPort(t, home, fake.port)
	writeSettings(t, dir, ownerSettings)
	noStarts(t)

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 0 || got.stderr != "" || !gjson.Get(readSettings(t, dir), "hooks.SessionStart").Exists() {
		t.Fatalf("install = %d, stderr %q; want 0, nothing, and the settings wired again", got.code, got.stderr)
	}
	if n := fake.stopsAsked(); n != 0 {
		t.Errorf("the server was asked to stop %d times, want never", n)
	}
	if strings.Contains(got.stdout, "Restarted") {
		t.Errorf("stdout says the server restarted:\n%s", got.stdout)
	}
}

// By the time the hand-over fails, the settings and the binary are in
// place: install says what happened and what to run, and exits 1.
func TestInstall_SaysWhatToDoWhenTheHandOverFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*fakeLoadline)
		spawn func(string, ...string) error
		says  string
	}{
		{
			name: "the old server runs on",
			edit: func(f *fakeLoadline) { f.keepsProcess = true },
			says: "still runs after 100ms; once it has stopped, run loadline open\n",
		},
		{
			name: "the old server fails the request",
			edit: func(f *fakeLoadline) { f.refuse = http.StatusInternalServerError },
			says: "500 Internal Server Error; run loadline open\n",
		},
		{
			name:  "the new server does not start",
			edit:  func(*fakeLoadline) {},
			spawn: func(string, ...string) error { return errors.New("no such file") },
			says:  "the old server stopped, but the new one did not start: start the server: no such file; run loadline open\n",
		},
		{
			name:  "the new server does not answer",
			edit:  func(*fakeLoadline) {},
			spawn: func(string, ...string) error { return nil },
			says:  "the old server stopped, but the new one did not start: the server started on port %d did not answer within 100ms; run loadline open\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli.SetStopWait(t, 100*time.Millisecond)
			cli.SetHandoverStartWait(t, 100*time.Millisecond)
			home, dir, fake := handOverHome(t, tc.edit)
			says := tc.says
			if strings.Contains(says, "%d") {
				says = fmt.Sprintf(says, fake.port)
			}
			if tc.spawn != nil {
				cli.SetServerSpawn(t, tc.spawn)
			} else {
				noStarts(t)
			}

			got := run(t, "install", "--config-dir", dir, "--yes")

			if got.code != 1 || !strings.HasPrefix(got.stderr, "loadline: ") || !strings.HasSuffix(got.stderr, says) ||
				strings.Count(got.stderr, "run loadline") != 1 {
				t.Errorf("install = %d, stderr %q; want 1 and an error that ends %q, with that one hint", got.code, got.stderr, says)
			}
			if !strings.HasSuffix(got.stdout, restartNotice+"\n") {
				t.Errorf("stdout does not end with the restart notice:\n%s", got.stdout)
			}
			if _, err := os.Stat(filepath.FromSlash(installedExe(home))); err != nil || !gjson.Get(readSettings(t, dir), "hooks.SessionStart").Exists() {
				t.Errorf("the settings or the binary are not in place (%v)", err)
			}
		})
	}
}
