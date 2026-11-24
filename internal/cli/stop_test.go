package cli_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/clocktest"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/deadlinetest"
	"github.com/dsj7419/loadline/internal/exetest"
	"github.com/dsj7419/loadline/internal/forward"
)

// hangBound is how long a wait in these tests lasts before the run is taken
// to have hung. It decides nothing: each wait ends as soon as what it waits
// for holds.
const hangBound = 30 * time.Second

// fakeLoadline stands in for a running Loadline server of any version, on a
// loopback port of its own. It answers health with its version, and a stop
// request with its token as it is told.
type fakeLoadline struct {
	t       *testing.T
	version string
	token   []byte
	// process is the process a stop answer names. Nil means one started for
	// the fake, which it ends once it has answered.
	process *exetest.Process
	// refuse, when set, is the status every stop request gets, whatever its
	// token, as from a server that has no stop route.
	refuse int
	// answer, when set, is the body of the 200 a stop request with the token
	// gets, in place of the one that names process.
	answer string
	// lingers, when set, keeps the fake answering, and its process running,
	// once it has answered a stop request.
	lingers bool
	// keepsProcess, when set, stops the fake answering once it has answered a
	// stop request, but leaves its process running.
	keepsProcess bool
	// successor, when set, is the version of a server that has the port the
	// moment the fake has answered a stop request, as one a status line
	// starts may: the fake answers health as that server from then on, while
	// its own process ends.
	successor string

	port     int
	server   *http.Server
	stops    atomic.Int32
	shutdown sync.Once
	gone     chan struct{}
}

// newFakeLoadline starts a fake on a free port, as edit sets it up.
func newFakeLoadline(t *testing.T, edit ...func(*fakeLoadline)) *fakeLoadline {
	t.Helper()
	f := &fakeLoadline{t: t, version: "0.1.1", gone: make(chan struct{})}
	for _, e := range edit {
		e(f)
	}
	if f.process == nil {
		f.process = exetest.Start(t)
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f.port = portOf(t, listener.Addr())
	f.serve(listener)
	return f
}

func (f *fakeLoadline) serve(listener net.Listener) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		version := f.version
		if f.successor != "" && f.stopsAsked() > 0 {
			version = f.successor
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"app": "loadline", "version": version})
	})
	mux.HandleFunc("POST /api/stop", f.handleStop)
	f.server = &http.Server{Handler: mux, ReadHeaderTimeout: hangBound}
	go func() { _ = f.server.Serve(listener) }()
	f.t.Cleanup(func() { f.stopAnswering(context.Background()) })
}

