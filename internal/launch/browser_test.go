package launch_test

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/launch"
)

// dashboardURL has what a shell would split or act on, which must reach the
// browser untouched as a single argument. Its port is one that browsers
// refuse to fetch from: a test that opened a real browser by mistake reaches
// no server, least of all the user's own on the default port.
const dashboardURL = "http://127.0.0.1:1/login?code=0123abcd&next=/ a"

// installedAt finds each browser named in paths, and no other.
func installedAt(paths map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		path, ok := paths[name]
		return path, ok
	}
}

func nothingInstalled(string) (string, bool) { return "", false }

type command struct {
	name string
	args []string
}

func commandFor(t *testing.T, b launch.Browser, find func(string) (string, bool)) command {
	t.Helper()
	name, args, err := launch.Command(dashboardURL, b, find)
	if err != nil {
		t.Fatalf("Command(%+v) failed: %v", b, err)
	}
	return command{name: name, args: args}
}

func assertCommand(t *testing.T, got, want command) {
	t.Helper()
	if got.name != want.name || !slices.Equal(got.args, want.args) {
		t.Errorf("command = %q %q, want %q %q", got.name, got.args, want.name, want.args)
	}
}

func TestCommand_BraveAppWindow(t *testing.T) {
	brave := filepath.Join("Brave-Browser", "Application", "brave.exe")

	got := commandFor(t, launch.Browser{Pref: "brave", AppWindow: true}, installedAt(map[string]string{"brave": brave}))

	assertCommand(t, got, command{name: brave, args: []string{"--app=" + dashboardURL}})
}

func TestCommand_ChromiumFamily(t *testing.T) {
	paths := map[string]string{"brave": "/opt/brave", "chrome": "/opt/chrome", "edge": "/opt/edge"}
	for _, pref := range []string{"brave", "chrome", "edge"} {
		t.Run(pref, func(t *testing.T) {
			appWindow := commandFor(t, launch.Browser{Pref: pref, AppWindow: true}, installedAt(paths))
			tab := commandFor(t, launch.Browser{Pref: pref}, installedAt(paths))

			assertCommand(t, appWindow, command{name: paths[pref], args: []string{"--app=" + dashboardURL}})
			assertCommand(t, tab, command{name: paths[pref], args: []string{dashboardURL}})
		})
	}
}

func TestCommand_FirefoxHasNoAppWindow(t *testing.T) {
	find := installedAt(map[string]string{"firefox": "/opt/firefox"})

	for _, appWindow := range []bool{true, false} {
		got := commandFor(t, launch.Browser{Pref: "firefox", AppWindow: appWindow}, find)

		assertCommand(t, got, command{name: "/opt/firefox", args: []string{dashboardURL}})
	}
}

func TestCommand_AutoPerOS(t *testing.T) {
	name, args := autoCommand(dashboardURL)

	for _, appWindow := range []bool{true, false} {
		got := commandFor(t, launch.Browser{Pref: "auto", AppWindow: appWindow}, func(name string) (string, bool) {
			t.Errorf("auto looked for %q, want the system default without a search", name)
			return "", false
		})

		assertCommand(t, got, command{name: name, args: args})
	}
}

func TestCommand_AbsolutePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Portable Browser", "browser.exe")
	refuseSearch := func(name string) (string, bool) {
		t.Errorf("a path looked for %q, want the path used as is", name)
		return "", false
	}

	appWindow := commandFor(t, launch.Browser{Pref: path, AppWindow: true}, refuseSearch)
	tab := commandFor(t, launch.Browser{Pref: path}, refuseSearch)

	assertCommand(t, appWindow, command{name: path, args: []string{"--app=" + dashboardURL}})
	assertCommand(t, tab, command{name: path, args: []string{dashboardURL}})
}

// Firefox has no app mode, and AppWindow is on by default, so a Firefox
// given by path, as a portable or Scoop install is, must never get --app=.
func TestCommand_FirefoxByPathHasNoAppWindow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Mozilla Firefox")
	for _, base := range []string{"firefox.exe", "FireFox.EXE", "firefox"} {
		t.Run(base, func(t *testing.T) {
			path := filepath.Join(dir, base)

			got := commandFor(t, launch.Browser{Pref: path, AppWindow: true}, nothingInstalled)

			assertCommand(t, got, command{name: path, args: []string{dashboardURL}})
		})
	}
}

