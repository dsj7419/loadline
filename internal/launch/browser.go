package launch

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// The browsers a config can name.
const (
	autoBrowser = "auto"
	brave       = "brave"
	chrome      = "chrome"
	edge        = "edge"
	firefox     = "firefox"
)

// chromium are the Chromium-family browsers, which take --app= and
// --user-data-dir=.
var chromium = []string{brave, chrome, edge}

// Browser says which browser opens the dashboard, and how.
type Browser struct {
	// Pref is "auto" for the system's default browser, "brave", "chrome",
	// "edge" or "firefox", in any case, or the absolute path of a browser
	// executable.
	Pref string
	// AppWindow opens the dashboard in a Chromium app window, which has no
	// tabs or address bar. Firefox, by name or by a path to its firefox
	// executable, and the system default ignore it; any other path given
	// with it must be a Chromium-family browser's.
	AppWindow bool
	// ProfileDir is a profile folder of the dashboard's own, which a
	// Chromium app window opens in; empty means the browser's default
	// profile.
	ProfileDir string
}

// browserKind is how the program that opens the dashboard takes its URL.
type browserKind int

const (
	// systemDefault is the system's opener, which hands the URL to the
	// default browser.
	systemDefault browserKind = iota
	// chromiumKind is a Chromium-family browser, which can open an app
	// window in a profile folder of its own.
	chromiumKind
	// firefoxKind is Firefox, which has no app window.
	firefoxKind
)

// Command returns the program and arguments that open url in b. find
// returns where the named browser is installed, or false when it is not; a
// browser that is not installed gives way to the system default. The URL,
// and the profile folder, are each always a single argument, passed to the
// program itself and never through a shell, which would act on the & in
// the URL.
func Command(url string, b Browser, find func(name string) (string, bool)) (name string, args []string, err error) {
	if !known(b.Pref) {
		return "", nil, fmt.Errorf("browser %q is not one of %q, %q, %q, %q or %q, nor an absolute path",
			b.Pref, autoBrowser, brave, chrome, edge, firefox)
	}
	program, kind := resolve(b, find)
	switch kind {
	case chromiumKind:
		return program, chromiumArgs(url, b), nil
	case firefoxKind:
		return program, []string{url}, nil
	default:
		name, args = autoCommand(url)
		return name, args, nil
	}
}

// TakesProfile reports whether Open starts b, with the browsers installed
// here, as a Chromium app window: the one window that opens in ProfileDir.
// It answers as Command decides, so that a caller makes a profile folder
// ready only for a browser that opens in it.
func TakesProfile(b Browser) bool {
	return takesProfile(b, installed)
}

// takesProfile is TakesProfile, with find saying where each browser is
// installed.
func takesProfile(b Browser, find func(name string) (string, bool)) bool {
	_, kind := resolve(b, find)
	return kind == chromiumKind && b.AppWindow
}

// known reports whether pref names a browser Command can open: "auto", one
// it knows by name, in any case, or an absolute path.
func known(pref string) bool {
	name := strings.ToLower(pref)
	return filepath.IsAbs(pref) || name == autoBrowser || name == firefox || slices.Contains(chromium, name)
}

// resolve decides which program opens the dashboard for b, and how it takes
// the URL. A path is used as it is: Firefox's, as a portable or Scoop
// install has it, and otherwise a Chromium-family browser's. A browser
// named is the one find finds installed. "auto", and a named browser that
// is not installed, give the system default, which has no program of its
// own here.
func resolve(b Browser, find func(name string) (string, bool)) (program string, kind browserKind) {
	if filepath.IsAbs(b.Pref) {
		if isFirefox(b.Pref) {
			return b.Pref, firefoxKind
		}
		return b.Pref, chromiumKind
	}
	name := strings.ToLower(b.Pref)
	switch {
	case name == firefox:
		kind = firefoxKind
	case slices.Contains(chromium, name):
		kind = chromiumKind
	default:
		return "", systemDefault
	}
	path, ok := find(name)
	if !ok {
		return "", systemDefault
	}
	return path, kind
}

// isFirefox reports whether path names a firefox executable, as a portable
// or Scoop install does: Firefox has no app mode, and AppWindow is on by
// default.
func isFirefox(path string) bool {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe") == firefox
}

// chromiumArgs are the arguments that open url in a Chromium-family
// browser: in an app window, in b.ProfileDir when it is set, or in a tab of
// the browser as it runs.
func chromiumArgs(url string, b Browser) []string {
	switch {
	case b.AppWindow && b.ProfileDir != "":
		// A profile folder the browser has not used yet is a new profile,
		// which would first show the browser's welcome page and ask to make
		// it the default browser.
		return []string{"--user-data-dir=" + b.ProfileDir, "--no-first-run", "--no-default-browser-check", "--app=" + url}
	case b.AppWindow:
		return []string{"--app=" + url}
	default:
		return []string{url}
	}
}

// Open opens url in b, as Command says, and returns once the browser has
// started. The browser runs in the home directory, with none of the
// caller's standard streams.
func Open(url string, b Browser) error {
	_, err := openBrowser(url, b)
	return err
}

// openBrowser is Open, and also returns a channel that is closed once the
// browser has exited and been reaped, which a caller that must not outlive
// it can wait for.
func openBrowser(url string, b Browser) (reaped <-chan struct{}, err error) {
	name, args, err := Command(url, b, installed)
	if err != nil {
		return nil, err
	}
	cmd, err := browserCommand(name, args...)
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	// Most launchers hand the URL to a running browser and exit at once.
	// Waiting for them here, rather than releasing them, reaps them as they
	// exit, so that on Unix none lingers as a zombie for as long as the
	// server runs.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = cmd.Wait()
	}()
	return done, nil
}

// browserCommand is the command Open runs.
func browserCommand(name string, args ...string) (*exec.Cmd, error) {
	return homeCommand(browserAttr(), name, args...)
}

// firstInstalled returns the first of candidates that is an executable:
// each is a path to check or, without a directory, a command to look up on
// PATH.
func firstInstalled(candidates []string) (string, bool) {
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, true
		}
	}
	return "", false
}