func (f *fakeLoadline) handleStop(w http.ResponseWriter, r *http.Request) {
	f.stops.Add(1)
	if f.refuse != 0 {
		http.Error(w, `{"error":"refused"}`, f.refuse)
		return
	}
	if r.Header.Get("X-Loadline-Token") != string(f.token) || len(f.token) == 0 {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	answer := f.answer
	if answer == "" {
		answer = fmt.Sprintf(`{"stopping":true,"pid":%d}`, f.process.PID())
	}
	_, _ = fmt.Fprint(w, answer)
	if f.lingers {
		return
	}
	if f.successor != "" {
		go func() {
			f.process.End()
			close(f.gone)
		}()
		return
	}
	// Once the answer is out, as a real server shuts down: its listener
	// first, the process last.
	go func(ctx context.Context) {
		f.stopAnswering(ctx)
		if !f.keepsProcess {
			f.process.End()
		}
		close(f.gone)
	}(context.WithoutCancel(r.Context()))
}

// stopAnswering closes the fake's listener, waiting for the requests it is
// answering.
func (f *fakeLoadline) stopAnswering(ctx context.Context) {
	f.shutdown.Do(func() {
		ctx, cancel := context.WithTimeout(ctx, hangBound)
		defer cancel()
		_ = f.server.Shutdown(ctx)
	})
}

// stopsAsked returns how many stop requests reached the fake.
func (f *fakeLoadline) stopsAsked() int { return int(f.stops.Load()) }

func (f *fakeLoadline) client(token []byte) forward.Client {
	return forward.Client{BaseURL: forward.LoopbackURL(f.port), Token: token, HealthTimeout: probeWindow}
}

// answers reports whether the fake answers health.
func (f *fakeLoadline) answers() bool { return f.client(nil).Healthy(f.t.Context()) }

// ours makes a fake that takes token, the installation's own.
func ours(token []byte) func(*fakeLoadline) { return func(f *fakeLoadline) { f.token = token } }

// earlier makes a fake of 0.1.0, which predates the stop route.
func earlier(status int) func(*fakeLoadline) {
	return func(f *fakeLoadline) { f.version, f.refuse = "0.1.0", status }
}

// tokenFor is a token of the form the server takes.
func tokenFor(b byte) []byte { return []byte(strings.Repeat(fmt.Sprintf("%02x", b), 32)) }

func TestStopServer_Outcomes(t *testing.T) {
	token := tokenFor(1)
	ask := func(t *testing.T, client forward.Client) (string, error) {
		t.Helper()
		return cli.StopServer(t.Context(), client, nil, hangBound)
	}

	t.Run("nothing answers", func(t *testing.T) {
		client := forward.Client{BaseURL: forward.LoopbackURL(freePort(t)), Token: token, HealthTimeout: probeWindow}
		if got, err := ask(t, client); got != "no server" || err != nil {
			t.Errorf("StopServer = %s, %v; want no server", got, err)
		}
	})

	t.Run("a server that stops", func(t *testing.T) {
		fake := newFakeLoadline(t, ours(token))

		got, err := ask(t, fake.client(token))

		if got != "stopped" || err != nil {
			t.Errorf("StopServer = %s, %v; want stopped", got, err)
		}
		if fake.answers() {
			t.Error("StopServer said stopped while the server still answers")
		}
	})

	for _, status := range []int{http.StatusMethodNotAllowed, http.StatusNotFound} {
		t.Run(fmt.Sprintf("a server without the route, %d", status), func(t *testing.T) {
			fake := newFakeLoadline(t, ours(token), earlier(status))

			if got, err := ask(t, fake.client(token)); got != "cannot ask" || err != nil {
				t.Errorf("StopServer = %s, %v; want cannot ask", got, err)
			}
			if !fake.answers() {
				t.Error("the server stopped answering, want it left running")
			}
		})
	}

	t.Run("another installation's server", func(t *testing.T) {
		fake := newFakeLoadline(t, ours(tokenFor(2)))

		if got, err := ask(t, fake.client(token)); got != "not ours" || err != nil {
			t.Errorf("StopServer = %s, %v; want not ours", got, err)
		}
		if !fake.answers() {
			t.Error("the server stopped answering, want it left running")
		}
	})

	for name, edit := range map[string]func(*fakeLoadline){
		"an answer that names no process": func(f *fakeLoadline) { f.answer, f.lingers = `{"stopping":true}`, true },
		"an answer that is no JSON":       func(f *fakeLoadline) { f.answer, f.lingers = `stopping`, true },
		"a server error":                  func(f *fakeLoadline) { f.refuse = http.StatusInternalServerError },
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeLoadline(t, ours(token), edit)

			if got, err := ask(t, fake.client(token)); got != "failed" || err == nil {
				t.Errorf("StopServer = %s, %v; want failed, with why", got, err)
			}
		})
	}

	// The server stops answering well before its process exits, which is
	// what frees its binary and the history: stopped means both.
	t.Run("a server whose process lives on", func(t *testing.T) {
		fake := newFakeLoadline(t, ours(token), func(f *fakeLoadline) { f.keepsProcess = true })
		clock := clocktest.New(t0)
		done := make(chan string, 1)
		go func() {
			got, _ := cli.StopServer(context.Background(), fake.client(token), clock.AfterFunc, time.Minute)
			done <- got
		}()

		for range 3 {
			waitPollArmed(t, clock)
			select {
			case got := <-done:
				t.Fatalf("StopServer = %s while the process still runs, want it waiting", got)
			default:
			}
			clock.Advance(cli.ServerPollEvery)
		}
		waitPollArmed(t, clock)
		if fake.answers() {
			t.Fatal("the fake still answers, want only its process left")
		}
		fake.process.End()
		clock.Advance(cli.ServerPollEvery)

		if got := receiveWithin(t, done); got != "stopped" {
			t.Errorf("StopServer once the process exited = %s, want stopped", got)
		}
	})

	t.Run("a server whose process outlives the wait", func(t *testing.T) {
		fake := newFakeLoadline(t, ours(token), func(f *fakeLoadline) { f.keepsProcess = true })
		clock := clocktest.New(t0)
		done := make(chan string, 1)
		go func() {
			got, _ := cli.StopServer(context.Background(), fake.client(token), clock.AfterFunc, time.Minute)
			done <- got
		}()

		waitPollArmed(t, clock)
		clock.Advance(time.Minute)

		if got := receiveWithin(t, done); got != "still running" {
			t.Errorf("StopServer = %s, want still running", got)
		}
	})

	// Windows may refuse to let the process be watched. Once the server is
	// silent there is nothing more to wait for, and that is said as it is.
	t.Run("a server whose process cannot be watched", func(t *testing.T) {
		cli.CannotWatchProcesses(t)
		fake := newFakeLoadline(t, ours(token), func(f *fakeLoadline) { f.keepsProcess = true })

		if got, err := ask(t, fake.client(token)); got != "unwatched" || err != nil {
			t.Errorf("StopServer = %s, %v; want unwatched", got, err)
		}
	})

	// Once the process the answer named has exited, the server is gone, its
	// history closed and its lock let go of: what answers on the port is
	// another server, as one a status line starts the moment the port is
	// free.
	t.Run("a port that answers on though the server's process exited", func(t *testing.T) {
		gone := exetest.Start(t)
		gone.End()
		fake := newFakeLoadline(t, ours(token), func(f *fakeLoadline) { f.process, f.lingers = gone, true })

		if got, err := ask(t, fake.client(token)); got != "stopped" || err != nil {
			t.Errorf("StopServer = %s, %v; want stopped", got, err)
		}
	})

	// The next server has the port before the asked one is ever seen
	// silent. It is not asked to stop in its turn.
	t.Run("the next server has the port at once", func(t *testing.T) {
		fake := newFakeLoadline(t, ours(token), func(f *fakeLoadline) { f.successor = "0.1.1-next" })

		if got, err := ask(t, fake.client(token)); got != "stopped" || err != nil {
			t.Errorf("StopServer = %s, %v; want stopped", got, err)
		}
		if n := fake.stopsAsked(); n != 1 {
			t.Errorf("%d stop requests reached the port, want the one: the next server was asked to stop too", n)
		}
	})

	// A process that cannot be watched is gone, as far as can be told, only
	// once nothing answers.
	t.Run("a server whose process cannot be watched answers past the wait", func(t *testing.T) {
		cli.CannotWatchProcesses(t)
		fake := newFakeLoadline(t, ours(token), func(f *fakeLoadline) { f.lingers = true })
		clock := clocktest.New(t0)
		done := make(chan string, 1)
		go func() {
			got, _ := cli.StopServer(context.Background(), fake.client(token), clock.AfterFunc, time.Minute)
			done <- got
		}()

		waitPollArmed(t, clock)
		clock.Advance(time.Minute)

		if got := receiveWithin(t, done); got != "still running" {
			t.Errorf("StopServer = %s, want still running", got)
		}
	})
}

