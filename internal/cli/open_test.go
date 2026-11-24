package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/launch"
)

// serverStartWait is how long open waits for a server it started to answer.
const serverStartWait = 3 * time.Second

// openFixture is a loadline open run against the server of a serveFixture,
// both with the files Loadline keeps in LOADLINE_HOME, configured for a
// Chromium-family browser, given by its path, in an app window.
type openFixture struct {
	*serveFixture
	opens  *browserOpens
	spawns *spawner
	// browser is the path of the configured browser. A browser given by its
	// path is opened as it is, whichever browsers are installed here.
	browser string
}

func newOpenFixture(t *testing.T) *openFixture {
	t.Helper()
	f := &openFixture{
		serveFixture: newServeFixture(t),
		opens:        &browserOpens{},
		spawns:       &spawner{},
		browser:      filepath.Join(t.TempDir(), "Chromium", "chrome.exe"),
	}
	f.paths = loadlineHome(t)
	f.configureBrowser(f.browser, "")
	return f
}

// loadlineHome points LOADLINE_HOME at a new folder, and returns the paths
// Loadline finds its files at there.
func loadlineHome(t *testing.T) config.Paths {
	t.Helper()
	t.Setenv("LOADLINE_HOME", t.TempDir())
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("find the paths in LOADLINE_HOME: %v", err)
	}
	return paths
}

// configureBrowser writes a config that has the fixture's server open the
// dashboard in browser, in an app window, with the keys in more besides.
func (f *openFixture) configureBrowser(browser, more string) {
	f.t.Helper()
	f.writeConfig(fmt.Sprintf(`{"port":%d,"browser":%q,"appWindow":true%s}`, f.port, browser, more))
}

