package cli_test

import (
	"context"
	"errors"
	"fmt"
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
		_, _ = fmt.Fprintf(w, `{"app":"loadline","version":%q}`, version)
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

// psLike reports whether value matches pattern as PowerShell's -like does,
// ignoring case: * matches any run, ? one character, [...] one of a set,
// and a backtick takes the character after it as it is.
func psLike(value, pattern string) bool {
	var expr strings.Builder
	expr.WriteString("(?is)^")
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		switch r := runes[i]; r {
		case '`':
			if i+1 < len(runes) {
				i++
				expr.WriteString(regexp.QuoteMeta(string(runes[i])))
			}
		case '*':
			expr.WriteString(".*")
		case '?':
			expr.WriteString(".")
		case '[':
			end := slices.Index(runes[i:], ']')
			if end < 0 {
				return false
			}
			expr.WriteString("[" + regexp.QuoteMeta(string(runes[i+1:i+end])) + "]")
			i += end
		default:
			expr.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	expr.WriteString("$")
	return regexp.MustCompile(expr.String()).MatchString(value)
}

// psSingleQuotes are the characters PowerShell reads as a single quote.
const psSingleQuotes = "'‘’‚‛"

// likePatterns returns the -like patterns of the PowerShell line in text,
// each read as PowerShell reads a single-quoted string: it ends at any of
// its five single quotes, and two of them in a row stand for the second.
func likePatterns(t *testing.T, text string) []string {
	t.Helper()
	var patterns []string
	for rest := text; ; {
		_, after, found := strings.Cut(rest, "-like ")
		if !found {
			return patterns
		}
		runes := []rune(after)
		if len(runes) == 0 || !strings.ContainsRune(psSingleQuotes, runes[0]) {
			t.Fatalf("-like takes no single-quoted string in %q", text)
		}
		var pattern []rune
		i := 1
		for ; i < len(runes); i++ {
			if strings.ContainsRune(psSingleQuotes, runes[i]) {
				if i+1 >= len(runes) || !strings.ContainsRune(psSingleQuotes, runes[i+1]) {
					break
				}
				i++
			}
			pattern = append(pattern, runes[i])
		}
		if i >= len(runes) {
			t.Fatalf("a -like string is left open in %q", text)
		}
		patterns = append(patterns, string(pattern))
		rest = string(runes[i+1:])
	}
}

// matchesAny reports whether value matches one of patterns, as match does.
func matchesAny(value string, patterns []string, match func(value, pattern string) bool) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return match(value, p) })
}

// A server from 0.1.0 has no stop route, and a server Windows detached gets
// no signal, so the instruction gives the line that ends it, and nothing
// else. On Windows that is the process listening on the port, as long as it
// runs from the folder install put the binary in, under the name it started
// with or one install moved it aside to.
func TestOldServerInstruction(t *testing.T) {
	odd := []string{
		`plain`,
		`with space`,
		`O'Neil`,
		`O’Neil`,
		`a&b`,
		`(x86)`,
		`[brackets]`,
		`c++`,
		`Ünïcödé 数据`,
		"back`tick",
	}

	t.Run("windows", func(t *testing.T) {
		for _, name := range odd {
			bin := `C:\Users\` + name + `\AppData\Local\loadline\bin`
			text := cli.OldServerInstruction("windows", bin, 8289)

			patterns := likePatterns(t, text)
			if len(patterns) == 0 || !strings.Contains(text, "Get-NetTCPConnection -State Listen -LocalPort 8289") ||
				!strings.Contains(text, "| Stop-Process") || !strings.Contains(text, "loadline open") {
				t.Errorf("%s: instruction %q, want a PowerShell line that ends the process on port 8289, and loadline open", name, text)
				continue
			}
			for _, image := range []string{bin + `\loadline.exe`, bin + `\loadline.exe.old`, bin + `\loadline.exe.old-27`} {
				if !matchesAny(image, patterns, psLike) {
					t.Errorf("%s: patterns %q miss %s", name, patterns, image)
				}
			}
			for _, image := range []string{`C:\elsewhere\bin\loadline.exe`, bin + `x\loadline.exe`, bin + `\other.exe`} {
				if matchesAny(image, patterns, psLike) {
					t.Errorf("%s: patterns %q match %s", name, patterns, image)
				}
			}
		}
	})

	t.Run("unix", func(t *testing.T) {
		for _, name := range odd {
			bin := "/home/" + name + "/.local/state/loadline/bin"
			exe := bin + "/loadline"
			text := cli.OldServerInstruction("linux", bin, 8289)

			if !strings.Contains(text, "loadline open") || !strings.Contains(text, "8289") || !strings.Contains(text, "pkill -f") {
				t.Errorf("%s: instruction %q, want the port, pkill -f and loadline open", name, text)
				continue
			}
			if strings.ContainsRune(name, '\'') {
				// No single-quoted line can hold it as written, so the tool
				// and the binary's path are named instead.
				if !strings.Contains(text, exe) || strings.Contains(text, "pkill -f '") {
					t.Errorf("%s: instruction %q, want pkill -f and the path %s, with no quoted line", name, text, exe)
				}
				continue
			}
			m := regexp.MustCompile(`pkill -f '([^']*)'`).FindStringSubmatch(text)
			if m == nil {
				t.Errorf("%s: instruction %q, want a pkill -f line", name, text)
				continue
			}
			pattern, err := regexp.Compile(m[1])
			if err != nil {
				t.Errorf("%s: pattern %q does not compile: %v", name, m[1], err)
				continue
			}
			if !pattern.MatchString(exe + " serve --no-open") {
				t.Errorf("%s: pattern %q misses %s serve", name, m[1], exe)
			}
			for _, command := range []string{
				exe + " install", "/usr/bin/env " + exe + " serve", bin + "x/loadline serve",
				strings.Replace(exe, ".local", "xlocal", 1) + " serve",
			} {
				if pattern.MatchString(command) {
					t.Errorf("%s: pattern %q matches %s", name, m[1], command)
				}
			}
		}
	})
}

func TestRun_HelpListsStop(t *testing.T) {
	if got := run(t, "help"); !strings.Contains(got.stdout, "\n  stop ") {
		t.Errorf("usage %q does not list stop", got.stdout)
	}
}