// waitPollArmed waits until the wait for the next check of a stopping
// server is pending on clock, with all of it still to run.
func waitPollArmed(t *testing.T, clock *clocktest.Clock) {
	t.Helper()
	waitFor(t, "the next check of the server", func() bool { return slices.Contains(clock.JustArmed(), cli.ServerPollEvery) })
}

func receiveWithin[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(hangBound):
		t.Fatalf("nothing received within %v", hangBound)
		var zero T
		return zero
	}
}

// stopHome points Loadline at a home of its own, and returns it.
func stopHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", home)
	return home
}

// writeConfigPort writes a config into home that names port.
func writeConfigPort(t *testing.T, home string, port int) {
	t.Helper()
	contents := fmt.Sprintf(`{"port":%d,"claudePath":%q}`, port, filepath.Join(home, "no-claude"))
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

// installToken creates the token of the installation in home.
func installToken(t *testing.T, home string) []byte {
	t.Helper()
	token, err := config.EnsureToken(filepath.Join(home, "token"))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestRunStop_ExitCodes(t *testing.T) {
	t.Run("stopped", func(t *testing.T) {
		home := stopHome(t)
		fake := newFakeLoadline(t, ours(installToken(t, home)))
		writeConfigPort(t, home, fake.port)

		got := run(t, "stop")

		want := fmt.Sprintf("Stopped the Loadline server on port %d. A running Claude Code session starts it again within 30 seconds.\n", fake.port)
		if got.code != 0 || got.stdout != want || got.stderr != "" {
			t.Errorf("stop = %d, stdout %q, stderr %q; want 0 and %q", got.code, got.stdout, got.stderr, want)
		}
		if !exited(t, fake.process.PID()) {
			t.Error("stop exited 0 while the server's process still runs")
		}
	})

	t.Run("--port names another than the configured one", func(t *testing.T) {
		home := stopHome(t)
		writeConfigPort(t, home, freePort(t))
		fake := newFakeLoadline(t, ours(installToken(t, home)))

		if got := run(t, "stop", "--port", fmt.Sprint(fake.port)); got.code != 0 || fake.stopsAsked() != 1 {
			t.Errorf("stop --port %d = %d, stderr %q, and %d stop requests; want 0 and the one", fake.port, got.code, got.stderr, fake.stopsAsked())
		}
	})

	t.Run("nothing answers", func(t *testing.T) {
		port := freePort(t)
		writeConfigPort(t, stopHome(t), port)

		got := run(t, "stop")

		if want := fmt.Sprintf("No Loadline server is running on port %d.\n", port); got.code != 0 || got.stdout != want || got.stderr != "" {
			t.Errorf("stop = %d, stdout %q, stderr %q; want 0 and %q", got.code, got.stdout, got.stderr, want)
		}
	})

	for _, status := range []int{http.StatusMethodNotAllowed, http.StatusNotFound} {
		t.Run(fmt.Sprintf("an earlier server, %d", status), func(t *testing.T) {
			home := stopHome(t)
			fake := newFakeLoadline(t, ours(installToken(t, home)), earlier(status))
			writeConfigPort(t, home, fake.port)

			got := run(t, "stop")

			if got.code != 1 || got.stdout != "" || !strings.Contains(got.stderr, "loadline open") || !strings.Contains(got.stderr, "earlier version") {
				t.Errorf("stop = %d, stdout %q, stderr %q; want 1 and how to end it", got.code, got.stdout, got.stderr)
			}
			if !fake.answers() {
				t.Error("the earlier server stopped answering, want it left running")
			}
		})
	}

	t.Run("another installation's server", func(t *testing.T) {
		home := stopHome(t)
		installToken(t, home)
		fake := newFakeLoadline(t, ours(tokenFor(2)))
		writeConfigPort(t, home, fake.port)

		got := run(t, "stop")

		want := fmt.Sprintf("loadline: the server on port %d did not take this installation's token: it belongs to another installation, and was left running\n", fake.port)
		if got.code != 1 || got.stderr != want {
			t.Errorf("stop = %d, stderr %q; want 1 and %q", got.code, got.stderr, want)
		}
	})

	t.Run("still running", func(t *testing.T) {
		cli.SetStopWait(t, 100*time.Millisecond)
		home := stopHome(t)
		fake := newFakeLoadline(t, ours(installToken(t, home)), func(f *fakeLoadline) { f.keepsProcess = true })
		writeConfigPort(t, home, fake.port)

		got := run(t, "stop")

		want := fmt.Sprintf("loadline: the server on port %d was asked to stop, but its process %d still runs after 100ms; run loadline stop again in a moment\n",
			fake.port, fake.process.PID())
		if got.code != 1 || got.stderr != want {
			t.Errorf("stop = %d, stderr %q; want 1 and %q", got.code, got.stderr, want)
		}
	})

	t.Run("failed", func(t *testing.T) {
		home := stopHome(t)
		fake := newFakeLoadline(t, ours(installToken(t, home)), func(f *fakeLoadline) { f.refuse = http.StatusInternalServerError })
		writeConfigPort(t, home, fake.port)

		got := run(t, "stop")

		if prefix := fmt.Sprintf("loadline: stop the server on port %d: ", fake.port); got.code != 1 || !strings.HasPrefix(got.stderr, prefix) ||
			!strings.Contains(got.stderr, "500") || !strings.HasSuffix(got.stderr, "; run loadline doctor to check the server\n") {
			t.Errorf("stop = %d, stderr %q; want 1, the server's answer and what to run", got.code, got.stderr)
		}
	})

	t.Run("a process that cannot be watched", func(t *testing.T) {
		cli.CannotWatchProcesses(t)
		home := stopHome(t)
		fake := newFakeLoadline(t, ours(installToken(t, home)), func(f *fakeLoadline) { f.keepsProcess = true })
		writeConfigPort(t, home, fake.port)

		got := run(t, "stop")

		want := fmt.Sprintf("loadline: the server on port %d stopped answering, but its process %d could not be watched to see it exit; "+
			"run loadline doctor to check the server\n", fake.port, fake.process.PID())
		if got.code != 1 || got.stderr != want {
			t.Errorf("stop = %d, stderr %q; want 1 and %q", got.code, got.stderr, want)
		}
	})

	for name, args := range map[string][]string{
		"a privileged port": {"stop", "--port", "80"},
		"an argument":       {"stop", "now"},
		"an unknown flag":   {"stop", "--force"},
	} {
		t.Run(name, func(t *testing.T) {
			writeConfigPort(t, stopHome(t), freePort(t))

			if got := run(t, args...); got.code != 2 || got.stdout != "" || !strings.Contains(got.stderr, "loadline stop") {
				t.Errorf("%q = %d, stdout %q, stderr %q; want 2 and stop's usage", args, got.code, got.stdout, got.stderr)
			}
		})
	}
}

// Stop prints the status a refusal came with, and the reason phrase of its
// status line is whatever answers on the port's own text: escape sequences
// there would retitle the terminal, erase stop's line and write another in
// its place. Stop names the status by its code alone.
func TestRunStop_NamesARefusalByItsCodeAlone(t *testing.T) {
	home := stopHome(t)
	installToken(t, home)
	port := refuseStopWith(t, "HTTP/1.1 500 \x1b]0;title set by the port's holder\a\x1b[2K\x1b[1GStopped the Loadline server.")
	writeConfigPort(t, home, port)

	got := run(t, "stop")

	want := fmt.Sprintf("loadline: stop the server on port %d: post /api/stop: the server answered 500 Internal Server Error; "+
		"run loadline doctor to check the server\n", port)
	if got.code != 1 || got.stdout != "" || got.stderr != want {
		t.Errorf("stop = %d, stdout %q, stderr %q; want 1 and %q", got.code, got.stdout, got.stderr, want)
	}
}

// refuseStopWith serves a free port that answers health as Loadline does,
// and every other request with statusLine, as a process that took the port
// may: Go's own server writes a reason phrase of its own. It returns the
// port.
func refuseStopWith(t *testing.T, statusLine string) int {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	const health = `{"app":"loadline","version":"0.1.1"}`
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				req, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, req.Body)
				if req.URL.Path == "/healthz" {
					_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
						len(health), health)
					return
				}
				_, _ = io.WriteString(conn, statusLine+"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			}()
		}
	}()
	return portOf(t, listener.Addr())
}