// inOwnProfile is the configured browser, opening the dashboard in its own
// profile.
func (f *openFixture) inOwnProfile() launch.Browser {
	return launch.Browser{Pref: f.browser, AppWindow: true, ProfileDir: f.paths.BrowserProfileDir}
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

// assertOpenedSignedIn checks that want was opened once, at a login URL
// that signs it in to the server.
func (f *openFixture) assertOpenedSignedIn(t *testing.T, want launch.Browser) {
	t.Helper()
	urls, browsers := f.opens.opened()
	if len(urls) != 1 {
		t.Fatalf("opened %q, want the dashboard opened once", urls)
	}
	if !strings.HasPrefix(urls[0], f.url("/login?code=")) {
		t.Fatalf("opened %q, want the server's login URL", urls[0])
	}
	if browsers[0] != want {
		t.Errorf("opened in %+v, want %+v", browsers[0], want)
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
	f.assertOpenedSignedIn(t, f.inOwnProfile())
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
	f.assertOpenedSignedIn(t, f.inOwnProfile())
}

func TestOpen_OpensTheDashboardInItsOwnProfile(t *testing.T) {
	f := newOpenFixture(t)
	f.start()

	got := runOpen(t.Context(), f.options(func(string, ...string) error { return nil }))

	if got.code != 0 || got.stderr != "" {
		t.Fatalf("open = %d, stderr %q; want 0", got.code, got.stderr)
	}
	profile := filepath.Join(os.Getenv("LOADLINE_HOME"), "browser")
	f.assertOpenedSignedIn(t, launch.Browser{Pref: f.browser, AppWindow: true, ProfileDir: profile})
	assertPrivateProfile(t, profile)
}

func TestOpen_UsesTheDefaultProfileWhenTheConfigSaysSo(t *testing.T) {
	f := newOpenFixture(t)
	f.configureBrowser(f.browser, `,"browserProfile":"default"`)
	f.start()

	got := runOpen(t.Context(), f.options(func(string, ...string) error { return nil }))

	if got.code != 0 || got.stderr != "" {
		t.Fatalf("open = %d, stderr %q; want 0", got.code, got.stderr)
	}
	f.assertOpenedSignedIn(t, launch.Browser{Pref: f.browser, AppWindow: true})
	assertNoProfile(t, f.paths.BrowserProfileDir)
}

// Firefox has no app window, so it opens the dashboard in the profile it
// runs, and the dashboard's own would be a folder for nothing. Whether
// Firefox is installed here or not, which gives way to the system default,
// no profile is made.
func TestOpen_MakesNoProfileForFirefox(t *testing.T) {
	for name, browser := range map[string]string{
		"by name": "firefox",
		"by path": filepath.Join(t.TempDir(), "Mozilla Firefox", "firefox.exe"),
	} {
		t.Run(name, func(t *testing.T) {
			f := newOpenFixture(t)
			f.configureBrowser(browser, "")
			f.start()

			got := runOpen(t.Context(), f.options(func(string, ...string) error { return nil }))

			if got.code != 0 || got.stderr != "" {
				t.Fatalf("open = %d, stderr %q; want 0", got.code, got.stderr)
			}
			f.assertOpenedSignedIn(t, launch.Browser{Pref: browser, AppWindow: true})
			assertNoProfile(t, f.paths.BrowserProfileDir)
		})
	}
}

// A profile Loadline cannot use never keeps the dashboard from opening: it
// opens in the browser's default profile, as it did before it had one of
// its own, and open says once why.
func TestOpen_FallsBackToTheDefaultProfileWhenTheProfileIsRefused(t *testing.T) {
	f := newOpenFixture(t)
	f.start()
	plantProfileNotMadePrivate(t, f.paths.BrowserProfileDir)

	got := runOpen(t.Context(), f.options(func(string, ...string) error { return nil }))

	assertWarnedOnce(t, got, f.paths.BrowserProfileDir+" is not private, so the dashboard's browser profile there cannot be trusted")
	f.assertOpenedSignedIn(t, launch.Browser{Pref: f.browser, AppWindow: true})
}

// The browser keeps the dashboard's sign-in in its profile, which is in the
// data directory, so a data directory that Loadline refuses gets no
// profile, even where the profile would pass by itself.
func TestOpen_FallsBackToTheDefaultProfileWhenTheDataDirectoryIsRefused(t *testing.T) {
	f := newOpenFixture(t)
	f.start()
	if err := os.WriteFile(filepath.Join(f.paths.DataDir, "notes.txt"), nil, 0o600); err != nil {
		t.Fatalf("plant a file that is not Loadline's: %v", err)
	}

	got := runOpen(t.Context(), f.options(func(string, ...string) error { return nil }))

	assertWarnedOnce(t, got, f.paths.DataDir+" holds notes.txt, which is not Loadline's")
	f.assertOpenedSignedIn(t, launch.Browser{Pref: f.browser, AppWindow: true})
	assertNoProfile(t, f.paths.BrowserProfileDir)
}

// assertWarnedOnce checks that open succeeded, and said in one line that
// the dashboard opens in the browser's default profile, and why.
func assertWarnedOnce(t *testing.T, got openResult, why string) {
	t.Helper()
	if got.code != 0 || strings.Count(got.stderr, "\n") != 1 || !strings.Contains(got.stderr, "default profile") ||
		strings.Count(got.stderr, why) != 1 {
		t.Errorf("open = %d, stderr %q; want 0, and one line that says the dashboard opens in the default profile because %q",
			got.code, got.stderr, why)
	}
}

// plantProfileNotMadePrivate puts a folder that Loadline did not make
// private, as one copied or restored from elsewhere is, where the
// dashboard's profile goes.
func plantProfileNotMadePrivate(t *testing.T, profile string) {
	t.Helper()
	if err := os.Mkdir(profile, 0o755); err != nil {
		t.Fatalf("plant the profile: %v", err)
	}
	// Past whatever the umask took off. On Windows the folder inherits the
	// data directory's list instead of having one of its own.
	if err := os.Chmod(profile, 0o755); err != nil {
		t.Fatalf("open the profile to others: %v", err)
	}
}

// assertPrivateProfile checks that the dashboard's profile is there, and
// private as Loadline makes it.
func assertPrivateProfile(t *testing.T, profile string) {
	t.Helper()
	if private, err := fsperm.DirPrivate(profile); err != nil || !private {
		t.Errorf("the dashboard's profile %s: private %v, %v; want a private folder", profile, private, err)
	}
}

// assertNoProfile checks that nothing was made where the dashboard's
// profile goes.
func assertNoProfile(t *testing.T, profile string) {
	t.Helper()
	if _, err := os.Lstat(profile); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat(%s) = %v, want no profile made", profile, err)
	}
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
