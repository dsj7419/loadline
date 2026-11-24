package cli

import (
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/launch"
)

// dashboardBrowser returns the browser that opens the dashboard, as cfg
// says, and makes its profile ready. A Chromium app window opens in the
// dashboard's own profile, paths.BrowserProfileDir, unless the config asks
// for the browser's default profile. It is called as the dashboard opens,
// so that nothing that opens no window makes a profile.
//
// When the profile cannot be made ready, it returns the browser in its
// default profile, with the reason, for the caller to report before it
// opens the dashboard there: a profile that Loadline cannot use never keeps
// the dashboard from opening.
func dashboardBrowser(cfg config.Config, paths config.Paths) (launch.Browser, error) {
	b := launch.Browser{Pref: cfg.Browser, AppWindow: cfg.AppWindow}
	if cfg.BrowserProfile != config.ProfileDedicated || !launch.TakesProfile(b) {
		return b, nil
	}
	// PrepareBrowserProfile judges the profile alone, and Make the data
	// directory that holds it: no link, nothing but Loadline's files, and
	// private. A server made it as it started, but loadline open may not
	// have, and it may have changed since; Make changes nothing that is
	// private already.
	if err := datadir.Make(paths.DataDir); err != nil {
		return b, err
	}
	if err := datadir.PrepareBrowserProfile(paths.BrowserProfileDir); err != nil {
		return b, err
	}
	b.ProfileDir = paths.BrowserProfileDir
	return b, nil
}
