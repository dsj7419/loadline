package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/launch"
)

const (
	// openDelay is how long a starting server gives a dashboard that is
	// already open, and lost its server, to reconnect before it opens one.
	openDelay = 3 * time.Second
	// openStampName holds the boot time of the last time the server opened
	// the dashboard.
	openStampName = "opened.stamp"
	// sameBoot is how far apart two readings of the boot time may be and
	// still name the same boot: some systems derive it from the uptime.
	sameBoot = 120 * time.Second
)

// dashboardOpener is how a starting server opens the dashboard.
type dashboardOpener struct {
	mode config.OpenMode
	// browser returns the browser to open the dashboard in, with its
	// profile made ready, as dashboardBrowser does.
	browser  func() (launch.Browser, error)
	open     func(url string, b launch.Browser) error
	bootTime func() (time.Time, error)
	// stamp is the file that records the boot of the last open, or "" when
	// it is neither read nor written.
	stamp string
}

// newDashboardOpener reads the open policy from the config and the flags.
// A demo is started by hand, to be looked at whatever the real server has
// opened this boot, so it opens as if always, and leaves the stamp to the
// real server.
func newDashboardOpener(o ServeOptions, cfg config.Config) dashboardOpener {
	opener := dashboardOpener{
		mode:     cfg.OpenOnStart,
		browser:  func() (launch.Browser, error) { return dashboardBrowser(cfg, o.Paths) },
		open:     o.Open,
		bootTime: o.BootTime,
		stamp:    filepath.Join(o.Paths.DataDir, openStampName),
	}
	switch {
	case o.NoOpen:
		opener.mode = config.OpenNever
	case o.Demo:
		opener.stamp = ""
		if opener.mode == config.OpenOncePerLogin {
			opener.mode = config.OpenAlways
		}
	}
	if opener.open == nil {
		opener.open = launch.Open
	}
	if opener.bootTime == nil {
		opener.bootTime = launch.BootTime
	}
	return opener
}

// openDashboard opens the dashboard, signed in by a fresh login code, once
// the server has served for openDelay, unless the policy says not to or a
// dashboard has connected by then. A dashboard left open by the server
// before this one reconnects on its own, and a second window would only
// get in the way.
func (s *instance) openDashboard(ctx context.Context) {
	o := s.opener
	if o.mode == config.OpenNever || !s.after.Sleep(ctx, openDelay) {
		return
	}
	if s.srv.Clients() > 0 {
		s.log.Info("dashboard not opened", "reason", "a dashboard is connected")
		return
	}
	boot, err := o.bootTime()
	if err != nil {
		s.log.Warn("boot time unknown; opening the dashboard as on every start", "err", err)
		boot = time.Time{}
	}
	stampBoot, haveStamp := readOpenStamp(o.stamp)
	if !shouldOpen(o.mode, stampBoot, boot, haveStamp) {
		s.log.Info("dashboard not opened", "reason", "opened earlier this boot")
		return
	}
	// Before the login code is minted, so that none of the minute it is
	// valid for goes on making the profile ready.
	browser, err := o.browser()
	if err != nil {
		s.log.Warn("opening the dashboard in the browser's default profile", "err", err)
	}
	loginURL := forward.LoopbackURL(s.port) + "/login?code=" + url.QueryEscape(s.srv.MintLoginCode())
	if err := o.open(loginURL, browser); err != nil {
		s.log.Warn("open the dashboard failed", "err", err)
		return
	}
	s.log.Info("dashboard opened")
	if o.stamp == "" || boot.IsZero() {
		return
	}
	if err := writeOpenStamp(o.stamp, boot); err != nil {
		s.log.Warn("record the dashboard's opening failed", "err", err)
	}
}

// shouldOpen applies the open policy: never, always, or once per boot,
// which opens unless the stamp records the current boot, within sameBoot.
// A zero currentBoot is a boot time that could not be read, which opens as
// always does.
func shouldOpen(mode config.OpenMode, stampBoot, currentBoot time.Time, haveStamp bool) bool {
	switch mode {
	case config.OpenNever:
		return false
	case config.OpenOncePerLogin:
		return !haveStamp || currentBoot.IsZero() || stampBoot.Sub(currentBoot).Abs() > sameBoot
	default:
		return true
	}
}

// readOpenStamp returns the boot time the stamp at path records, and false
// when there is none to read.
func readOpenStamp(path string) (time.Time, bool) {
	if path == "" {
		return time.Time{}, false
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}

// writeOpenStamp records boot, in Unix seconds, as the boot of the last open.
func writeOpenStamp(path string, boot time.Time) error {
	if err := os.WriteFile(path, []byte(strconv.FormatInt(boot.Unix(), 10)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