func TestCommand_ChromiumMissingFallsBackToAuto(t *testing.T) {
	name, args := autoCommand(dashboardURL)
	for _, pref := range []string{"brave", "chrome", "edge", "firefox"} {
		t.Run(pref, func(t *testing.T) {
			got := commandFor(t, launch.Browser{Pref: pref, AppWindow: true}, nothingInstalled)

			assertCommand(t, got, command{name: name, args: args})
		})
	}
}

func TestCommand_NamesIgnoreCase(t *testing.T) {
	got := commandFor(t, launch.Browser{Pref: "Brave", AppWindow: true}, installedAt(map[string]string{"brave": "/opt/brave"}))

	assertCommand(t, got, command{name: "/opt/brave", args: []string{"--app=" + dashboardURL}})
}

// dashboardProfile is the dashboard's own profile folder, in a user folder
// whose name has a space, as many have.
const dashboardProfile = "C:/Users/John Smith/AppData/Local/loadline/browser"

// inDashboardProfile is what a Chromium app window opens url with in
// profile, in this order and each flag an argument of its own: the profile,
// with the pages a new profile shows first turned off, then with extensions
// and the browser's default apps kept out of it, and then the URL.
func inDashboardProfile(profile, url string) []string {
	return []string{
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-default-apps",
		"--app=" + url,
	}
}

// chromiumCase is a Chromium-family browser as a config names it, and the
// program that runs it.
type chromiumCase struct {
	name, pref, program string
}

// chromiumCases are the Chromium-family browsers, each by name and
// installed, and one given by its path, with what finds those installed.
func chromiumCases(t *testing.T) ([]chromiumCase, func(string) (string, bool)) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Chromium", "chrome.exe")
	installed := map[string]string{"brave": "/opt/brave", "chrome": "/opt/chrome", "edge": "/opt/edge"}
	return []chromiumCase{
		{name: "brave", pref: "brave", program: "/opt/brave"},
		{name: "chrome", pref: "chrome", program: "/opt/chrome"},
		{name: "edge", pref: "edge", program: "/opt/edge"},
		{name: "by path", pref: path, program: path},
	}, installedAt(installed)
}

func TestCommand_ChromiumAppWindowOpensInTheDashboardProfile(t *testing.T) {
	cases, find := chromiumCases(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commandFor(t, launch.Browser{Pref: tc.pref, AppWindow: true, ProfileDir: dashboardProfile}, find)

			assertCommand(t, got, command{name: tc.program, args: inDashboardProfile(dashboardProfile, dashboardURL)})
		})
	}
}

// Without a profile folder, an app window opens exactly as it did before
// the dashboard had a profile of its own: in the browser's default profile.
func TestCommand_DefaultProfileOptOut(t *testing.T) {
	cases, find := chromiumCases(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commandFor(t, launch.Browser{Pref: tc.pref, AppWindow: true}, find)

			assertCommand(t, got, command{name: tc.program, args: []string{"--app=" + dashboardURL}})
		})
	}
}

// A tab opens in the browser the user already has open, in the profile it
// runs, so the profile folder is for an app window alone.
func TestCommand_ATabIgnoresTheProfile(t *testing.T) {
	cases, find := chromiumCases(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commandFor(t, launch.Browser{Pref: tc.pref, ProfileDir: dashboardProfile}, find)

			assertCommand(t, got, command{name: tc.program, args: []string{dashboardURL}})
		})
	}
}

func TestCommand_FirefoxAndTheDefaultBrowserIgnoreTheProfile(t *testing.T) {
	firefoxPath := filepath.Join(t.TempDir(), "Mozilla Firefox", "firefox.exe")
	autoName, autoArgs := autoCommand(dashboardURL)
	for _, tc := range []struct {
		name, pref string
		find       func(string) (string, bool)
		want       command
	}{
		{
			name: "firefox", pref: "firefox",
			find: installedAt(map[string]string{"firefox": "/opt/firefox"}),
			want: command{name: "/opt/firefox", args: []string{dashboardURL}},
		},
		{name: "firefox by path", pref: firefoxPath, find: nothingInstalled, want: command{name: firefoxPath, args: []string{dashboardURL}}},
		{name: "auto", pref: "auto", find: nothingInstalled, want: command{name: autoName, args: autoArgs}},
		{name: "a missing browser", pref: "brave", find: nothingInstalled, want: command{name: autoName, args: autoArgs}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := commandFor(t, launch.Browser{Pref: tc.pref, AppWindow: true, ProfileDir: dashboardProfile}, tc.find)

			assertCommand(t, got, tc.want)
		})
	}
}

