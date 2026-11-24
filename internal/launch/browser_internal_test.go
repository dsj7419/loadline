package launch

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// foundAt finds each browser named in paths, and no other.
func foundAt(paths map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		path, ok := paths[name]
		return path, ok
	}
}

// everyBrowserAt has each browser Loadline knows by name installed.
var everyBrowserAt = map[string]string{
	"brave": "/opt/brave", "chrome": "/opt/chrome", "edge": "/opt/edge", "firefox": "/opt/firefox",
}

func TestResolve(t *testing.T) {
	chromiumPath := filepath.Join(t.TempDir(), "Chromium", "chrome.exe")
	firefoxPath := filepath.Join(t.TempDir(), "Mozilla Firefox", "FireFox.EXE")
	batchPath := filepath.Join(t.TempDir(), "launchers", "Brave.Cmd")
	for _, tc := range []struct {
		name, pref  string
		installed   map[string]string
		wantProgram string
		wantKind    browserKind
	}{
		{name: "brave", pref: "brave", installed: everyBrowserAt, wantProgram: "/opt/brave", wantKind: chromiumKind},
		{name: "chrome", pref: "chrome", installed: everyBrowserAt, wantProgram: "/opt/chrome", wantKind: chromiumKind},
		{name: "edge", pref: "edge", installed: everyBrowserAt, wantProgram: "/opt/edge", wantKind: chromiumKind},
		{name: "a name in another case", pref: "Edge", installed: everyBrowserAt, wantProgram: "/opt/edge", wantKind: chromiumKind},
		{name: "firefox", pref: "firefox", installed: everyBrowserAt, wantProgram: "/opt/firefox", wantKind: firefoxKind},
		{name: "a Chromium path", pref: chromiumPath, wantProgram: chromiumPath, wantKind: chromiumKind},
		{name: "a firefox path", pref: firefoxPath, wantProgram: firefoxPath, wantKind: firefoxKind},
		{name: "a batch file's path", pref: batchPath, wantProgram: batchPath, wantKind: batchFileKind},
		{
			name: "a Chromium browser installed as a batch file", pref: "brave",
			installed: map[string]string{"brave": "/opt/brave.BAT"}, wantProgram: "/opt/brave.BAT", wantKind: batchFileKind,
		},
		{
			// Firefox takes the URL alone, so a batch file has nothing to
			// leave out for it.
			name: "firefox installed as a batch file", pref: "firefox",
			installed: map[string]string{"firefox": "/opt/firefox.cmd"}, wantProgram: "/opt/firefox.cmd", wantKind: firefoxKind,
		},
		{name: "auto", pref: "auto", installed: everyBrowserAt, wantKind: systemDefault},
		{name: "a missing Chromium browser", pref: "brave", installed: map[string]string{"firefox": "/opt/firefox"}, wantKind: systemDefault},
		{name: "a missing firefox", pref: "firefox", installed: map[string]string{"brave": "/opt/brave"}, wantKind: systemDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			find := func(name string) (string, bool) {
				if filepath.IsAbs(tc.pref) || tc.pref == "auto" {
					t.Errorf("resolving %q looked for %q, want no search", tc.pref, name)
				}
				return foundAt(tc.installed)(name)
			}

			program, kind := resolve(Browser{Pref: tc.pref, AppWindow: true}, find)

			if program != tc.wantProgram || kind != tc.wantKind {
				t.Errorf("resolve(%q) = %q, %v; want %q, %v", tc.pref, program, kind, tc.wantProgram, tc.wantKind)
			}
		})
	}
}

