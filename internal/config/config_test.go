package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	return path
}

func mustLoad(t *testing.T, contents string) config.Config {
	t.Helper()

	cfg, err := config.Load(writeConfig(t, contents))
	if err != nil {
		t.Fatalf("Load(%s): %v", contents, err)
	}
	return cfg
}

func TestDefaults(t *testing.T) {
	want := config.Config{
		Port:            8289,
		OpenOnStart:     config.OpenOncePerLogin,
		Browser:         "auto",
		AppWindow:       true,
		IdleExitMinutes: 30,
		RetentionDays:   30,
		Alerts:          config.AlertSettings{WeeklyPct: 90, ContextPct: 85, NeedsYouSeconds: 20},
	}

	if got := config.Defaults(); !reflect.DeepEqual(got, want) {
		t.Errorf("Defaults() = %+v, want %+v", got, want)
	}
}

func TestOpenModeValues(t *testing.T) {
	for mode, want := range map[config.OpenMode]string{
		config.OpenOncePerLogin: "once_per_login",
		config.OpenAlways:       "always",
		config.OpenNever:        "never",
	} {
		if string(mode) != want {
			t.Errorf("open mode %q, want %q", mode, want)
		}
	}
}

func TestLoad_MissingFileGivesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "config.json")

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load of a missing file: %v", err)
	}
	if want := config.Defaults(); !reflect.DeepEqual(got, want) {
		t.Errorf("Load of a missing file = %+v, want defaults %+v", got, want)
	}
}

func TestLoad_EmptyObjectGivesDefaults(t *testing.T) {
	if got, want := mustLoad(t, `{}`), config.Defaults(); !reflect.DeepEqual(got, want) {
		t.Errorf("Load of {} = %+v, want defaults %+v", got, want)
	}
}