// The profile folder reaches a browser executable as one argument, whatever
// its path holds: a space, a quote and an ampersand, what cmd, PowerShell
// and sh expand or escape (% $ ! ^ `), all of which a shell would act on, or
// letters outside ASCII.
func TestCommand_ProfileDirIsOneArgument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Chromium", "chrome.exe")
	for _, profile := range []string{
		"C:/Users/John Smith/AppData/Local/loadline/browser",
		filepath.Join(t.TempDir(), "O'Brien & Sons", "loadline", "browser"),
		filepath.Join(t.TempDir(), "100% $HOME !x ^y `z", "browser"),
		filepath.Join(t.TempDir(), "Zoë", "東京", "browser"),
	} {
		t.Run(profile, func(t *testing.T) {
			got := commandFor(t, launch.Browser{Pref: path, AppWindow: true, ProfileDir: profile}, nothingInstalled)

			assertCommand(t, got, command{name: path, args: inDashboardProfile(profile, dashboardURL)})
		})
	}
}

// Windows runs a batch file through cmd.exe, which reads its command line
// again, so a batch file given as the browser gets the arguments it got
// before the dashboard had a profile of its own, whatever the profile folder
// is: a profile path may hold an & or ^ that cmd.exe would act on. Dots and
// blanks after the name are dropped, as Windows drops them.
func TestCommand_ABatchFileGetsNoProfile(t *testing.T) {
	for _, name := range []string{"browser.cmd", "Browser.BAT", "launch.Cmd", "browser.cmd.", "browser.bat . "} {
		path := filepath.Join(t.TempDir(), "launchers", name)
		t.Run(name, func(t *testing.T) {
			appWindow := commandFor(t, launch.Browser{Pref: path, AppWindow: true, ProfileDir: dashboardProfile}, nothingInstalled)
			tab := commandFor(t, launch.Browser{Pref: path, ProfileDir: dashboardProfile}, nothingInstalled)

			assertCommand(t, appWindow, command{name: path, args: []string{"--app=" + dashboardURL}})
			assertCommand(t, tab, command{name: path, args: []string{dashboardURL}})
		})
	}
}

// TakesProfile answers for the browsers installed here, so only browsers
// given by path, and the system default, give the same answer on every
// machine; the named browsers are TestTakesProfile's, inside the package.
func TestTakesProfile_ByPath(t *testing.T) {
	chromium := filepath.Join(t.TempDir(), "Chromium", "chrome.exe")
	firefox := filepath.Join(t.TempDir(), "Mozilla Firefox", "firefox.exe")
	for _, tc := range []struct {
		name    string
		browser launch.Browser
		want    bool
	}{
		{name: "a Chromium app window", browser: launch.Browser{Pref: chromium, AppWindow: true}, want: true},
		{name: "a Chromium tab", browser: launch.Browser{Pref: chromium}},
		{name: "a batch file's app window", browser: launch.Browser{Pref: filepath.Join(t.TempDir(), "browser.CMD"), AppWindow: true}},
		{name: "firefox", browser: launch.Browser{Pref: firefox, AppWindow: true}},
		{name: "auto", browser: launch.Browser{Pref: "auto", AppWindow: true}},
		{name: "an unknown browser", browser: launch.Browser{Pref: "opera", AppWindow: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := launch.TakesProfile(tc.browser); got != tc.want {
				t.Errorf("TakesProfile(%+v) = %v, want %v", tc.browser, got, tc.want)
			}
		})
	}
}

func TestCommand_RefusesAnUnknownBrowser(t *testing.T) {
	relative := []string{"." + string(filepath.Separator) + "brave", filepath.Join("bin", "brave")}
	for _, pref := range append([]string{"opera", "brave.exe"}, relative...) {
		t.Run(pref, func(t *testing.T) {
			name, args, err := launch.Command(dashboardURL, launch.Browser{Pref: pref}, func(string) (string, bool) {
				t.Error("an unknown browser was looked for")
				return "", false
			})

			if err == nil {
				t.Fatalf("Command(%q) = %q %q, want an error", pref, name, args)
			}
			for _, accepted := range []string{fmt.Sprintf("%q", pref), `"auto"`, `"brave"`, `"chrome"`, `"edge"`, `"firefox"`, "absolute path"} {
				if !strings.Contains(err.Error(), accepted) {
					t.Errorf("error %q does not name %s", err, accepted)
				}
			}
		})
	}
}