func TestTakesProfile(t *testing.T) {
	chromiumPath := filepath.Join(t.TempDir(), "Chromium", "chrome.exe")
	firefoxPath := filepath.Join(t.TempDir(), "Mozilla Firefox", "firefox.exe")
	batchPath := filepath.Join(t.TempDir(), "launchers", "Brave.CMD")
	batPath := filepath.Join(t.TempDir(), "launchers", "browser.bat")
	for _, tc := range []struct {
		name      string
		browser   Browser
		installed map[string]string
		want      bool
	}{
		{name: "a brave app window", browser: Browser{Pref: "brave", AppWindow: true}, installed: everyBrowserAt, want: true},
		{name: "a chrome app window", browser: Browser{Pref: "chrome", AppWindow: true}, installed: everyBrowserAt, want: true},
		{name: "an edge app window", browser: Browser{Pref: "edge", AppWindow: true}, installed: everyBrowserAt, want: true},
		{name: "an app window by path", browser: Browser{Pref: chromiumPath, AppWindow: true}, want: true},
		{name: "a batch file's app window", browser: Browser{Pref: batchPath, AppWindow: true}},
		{name: "a .bat file's app window", browser: Browser{Pref: batPath, AppWindow: true}},
		{
			name:      "an app window of a browser installed as a batch file",
			browser:   Browser{Pref: "brave", AppWindow: true},
			installed: map[string]string{"brave": "/opt/brave.cmd"},
		},
		{name: "a brave tab", browser: Browser{Pref: "brave"}, installed: everyBrowserAt},
		{name: "a tab by path", browser: Browser{Pref: chromiumPath}},
		{name: "firefox", browser: Browser{Pref: "firefox", AppWindow: true}, installed: everyBrowserAt},
		{name: "firefox by path", browser: Browser{Pref: firefoxPath, AppWindow: true}},
		{name: "auto", browser: Browser{Pref: "auto", AppWindow: true}, installed: everyBrowserAt},
		{name: "a missing brave", browser: Browser{Pref: "brave", AppWindow: true}, installed: map[string]string{"edge": "/opt/edge"}},
		{name: "an unknown browser", browser: Browser{Pref: "opera", AppWindow: true}, installed: everyBrowserAt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := takesProfile(tc.browser, foundAt(tc.installed)); got != tc.want {
				t.Errorf("takesProfile(%+v) = %v, want %v", tc.browser, got, tc.want)
			}
		})
	}
}

// Whoever asks TakesProfile before opening must get the answer Command acts
// on: a profile folder made for a window that ignores it is one too many,
// and a window started in one that was never made private is worse.
func TestTakesProfile_AgreesWithCommand(t *testing.T) {
	prefs := []string{
		"auto", "brave", "Chrome", "edge", "firefox",
		filepath.Join(t.TempDir(), "Chromium", "chrome.exe"),
		filepath.Join(t.TempDir(), "Mozilla Firefox", "firefox.exe"),
		filepath.Join(t.TempDir(), "launchers", "browser.cmd"),
		filepath.Join(t.TempDir(), "launchers", "BROWSER.BAT"),
	}
	installs := map[string]map[string]string{
		"everything installed":       everyBrowserAt,
		"nothing installed":          {},
		"only edge":                  {"edge": "/opt/edge"},
		"brave installed as a .cmd":  {"brave": "/opt/brave.cmd", "edge": "/opt/edge"},
		"chrome installed as a .bat": {"chrome": "/opt/Chrome.Bat"},
	}
	for setup, installed := range installs {
		for _, pref := range prefs {
			for _, appWindow := range []bool{true, false} {
				b := Browser{Pref: pref, AppWindow: appWindow, ProfileDir: filepath.Join(t.TempDir(), "browser")}

				_, args, err := Command("http://127.0.0.1:8289/", b, foundAt(installed))
				if err != nil {
					t.Fatalf("Command(%+v) with %s: %v", b, setup, err)
				}

				inProfile := slices.ContainsFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "--user-data-dir=") })
				if takes := takesProfile(b, foundAt(installed)); takes != inProfile {
					t.Errorf("with %s, takesProfile(%+v) = %v, but Command gives %q", setup, b, takes, args)
				}
			}
		}
	}
}
