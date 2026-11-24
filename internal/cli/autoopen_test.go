package cli_test

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/launch"
)

// openDelay is how long a starting server gives an open dashboard to
// reconnect before it opens one.
const openDelay = 3 * time.Second

var boot = time.Date(2026, time.November, 21, 8, 0, 0, 0, time.UTC)

// browserOpens records the dashboards the code under test opens.
type browserOpens struct {
	// fail, when set, is the error every open returns.
	fail error

	mu       sync.Mutex
	urls     []string
	browsers []launch.Browser
}

func (b *browserOpens) open(url string, browser launch.Browser) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.urls = append(b.urls, url)
	b.browsers = append(b.browsers, browser)
	return b.fail
}

func (b *browserOpens) opened() ([]string, []launch.Browser) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.urls...), append([]launch.Browser(nil), b.browsers...)
}

func (b *browserOpens) count() int {
	urls, _ := b.opened()
	return len(urls)
}

// opening lets the server open the dashboard, into opens, on a machine that
// booted at bootedAt.
func opening(opens *browserOpens, bootedAt time.Time) func(*cli.ServeOptions) {
	return func(o *cli.ServeOptions) {
		o.NoOpen = false
		o.Open = opens.open
		o.BootTime = func() (time.Time, error) { return bootedAt, nil }
	}
}

// passOpenDelay waits until the server has started its wait to open the
// dashboard, and lets that time pass.
func (f *serveFixture) passOpenDelay() {
	f.t.Helper()
	f.waitArmed(openDelay)
	f.clock.Advance(openDelay)
}

func (f *serveFixture) waitLog(want string) {
	f.t.Helper()
	waitFor(f.t, "the log to say "+want, func() bool { return strings.Contains(f.logText(), want) })
}

func (f *serveFixture) stampPath() string { return filepath.Join(f.paths.DataDir, "opened.stamp") }