func TestLoad_ReadsEveryDocumentedKey(t *testing.T) {
	got := mustLoad(t, `{
		"port": 9000,
		"openOnStart": "never",
		"browser": "chrome",
		"appWindow": false,
		"idleExitMinutes": 0,
		"retentionDays": 7,
		"claudePath": "C:/tools/claude.exe",
		"alerts": {"weeklyPct": 75.5, "contextPct": 60, "needsYouSeconds": 45},
		"accounts": [
			{"configDir": "C:/Users/me/.claude", "label": "Main"},
			{"configDir": "C:/Users/me/.claude-b", "label": "B", "dailyBudgetUSD": 25.5}
		]
	}`)

	want := config.Config{
		Port:            9000,
		OpenOnStart:     config.OpenNever,
		Browser:         "chrome",
		AppWindow:       false,
		IdleExitMinutes: 0,
		RetentionDays:   7,
		ClaudePath:      "C:/tools/claude.exe",
		Alerts:          config.AlertSettings{WeeklyPct: 75.5, ContextPct: 60, NeedsYouSeconds: 45},
		Accounts: []config.AccountSetting{
			{ConfigDir: "C:/Users/me/.claude", Label: "Main"},
			{ConfigDir: "C:/Users/me/.claude-b", Label: "B", DailyBudgetUSD: 25.5},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

func TestLoad_PartialFileOverridesOnlySetKeys(t *testing.T) {
	got := mustLoad(t, `{"accounts":[{"configDir":"C:/Users/me/.claude","label":"Main"}],"browser":"brave","appWindow":true}`)

	want := config.Defaults()
	want.Browser = "brave"
	want.Accounts = []config.AccountSetting{{ConfigDir: "C:/Users/me/.claude", Label: "Main"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

func TestLoad_PartialAlertsKeepRemainingDefaults(t *testing.T) {
	got := mustLoad(t, `{"alerts":{"weeklyPct":95}}`)

	want := config.Defaults()
	want.Alerts.WeeklyPct = 95
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

func TestLoad_ExplicitZeroValuesOverrideDefaults(t *testing.T) {
	got := mustLoad(t, `{"appWindow":false,"idleExitMinutes":0}`)

	if got.AppWindow {
		t.Error("appWindow:false was ignored")
	}
	if got.IdleExitMinutes != 0 {
		t.Errorf("idleExitMinutes:0 gave %d, want 0 (never exit)", got.IdleExitMinutes)
	}
}

func TestLoad_AcceptsBoundaryValues(t *testing.T) {
	for name, contents := range map[string]string{
		"lowest port":          `{"port":1024}`,
		"highest port":         `{"port":65535}`,
		"one retention day":    `{"retentionDays":1}`,
		"never exit when idle": `{"idleExitMinutes":0}`,
		"alert thresholds low": `{"alerts":{"weeklyPct":0,"contextPct":0,"needsYouSeconds":1}}`,
		"alert thresholds max": `{"alerts":{"weeklyPct":100,"contextPct":100}}`,
		"open always":          `{"openOnStart":"always"}`,
		"open once per login":  `{"openOnStart":"once_per_login"}`,
		"zero daily budget":    `{"accounts":[{"configDir":"/home/me/.claude","dailyBudgetUSD":0}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			mustLoad(t, contents)
		})
	}
}

func TestLoad_RejectsUnknownKey(t *testing.T) {
	for name, tc := range map[string]struct{ contents, wantKey string }{
		"top level":       {`{"porrt":8289}`, "porrt"},
		"inside alerts":   {`{"alerts":{"weekly":90}}`, "weekly"},
		"inside accounts": {`{"accounts":[{"configDir":"/x","lable":"X"}]}`, "lable"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, tc.contents))
			if err == nil {
				t.Fatal("Load accepted an unknown key")
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Errorf("error %q does not name the unknown key %q", err, tc.wantKey)
			}
		})
	}
}

func TestLoad_RejectsInvalidValues(t *testing.T) {
	for name, tc := range map[string]struct{ contents, wantMessage string }{
		"port below range":        {`{"port":1023}`, "port 1023 is outside"},
		"port zero":               {`{"port":0}`, "port 0 is outside"},
		"port above range":        {`{"port":65536}`, "port 65536 is outside"},
		"retention below one":     {`{"retentionDays":0}`, "retentionDays 0 is below"},
		"negative idle exit":      {`{"idleExitMinutes":-1}`, "idleExitMinutes -1 is below"},
		"weekly pct negative":     {`{"alerts":{"weeklyPct":-1}}`, "alerts.weeklyPct -1 is outside"},
		"weekly pct above 100":    {`{"alerts":{"weeklyPct":100.5}}`, "alerts.weeklyPct 100.5 is outside"},
		"context pct negative":    {`{"alerts":{"contextPct":-0.5}}`, "alerts.contextPct -0.5 is outside"},
		"context pct above 100":   {`{"alerts":{"contextPct":101}}`, "alerts.contextPct 101 is outside"},
		"needs you below one":     {`{"alerts":{"needsYouSeconds":0}}`, "alerts.needsYouSeconds 0 is below"},
		"unknown open mode":       {`{"openOnStart":"sometimes"}`, `openOnStart "sometimes" is not one of`},
		"empty open mode":         {`{"openOnStart":""}`, `openOnStart "" is not one of`},
		"empty browser":           {`{"browser":""}`, "browser must not be empty"},
		"account without dir":     {`{"accounts":[{"configDir":"/a"},{"label":"B"}]}`, "accounts[1].configDir must not be empty"},
		"account with blank dir":  {`{"accounts":[{"configDir":"  "}]}`, "accounts[0].configDir must not be empty"},
		"account negative budget": {`{"accounts":[{"configDir":"/a","dailyBudgetUSD":-1}]}`, "accounts[0].dailyBudgetUSD -1 is below"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, tc.contents))
			if err == nil {
				t.Fatal("Load accepted an invalid config")
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Errorf("error %q does not contain %q", err, tc.wantMessage)
			}
		})
	}
}

func TestLoad_RejectsMalformedFiles(t *testing.T) {
	for name, contents := range map[string]string{
		"not json":            `port = 8289`,
		"empty file":          ``,
		"whitespace only":     " \n",
		"array at top level":  `[]`,
		"wrong type for port": `{"port":"8289"}`,
		"fractional port":     `{"port":8289.5}`,
		"truncated":           `{"port":8289`,
		"second document":     `{"port":8289} {"port":8290}`,
		"trailing garbage":    `{"port":8289} nope`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := config.Load(writeConfig(t, contents)); err == nil {
				t.Fatalf("Load accepted %q", contents)
			}
		})
	}
}

func TestLoad_ErrorsNameTheFile(t *testing.T) {
	path := writeConfig(t, `{"port":80}`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted an invalid config")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the file %q", err, path)
	}
}

func TestLoad_UnreadableFileIsAnError(t *testing.T) {
	if _, err := config.Load(t.TempDir()); err == nil {
		t.Fatal("Load of a directory succeeded, want a read error rather than defaults")
	}
}

func TestValidatePort(t *testing.T) {
	for _, port := range []int{1024, 8289, 65535} {
		if err := config.ValidatePort(port); err != nil {
			t.Errorf("ValidatePort(%d) = %v, want nil", port, err)
		}
	}
	for _, port := range []int{-1, 0, 80, 1023, 65536} {
		err := config.ValidatePort(port)
		if err == nil || !strings.Contains(err.Error(), "outside 1024-65535") {
			t.Errorf("ValidatePort(%d) = %v, want an out-of-range error", port, err)
		}
	}
}
