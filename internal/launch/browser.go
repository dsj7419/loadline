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

// chromium are the browsers that take --app=.
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
}

// Command returns the program and arguments that open url in b. find
// returns where the named browser is installed, or false when it is not; a
// browser that is not installed gives way to the system default. The URL is
// always a single argument, passed to the program itself and never through
// a shell, which would act on the & in it.
func Command(url string, b Browser, find func(name string) (string, bool)) (name string, args []string, err error) {
	if filepath.IsAbs(b.Pref) {
		if isFirefox(b.Pref) {
			return b.Pref, []string{url}, nil
		}
		return b.Pref, chromiumArgs(url, b.AppWindow), nil
	}
	pref := strings.ToLower(b.Pref)
	switch {
	case pref == autoBrowser:
		name, args = autoCommand(url)
		return name, args, nil
	case pref == firefox:
		if path, ok := find(pref); ok {
			return path, []string{url}, nil
		}
	case slices.Contains(chromium, pref):
		if path, ok := find(pref); ok {
			return path, chromiumArgs(url, b.AppWindow), nil
		}
	default:
		return "", nil, fmt.Errorf("browser %q is not one of %q, %q, %q, %q or %q, nor an absolute path",
			b.Pref, autoBrowser, brave, chrome, edge, firefox)
	}
	name, args = autoCommand(url)
	return name, args, nil
}

// isFirefox reports whether path names a firefox executable, as a portable
// or Scoop install does: Firefox has no app mode, and AppWindow is on by
// default.
func isFirefox(path string) bool {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe") == firefox
}

func chromiumArgs(url string, appWindow bool) []string {
	if appWindow {
		return []string{"--app=" + url}
	}
	return []string{url}
}

// Open opens url in b, as Command says, and returns once the browser has
// started. The browser runs in the home directory, with none of the
// caller's standard streams.
func Open(url string, b Browser) error {
	name, args, err := Command(url, b, installed)
	if err != nil {
		return err
	}
	cmd, err := browserCommand(name, args...)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	// Most launchers hand the URL to a running browser and exit at once.
	// Waiting for them here, rather than releasing them, reaps them as they
	// exit, so that on Unix none lingers as a zombie for as long as the
	// server runs.
	go func() { _ = cmd.Wait() }()
	return nil
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