func (f *serveFixture) writeStamp(bootedAt time.Time) {
	f.t.Helper()
	if err := os.MkdirAll(f.paths.DataDir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.stampPath(), []byte(strconv.FormatInt(bootedAt.Unix(), 10)+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// stamp returns what the stamp says, or "" when there is none.
func (f *serveFixture) stamp() string {
	f.t.Helper()
	data, err := os.ReadFile(f.stampPath())
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		f.t.Fatalf("read the stamp: %v", err)
	}
	return string(data)
}

// signIn opens loginURL as a browser would, and fails unless it signs in:
// a redirect to the dashboard with its cookie.
func signIn(t *testing.T, loginURL string) {
	t.Helper()
	client := &http.Client{
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, loginURL, http.NoBody)
	if err != nil {
		t.Fatalf("new request for %s: %v", loginURL, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", loginURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	signedIn := false
	for _, cookie := range resp.Cookies() {
		signedIn = signedIn || cookie.Name == "loadline_ui" && cookie.Value != ""
	}
	if resp.StatusCode != http.StatusSeeOther || !signedIn {
		t.Errorf("GET %s = %d with cookies %v, want a redirect that signs the browser in", loginURL, resp.StatusCode, resp.Cookies())
	}
}

func TestServe_AutoOpenSkippedWhenClientReconnects(t *testing.T) {
	t.Run("a dashboard reconnects within 3 s", func(t *testing.T) {
		f := newServeFixture(t)
		opens := &browserOpens{}
		f.start(opening(opens, boot))
		stream := f.get("/api/events")
		defer func() { _ = stream.Body.Close() }()
		if line, err := bufio.NewReader(stream.Body).ReadString('\n'); err != nil || line != "event: view\n" {
			t.Fatalf("stream began %q, %v; want a view", line, err)
		}

		f.passOpenDelay()

		f.waitLog(`msg="dashboard not opened" reason="a dashboard is connected"`)
		if urls, _ := opens.opened(); len(urls) != 0 {
			t.Errorf("the server opened %q with a dashboard connected, want nothing opened", urls)
		}
	})

	t.Run("no dashboard connects", func(t *testing.T) {
		f := newServeFixture(t)
		browser := f.chromiumAppWindow()
		opens := &browserOpens{}
		r := f.start(opening(opens, boot))

		f.passOpenDelay()

		waitFor(t, "the dashboard to open", func() bool { return opens.count() > 0 })
		urls, browsers := opens.opened()
		if !strings.HasPrefix(urls[0], f.url("/login?code=")) {
			t.Fatalf("the server opened %q, want its login URL", urls[0])
		}
		signIn(t, urls[0])
		if want := (launch.Browser{Pref: browser, AppWindow: true, ProfileDir: f.paths.BrowserProfileDir}); browsers[0] != want {
			t.Errorf("the server opened %+v, want the configured %+v", browsers[0], want)
		}
		r.stop(t)
		if n := opens.count(); n != 1 {
			t.Errorf("the server opened the dashboard %d times, want once", n)
		}
		code := strings.TrimPrefix(urls[0], f.url("/login?code="))
		if log := f.logText(); !strings.Contains(log, `msg="dashboard opened"`) || strings.Contains(log, code) {
			t.Errorf("the log should note the open and never hold its login code:\n%s", log)
		}
	})
}

// chromiumAppWindow configures the server to open the dashboard in a
// Chromium-family browser given by its path, which is opened as it is
// whichever browsers are installed here, in an app window, and returns
// the browser's path.
func (f *serveFixture) chromiumAppWindow() string {
	f.t.Helper()
	browser := filepath.Join(f.t.TempDir(), "Chromium", "chrome.exe")
	f.writeConfig(fmt.Sprintf(`{"browser":%q,"appWindow":true}`, browser))
	return browser
}

func TestServe_AutoOpenUsesTheDashboardProfile(t *testing.T) {
	f := newServeFixture(t)
	browser := f.chromiumAppWindow()
	opens := &browserOpens{}
	r := f.start(opening(opens, boot))

	f.passOpenDelay()

	waitFor(t, "the dashboard to open", func() bool { return opens.count() > 0 })
	r.stop(t)
	_, browsers := opens.opened()
	if want := (launch.Browser{Pref: browser, AppWindow: true, ProfileDir: f.paths.BrowserProfileDir}); browsers[0] != want {
		t.Errorf("the server opened %+v, want %+v", browsers[0], want)
	}
	assertPrivateProfile(t, f.paths.BrowserProfileDir)
}

// The profile is made as the dashboard opens, so a server that opens none,
// as one that loadline open or install starts, makes none.
func TestServe_NoOpenMakesNoProfile(t *testing.T) {
	f := newServeFixture(t)
	f.chromiumAppWindow()

	f.start().stop(t)

	assertNoProfile(t, f.paths.BrowserProfileDir)
}

// A profile that the server cannot use by the time it opens the dashboard
// never keeps it from opening: it opens in the browser's default profile,
// and the log says once why.
func TestServe_AutoOpenFallsBackToTheDefaultProfileWhenTheProfileIsRefused(t *testing.T) {
	f := newServeFixture(t)
	browser := f.chromiumAppWindow()
	opens := &browserOpens{}
	r := f.start(opening(opens, boot))
	// Once the server has started, which refuses such a profile, and before
	// it opens the dashboard.
	plantProfileNotMadePrivate(t, f.paths.BrowserProfileDir)

	f.passOpenDelay()

	waitFor(t, "the dashboard to open", func() bool { return opens.count() > 0 })
	r.stop(t)
	_, browsers := opens.opened()
	if want := (launch.Browser{Pref: browser, AppWindow: true}); browsers[0] != want {
		t.Errorf("the server opened %+v, want %+v", browsers[0], want)
	}
	const refusal = "is not private, so the dashboard's browser profile there cannot be trusted"
	warnings := 0
	for line := range strings.Lines(f.logText()) {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "default profile") && strings.Contains(line, refusal) {
			warnings++
		}
	}
	if warnings != 1 || strings.Count(f.logText(), refusal) != 1 {
		t.Errorf("log:\n%s\nwant one warning that the dashboard opens in the default profile because the profile %s", f.logText(), refusal)
	}
}

func TestServe_OpensOncePerBoot(t *testing.T) {
	f := newServeFixture(t)
	opens := &browserOpens{}
	first := f.start(opening(opens, boot))
	f.passOpenDelay()
	waitFor(t, "the first start to open the dashboard", func() bool { return opens.count() == 1 })
	first.stop(t)
	if got, want := f.stamp(), strconv.FormatInt(boot.Unix(), 10)+"\n"; got != want {
		t.Errorf("stamp = %q, want the boot time %q", got, want)
	}

	// The same boot, as a boot time derived from the uptime may read it.
	second := f.start(opening(opens, boot.Add(30*time.Second)))
	f.passOpenDelay()
	f.waitLog(`msg="dashboard not opened" reason="opened earlier this boot"`)
	second.stop(t)
	if n := opens.count(); n != 1 {
		t.Errorf("a second start in the same boot opened the dashboard; opens = %d, want 1", n)
	}

	f.start(opening(opens, boot.Add(24*time.Hour)))
	f.passOpenDelay()
	waitFor(t, "the first start after a reboot to open the dashboard", func() bool { return opens.count() == 2 })
}

func TestServe_AFailedOpenLeavesNoStamp(t *testing.T) {
	f := newServeFixture(t)
	opens := &browserOpens{fail: errors.New("no browser here")}
	r := f.start(opening(opens, boot))

	f.passOpenDelay()

	f.waitLog(`msg="open the dashboard failed" err="no browser here"`)
	r.stop(t)
	if got := f.stamp(); got != "" {
		t.Errorf("stamp = %q after a failed open, want none, so that the next start tries again", got)
	}
}

func TestServe_OpensWhenTheBootTimeIsUnknown(t *testing.T) {
	f := newServeFixture(t)
	f.writeStamp(boot)
	opens := &browserOpens{}
	r := f.start(opening(opens, boot), func(o *cli.ServeOptions) {
		o.BootTime = func() (time.Time, error) { return time.Time{}, errors.New("no uptime") }
	})

	f.passOpenDelay()

	waitFor(t, "the dashboard to open", func() bool { return opens.count() == 1 })
	r.stop(t)
	if got, want := f.stamp(), strconv.FormatInt(boot.Unix(), 10)+"\n"; got != want {
		t.Errorf("stamp = %q, want it left as it was, %q", got, want)
	}
}

func TestServe_DemoOpensAndLeavesTheStampAlone(t *testing.T) {
	f := newServeFixture(t)
	f.writeStamp(boot)
	opens := &browserOpens{}
	// The same boot: a demo that went by the stamp would not open, and one
	// that wrote it would record this reading.
	r := f.start(opening(opens, boot.Add(30*time.Second)), func(o *cli.ServeOptions) { o.Demo = true })

	f.passOpenDelay()

	waitFor(t, "the demo to open", func() bool { return opens.count() == 1 })
	urls, _ := opens.opened()
	signIn(t, urls[0])
	r.stop(t)
	if got, want := f.stamp(), strconv.FormatInt(boot.Unix(), 10)+"\n"; got != want {
		t.Errorf("stamp = %q after the demo opened, want the real server's %q", got, want)
	}
}