// A server busy shutting down or starting may be slow to answer, so stop
// gives its health check 2 s, longer than a hook can wait, before it says
// that no server runs. The server times the wait, from the request's
// arrival until the client gives up (deadlinetest).
func TestRunStop_GivesABusyServerTwoSecondsToAnswer(t *testing.T) {
	home := stopHome(t)
	held := make(chan time.Duration, 1)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{ReadHeaderTimeout: hangBound, Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		arrived := time.Now()
		<-r.Context().Done()
		select {
		case held <- time.Since(arrived):
		default:
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	port := portOf(t, listener.Addr())
	writeConfigPort(t, home, port)

	got := run(t, "stop")

	if want := fmt.Sprintf("No Loadline server is running on port %d.\n", port); got.code != 0 || got.stdout != want || got.stderr != "" {
		t.Errorf("stop = %d, stdout %q, stderr %q; want 0 and %q", got.code, got.stdout, got.stderr, want)
	}
	select {
	case d := <-held:
		deadlinetest.AssertHeldFor(t, "stop's health check", d, 2*time.Second)
	case <-time.After(hangBound):
		t.Fatal("the server saw no health check end")
	}
}

// stop must not create the token: a server that starts later would take
// that one, which no hook holds.
func TestRunStop_NeverCreatesAToken(t *testing.T) {
	assertNoToken := func(t *testing.T, home string) {
		t.Helper()
		if names := entries(t, home); !slices.Equal(names, []string{"config.json"}) {
			t.Errorf("Loadline home holds %q after stop, want only the config", names)
		}
		if _, err := os.Stat(filepath.Join(home, "token")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stop created the token (%v)", err)
		}
	}

	t.Run("nothing answers", func(t *testing.T) {
		home := stopHome(t)
		writeConfigPort(t, home, freePort(t))

		if got := run(t, "stop"); got.code != 0 {
			t.Errorf("stop = %d, stderr %q; want 0", got.code, got.stderr)
		}
		assertNoToken(t, home)
	})

	t.Run("a server answers", func(t *testing.T) {
		home := stopHome(t)
		fake := newFakeLoadline(t, ours(tokenFor(2)))
		writeConfigPort(t, home, fake.port)

		if got := run(t, "stop"); got.code != 1 || !strings.Contains(got.stderr, "another installation") {
			t.Errorf("stop = %d, stderr %q; want 1 and the server left to its installation", got.code, got.stderr)
		}
		assertNoToken(t, home)
		if !fake.answers() {
			t.Error("the server stopped answering, want it left running")
		}
	})
}

// A server from 0.1.0 has no stop route, and a server Windows detached gets
// no signal, so the instruction gives the line that ends it, and nothing
// else, wherever it was started from: 0.1.0's open and serve run the binary
// the user ran, not the one install put in bin. On Windows that is the
// process listening on the port, when its name is Loadline's; elsewhere
// every loadline serve of the user's, as the instruction says. Either line
// says what it did.
func TestOldServerInstruction(t *testing.T) {
	t.Run("windows", func(t *testing.T) {
		text := cli.OldServerInstruction("windows", 8289)

		for _, want := range []string{
			"End it once, in PowerShell:\n",
			"Get-NetTCPConnection -State Listen -LocalPort 8289 ",
			"Where-Object { $_.ProcessName -like 'loadline*' }",
			"| Stop-Process -PassThru",
			"'Nothing was ended: no Loadline server listens on port 8289.'",
			"Then start the new one:\n  loadline open\n",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("instruction %q lacks %q", text, want)
			}
		}
	})

	t.Run("unix", func(t *testing.T) {
		text := cli.OldServerInstruction("linux", 8289)

		for _, want := range []string{
			"port 8289", "this ends every loadline serve that runs as you",
			"and may also end another process of yours whose command line names loadline serve", "Nothing was ended", "loadline open",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("instruction %q lacks %q", text, want)
			}
		}
		// Run as root, pkill would end every user's server: the line keeps
		// to the processes of the user who runs it, as it says.
		m := regexp.MustCompile(`pkill -u "\$\(id -u\)" -f '([^']*)'`).FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("instruction %q, want a pkill line kept to the user's own processes", text)
		}
		pattern, err := regexp.Compile(m[1])
		if err != nil {
			t.Fatalf("pattern %q does not compile: %v", m[1], err)
		}
		for _, folder := range []string{`plain`, `with space`, `O'Neil`, `[brackets]`, `c++`, `Ünïcödé 数据`, "back`tick"} {
			for _, command := range []string{
				"/home/" + folder + "/.local/state/loadline/bin/loadline serve",
				"/home/" + folder + "/Downloads/loadline_0.1.0/loadline serve --no-open",
			} {
				if !pattern.MatchString(command) {
					t.Errorf("pattern %q misses %s", m[1], command)
				}
			}
		}
		for _, command := range []string{"loadline serve --port 9000", "./loadline serve", "bin/loadline serve --no-open"} {
			if !pattern.MatchString(command) {
				t.Errorf("pattern %q misses %s, a loadline serve started from PATH or a folder", m[1], command)
			}
		}
		// pkill -f matches the whole command line, so a program that names a
		// loadline folder and serve among its arguments is no server, and is
		// left alone when it was run from PATH.
		for _, command := range []string{
			"/home/u/bin/loadline install", "/home/u/bin/loadline servers", "/home/u/bin/myloadline serve",
			"grep loadline serve", "make -C /home/u/src/loadline serve", "npm --prefix /home/u/src/loadline serve",
			"vim /home/u/notes/loadline serve", "sh -c /home/u/bin/loadline serve --no-open",
		} {
			if pattern.MatchString(command) {
				t.Errorf("pattern %q matches %s", m[1], command)
			}
		}
		// pkill sees no boundary between arguments, so one run by an absolute
		// path, as a script is through its shebang, that names .../loadline
		// serve later on is ended too, as the line says it may be.
		for _, command := range []string{"/usr/bin/vim notes/loadline serve", "/bin/sh /home/u/bin/start /home/u/bin/loadline serve"} {
			if !pattern.MatchString(command) {
				t.Errorf("pattern %q no longer matches %s: the line's warning needs another look", m[1], command)
			}
		}
	})
}

func TestRun_HelpListsStop(t *testing.T) {
	if got := run(t, "help"); !strings.Contains(got.stdout, "\n  stop ") {
		t.Errorf("usage %q does not list stop", got.stdout)
	}
}
