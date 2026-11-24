package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/launch"
)

// serverStartWait is how long open waits for a server it started to answer.
const serverStartWait = 3 * time.Second

// openFixture is a loadline open run against the server of a serveFixture,
// configured for Brave in an app window.
type openFixture struct {
	*serveFixture
	opens  *browserOpens
	spawns *spawner
}

func newOpenFixture(t *testing.T) *openFixture {
	t.Helper()
	f := &openFixture{serveFixture: newServeFixture(t), opens: &browserOpens{}, spawns: &spawner{}}
	f.writeConfig(fmt.Sprintf(`{"port":%d,"browser":"brave","appWindow":true}`, f.port))
	return f
}

func (f *openFixture) options(spawn func(string, ...string) error) cli.OpenOptions {
	return cli.OpenOptions{
		Paths: f.paths,
		Home:  f.home,
		Exe:   testExe,
		Spawn: func(exe string, args ...string) error {
			if err := f.spawns.spawn(exe, args...); err != nil {
				return err
			}
			return spawn(exe, args...)
		},
		Open: f.opens.open,
	}
}

type openResult struct {
	code           int
	stdout, stderr string
}

func runOpen(ctx context.Context, o cli.OpenOptions) openResult {
	var stdout, stderr bytes.Buffer
	code := cli.OpenDashboard(ctx, o, cli.IO{In: strings.NewReader(""), Out: &stdout, Err: &stderr})
	return openResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// assertOpenedSignedIn checks that the configured browser was opened once,
// at a login URL that signs it in to the server.
func (f *openFixture) assertOpenedSignedIn(t *testing.T) {
	t.Helper()
	urls, browsers := f.opens.opened()
	if len(urls) != 1 {
		t.Fatalf("opened %q, want the dashboard opened once", urls)
	}
	if !strings.HasPrefix(urls[0], f.url("/login?code=")) {
		t.Fatalf("opened %q, want the server's login URL", urls[0])
	}
	if want := (launch.Browser{Pref: "brave", AppWindow: true}); browsers[0] != want {
		t.Errorf("opened in %+v, want the configured %+v", browsers[0], want)
	}
	signIn(t, urls[0])
}

func TestOpen_StartsServerWhenDown(t *testing.T) {
	f := newOpenFixture(t)
	start := func(string, ...string) error {
		f.start()
		return nil
	}

	got := runOpen(t.Context(), f.options(start))

	if got.code != 0 || got.stdout != "" || got.stderr != "" {
		t.Fatalf("open = %d, stdout %q, stderr %q; want 0 and nothing written", got.code, got.stdout, got.stderr)
	}
	// --no-open, so that the server does not open a second window.
	if want := []string{testExe + " serve --no-open"}; !slices.Equal(f.spawns.started(), want) {
		t.Errorf("open started %q, want %q", f.spawns.started(), want)
	}
	f.assertOpenedSignedIn(t)
}

func TestOpen_UsesTheRunningServer(t *testing.T) {
	f := newOpenFixture(t)
	f.start()

	got := runOpen(t.Context(), f.options(func(string, ...string) error { return nil }))

	if got.code != 0 || got.stderr != "" {
		t.Fatalf("open = %d, stderr %q; want 0", got.code, got.stderr)
	}
	if started := f.spawns.started(); len(started) != 0 {
		t.Errorf("open started %q with a server running, want nothing started", started)
	}
	f.assertOpenedSignedIn(t)
}

func TestOpen_FailsWhenTheServerDoesNotAnswerWithin3s(t *testing.T) {
	f := newOpenFixture(t)
	o := f.options(func(string, ...string) error { return nil })
	o.After = f.clock.AfterFunc
	done := make(chan openResult, 1)
	go func() { done <- runOpen(t.Context(), o) }()

	f.waitArmed(serverStartWait)
	f.clock.Advance(serverStartWait)

	var got openResult
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("open went on waiting after its 3 s")
	}
	if got.code != 1 || !strings.Contains(got.stderr, fmt.Sprintf("port %d", f.port)) || !strings.Contains(got.stderr, "3s") ||
		strings.Count(got.stderr, "run loadline serve to see why") != 1 {
		t.Errorf("open = %d, stderr %q; want 1, the port and wait named, and what to run", got.code, got.stderr)
	}
	if n := f.opens.count(); n != 0 {
		t.Errorf("open opened %d browsers without a server, want none", n)
	}
}

func TestOpen_FailsWhenTheServerCannotStart(t *testing.T) {
	f := newOpenFixture(t)

	got := runOpen(t.Context(), f.options(func(string, ...string) error { return errors.New("no such file") }))

	if got.code != 1 || !strings.Contains(got.stderr, "start the server: no such file") {
		t.Errorf("open = %d, stderr %q; want 1 and the failed start", got.code, got.stderr)
	}
	if n := f.opens.count(); n != 0 {
		t.Errorf("open opened %d browsers without a server, want none", n)
	}
}

func TestOpen_ReportsABrowserThatFails(t *testing.T) {
	f := newOpenFixture(t)
	f.start()
	f.opens.fail = errors.New(`browser "opera" is not known`)

	got := runOpen(t.Context(), f.options(func(string, ...string) error { return nil }))

	if got.code != 1 || !strings.Contains(got.stderr, `open the dashboard: browser "opera" is not known`) {
		t.Errorf("open = %d, stderr %q; want 1 and the browser's failure", got.code, got.stderr)
	}
}

func TestOpen_RefusesAnInvalidConfig(t *testing.T) {
	f := newOpenFixture(t)
	f.writeConfig(`{"browser":" "}`)

	got := runOpen(t.Context(), f.options(func(string, ...string) error {
		t.Error("open started a server with an invalid config")
		return nil
	}))

	if got.code != 1 || !strings.Contains(got.stderr, "browser") {
		t.Errorf("open = %d, stderr %q; want 1 and the config error", got.code, got.stderr)
	}
}

func TestRun_OpenTakesNoArguments(t *testing.T) {
	for _, args := range [][]string{{"open", "now"}, {"open", "--browser", "brave"}} {
		if got := run(t, args...); got.code != 2 || got.stdout != "" || !strings.Contains(got.stderr, "loadline open") {
			t.Errorf("%q = %d, stdout %q, stderr %q; want open's usage error", args, got.code, got.stdout, got.stderr)
		}
	}
}

func TestRun_HelpListsOpen(t *testing.T) {
	if got := run(t, "help"); !strings.Contains(got.stdout, "\n  open ") {
		t.Errorf("usage %q does not list open", got.stdout)
	}
}
