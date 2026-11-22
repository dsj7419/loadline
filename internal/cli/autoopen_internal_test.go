package cli

import (
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/config"
)

func TestShouldOpen(t *testing.T) {
	boot := time.Date(2026, time.November, 21, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		mode      config.OpenMode
		stamp     time.Time
		haveStamp bool
		current   time.Time
		want      bool
	}{
		{name: "never", mode: config.OpenNever, current: boot, want: false},
		{name: "never, first this boot", mode: config.OpenNever, stamp: boot.Add(-time.Hour), haveStamp: true, current: boot, want: false},
		{name: "always", mode: config.OpenAlways, current: boot, want: true},
		{name: "always, opened this boot", mode: config.OpenAlways, stamp: boot, haveStamp: true, current: boot, want: true},
		{name: "once, never opened", mode: config.OpenOncePerLogin, current: boot, want: true},
		{name: "once, opened this boot", mode: config.OpenOncePerLogin, stamp: boot, haveStamp: true, current: boot, want: false},
		{name: "once, this boot read 120 s later", mode: config.OpenOncePerLogin, stamp: boot, haveStamp: true, current: boot.Add(120 * time.Second), want: false},
		{name: "once, this boot read 120 s earlier", mode: config.OpenOncePerLogin, stamp: boot, haveStamp: true, current: boot.Add(-120 * time.Second), want: false},
		{name: "once, a boot 121 s later", mode: config.OpenOncePerLogin, stamp: boot, haveStamp: true, current: boot.Add(121 * time.Second), want: true},
		{name: "once, an earlier boot's stamp", mode: config.OpenOncePerLogin, stamp: boot.Add(-24 * time.Hour), haveStamp: true, current: boot, want: true},
		{name: "once, a later boot's stamp", mode: config.OpenOncePerLogin, stamp: boot.Add(24 * time.Hour), haveStamp: true, current: boot, want: true},
		{name: "once, boot time unknown", mode: config.OpenOncePerLogin, stamp: boot, haveStamp: true, current: time.Time{}, want: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldOpen(c.mode, c.stamp, c.current, c.haveStamp); got != c.want {
				t.Errorf("shouldOpen(%s, stamp %v (%v), boot %v) = %v, want %v", c.mode, c.stamp, c.haveStamp, c.current, got, c.want)
			}
		})
	}
}

func TestDashboardOpener_ModeFromTheFlagsAndConfig(t *testing.T) {
	cases := []struct {
		name   string
		opts   ServeOptions
		config config.OpenMode
		want   config.OpenMode
	}{
		{name: "the config's", config: config.OpenOncePerLogin, want: config.OpenOncePerLogin},
		{name: "always", config: config.OpenAlways, want: config.OpenAlways},
		{name: "never", config: config.OpenNever, want: config.OpenNever},
		{name: "--no-open", opts: ServeOptions{NoOpen: true}, config: config.OpenAlways, want: config.OpenNever},
		// A demo is started by hand, to be looked at, whatever the real server
		// opened this boot; and it leaves the stamp to the real server.
		{name: "demo", opts: ServeOptions{Demo: true}, config: config.OpenOncePerLogin, want: config.OpenAlways},
		{name: "demo, never", opts: ServeOptions{Demo: true}, config: config.OpenNever, want: config.OpenNever},
		{name: "demo, --no-open", opts: ServeOptions{Demo: true, NoOpen: true}, config: config.OpenOncePerLogin, want: config.OpenNever},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.OpenOnStart = c.config

			if got := newDashboardOpener(c.opts, cfg).mode; got != c.want {
				t.Errorf("mode = %s, want %s", got, c.want)
			}
		})
	}
}
