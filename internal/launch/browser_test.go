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
// browser untouched as a single argument.
const dashboardURL = "http://127.0.0.1:8289/login?code=0123abcd&next=/ a"

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
