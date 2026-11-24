package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/domain"
)

// FuzzLoad reads a config file that holds arbitrary bytes. It gives a config
// or an error, never both, and a config it gives keeps every bound the
// README documents: the server can start from whatever Load accepts.
func FuzzLoad(f *testing.F) {
	for _, seed := range []string{
		`{}`,
		`{"port":8300,"openOnStart":"always","browser":"brave","appWindow":false,"browserProfile":"default","idleExitMinutes":0,` +
			`"retentionDays":7,"claudePath":"/usr/local/bin/claude",` +
			`"alerts":{"weeklyPct":75,"contextPct":100,"needsYouSeconds":1},` +
			`"accounts":[{"configDir":"~/.claude","label":"Main","plan":"Ultra","dailyBudgetUSD":25,` +
			`"credits":[{"usd":20,"addedAt":"2026-11-21T12:00:00-08:00"}]},{"configDir":"~/.claude-b"}]}`,
		`{"accounts":[{"configDir":"~/a","plan":" Ultra "}]}`,
		`{"accounts":[{"configDir":"~/a","plan":"` + strings.Repeat("x", 40) + `"}]}`,
		"\xef\xbb\xbf{\"port\":1024}",
		`{"port":80}`, `{"port":65536}`, `{"openOnStart":"sometimes"}`, `{"browser":" "}`, `{"browserProfile":"work"}`,
		`{"idleExitMinutes":-1}`, `{"retentionDays":0}`, `{"alerts":{"weeklyPct":100.5}}`,
		`{"accounts":[{"configDir":"relative"}]}`, `{"accounts":[{"configDir":"~/a"},{"configDir":"~/a/"}]}`,
		`{"accounts":[{"configDir":"~/a","credits":[{"usd":0,"addedAt":"2026-11-21T12:00:00Z"}]}]}`,
		`{"accounts":[{"configDir":"~/a","credits":[{"usd":1,"addedAt":"0000-01-01T00:00:00+01:00"}]}]}`,
		`{"Port":9000,"PORT":9001}`, `{"unknown":1}`, `{} {}`, `[]`, `null`, ``,
	} {
		f.Add([]byte(seed))
	}
	fuzzHome := filepath.Join(f.TempDir(), "home")
	path := filepath.Join(f.TempDir(), "config.json")

	f.Fuzz(func(t *testing.T, data []byte) {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}

		cfg, err := config.Load(path, fuzzHome)
		if err != nil {
			if !reflect.DeepEqual(cfg, config.Config{}) {
				t.Errorf("Load gave %+v with the error %v", cfg, err)
			}
			return
		}
		checkBounds(t, cfg, fuzzHome)
	})
}

// checkBounds fails the test for each documented bound cfg, loaded against
// homeDir, breaks.
func checkBounds(t *testing.T, cfg config.Config, homeDir string) {
	t.Helper()
	if config.ValidatePort(cfg.Port) != nil {
		t.Errorf("port %d is outside 1024-65535", cfg.Port)
	}
	if !slices.Contains([]config.OpenMode{config.OpenOncePerLogin, config.OpenAlways, config.OpenNever}, cfg.OpenOnStart) {
		t.Errorf("openOnStart %q is no mode Loadline knows", cfg.OpenOnStart)
	}
	if strings.TrimSpace(cfg.Browser) == "" {
		t.Error("browser is empty")
	}
	if !slices.Contains([]config.ProfileMode{config.ProfileDedicated, config.ProfileDefault}, cfg.BrowserProfile) {
		t.Errorf("browserProfile %q is no mode Loadline knows", cfg.BrowserProfile)
	}
	if cfg.IdleExitMinutes < 0 || cfg.RetentionDays < 1 {
		t.Errorf("idleExitMinutes %d or retentionDays %d is out of range", cfg.IdleExitMinutes, cfg.RetentionDays)
	}
	alerts := cfg.Alerts
	if !(alerts.WeeklyPct >= 0 && alerts.WeeklyPct <= 100) || !(alerts.ContextPct >= 0 && alerts.ContextPct <= 100) ||
		alerts.NeedsYouSeconds < 1 {
		t.Errorf("alerts %+v are out of range", alerts)
	}
	dirs := make(map[domain.ConfigDir]bool, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		dir := account.Dir(homeDir)
		if !filepath.IsAbs(string(dir)) || dirs[dir] {
			t.Errorf("account %+v has the config dir %q, want one absolute and its own", account, dir)
		}
		dirs[dir] = true
		if account.Plan != strings.TrimSpace(account.Plan) || utf8.RuneCountInString(account.Plan) > 32 {
			t.Errorf("account %+v has the plan %q, want one trimmed and at most 32 characters", account, account.Plan)
		}
		if !(account.DailyBudgetUSD >= 0) {
			t.Errorf("account %+v has the daily budget %v", account, account.DailyBudgetUSD)
		}
		for _, grant := range account.Credits {
			if !(grant.USD > 0) || grant.AddedAt.IsZero() {
				t.Errorf("account %+v has the credit grant %+v", account, grant)
			}
		}
	}
}
