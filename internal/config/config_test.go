package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/config"
)

// home is the home directory every test loads its config against.
var home = filepath.Join(os.TempDir(), "loadline-config-test", "home")

// abs returns an absolute config dir under home, spelled with forward
// slashes as a hand-written config file would spell it.
func abs(name string) string { return filepath.ToSlash(filepath.Join(home, name)) }

// q returns s as a JSON string.
func q(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal %q: %v", s, err)
	}
	return string(b)
}

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

	cfg, err := config.Load(writeConfig(t, contents), home)
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

	got, err := config.Load(path, home)
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
			{"configDir": `+q(t, abs(".claude"))+`, "label": "Main", "plan": "Ultra",
				"credits": [{"usd": 20, "addedAt": "2026-11-21T12:00:00-08:00"}, {"usd": 7.5, "addedAt": "2026-11-22T09:30:00Z"}]},
			{"configDir": `+q(t, abs(".claude-b"))+`, "label": "B", "dailyBudgetUSD": 25.5}
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
			{ConfigDir: abs(".claude"), Label: "Main", Plan: "Ultra", Credits: []config.CreditGrant{
				{USD: 20, AddedAt: time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC)},
				{USD: 7.5, AddedAt: time.Date(2026, 11, 22, 9, 30, 0, 0, time.UTC)},
			}},
			{ConfigDir: abs(".claude-b"), Label: "B", DailyBudgetUSD: 25.5},
		},
	}
	if len(got.Accounts) != 2 || len(got.Accounts[0].Credits) != 2 {
		t.Fatalf("Load = %+v, want two accounts, the first with two grants", got)
	}
	// A time read from JSON keeps the offset it was written with, which
	// DeepEqual would compare; the moment is what matters.
	for i, g := range got.Accounts[0].Credits {
		if !g.AddedAt.Equal(want.Accounts[0].Credits[i].AddedAt) {
			t.Errorf("credits[%d].addedAt = %v, want %v", i, g.AddedAt, want.Accounts[0].Credits[i].AddedAt)
		}
		got.Accounts[0].Credits[i].AddedAt = want.Accounts[0].Credits[i].AddedAt
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

func TestLoad_PartialFileOverridesOnlySetKeys(t *testing.T) {
	got := mustLoad(t, `{"accounts":[{"configDir":`+q(t, abs(".claude"))+`,"label":"Main"}],"browser":"brave","appWindow":true}`)

	want := config.Defaults()
	want.Browser = "brave"
	want.Accounts = []config.AccountSetting{{ConfigDir: abs(".claude"), Label: "Main"}}
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
		"zero daily budget":    `{"accounts":[{"configDir":` + q(t, abs(".claude")) + `,"dailyBudgetUSD":0}]}`,
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
		"inside credits":  {`{"accounts":[{"configDir":"/x","credits":[{"usd":5,"added":"2026-11-21T12:00:00Z"}]}]}`, "added"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, tc.contents), home)
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
		"account without dir":     {`{"accounts":[{"configDir":` + q(t, abs("a")) + `},{"label":"B"}]}`, "accounts[1].configDir must not be empty"},
		"account with blank dir":  {`{"accounts":[{"configDir":"  "}]}`, "accounts[0].configDir must not be empty"},
		"account negative budget": {`{"accounts":[{"configDir":` + q(t, abs("a")) + `,"dailyBudgetUSD":-1}]}`, "accounts[0].dailyBudgetUSD -1 is below"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, tc.contents), home)
			if err == nil {
				t.Fatal("Load accepted an invalid config")
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Errorf("error %q does not contain %q", err, tc.wantMessage)
			}
		})
	}
}

func TestLoad_CreditGrantValidation(t *testing.T) {
	account := func(credits string) string {
		return `{"accounts":[{"configDir":` + q(t, abs(".claude")) + `},{"configDir":` + q(t, abs(".claude-b")) +
			`,"credits":` + credits + `}]}`
	}
	for name, tc := range map[string]struct{ credits, wantMessage string }{
		"zero dollars": {
			`[{"usd":20,"addedAt":"2026-11-21T12:00:00-08:00"},{"usd":0,"addedAt":"2026-11-21T12:00:00-08:00"}]`,
			"accounts[1].credits[1].usd 0 is not above 0",
		},
		"negative dollars": {`[{"usd":-5,"addedAt":"2026-11-21T12:00:00Z"}]`, "accounts[1].credits[0].usd -5 is not above 0"},
		"no amount":        {`[{"addedAt":"2026-11-21T12:00:00Z"}]`, "accounts[1].credits[0].usd 0 is not above 0"},
		"no date":          {`[{"usd":20}]`, "accounts[1].credits[0].addedAt must be set"},
		"a null date":      {`[{"usd":20,"addedAt":null}]`, "accounts[1].credits[0].addedAt must be set"},
		"a date with no offset": {
			`[{"usd":20,"addedAt":"2026-11-21T12:00:00"}]`,
			`credit addedAt "2026-11-21T12:00:00" is not a time with its offset, such as "2026-11-21T12:00:00-08:00"`,
		},
		"a date alone":     {`[{"usd":20,"addedAt":"2026-11-21"}]`, `credit addedAt "2026-11-21" is not a time`},
		"a number of days": {`[{"usd":20,"addedAt":3}]`, "credit addedAt 3 is not a time"},
		"text for dollars": {`[{"usd":"20","addedAt":"2026-11-21T12:00:00Z"}]`, "usd"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, account(tc.credits)), home)
			if err == nil {
				t.Fatal("Load accepted an invalid credit grant")
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Errorf("error %q does not contain %q", err, tc.wantMessage)
			}
		})
	}

	for name, credits := range map[string]string{
		"no grants":     `[]`,
		"a cent":        `[{"usd":0.01,"addedAt":"2026-11-21T12:00:00Z"}]`,
		"several dates": `[{"usd":20,"addedAt":"2026-11-21T12:00:00-08:00"},{"usd":5,"addedAt":"2026-10-01T00:00:00+02:00"}]`,
	} {
		t.Run("accepts "+name, func(t *testing.T) {
			mustLoad(t, account(credits))
		})
	}
}

// An account's plan is only a name to show, read without the spaces around
// it, so a blank one names none, and as short as a badge needs.
func TestLoad_AccountPlan(t *testing.T) {
	account := func(plan string) string {
		return `{"accounts":[{"configDir":` + q(t, abs(".claude")) + `},{"configDir":` + q(t, abs(".claude-b")) +
			`,"plan":` + q(t, plan) + `}]}`
	}
	for name, tc := range map[string]struct{ plan, want string }{
		"a name":                       {"Ultra", "Ultra"},
		"a name in spaces":             {" \tUltra 20x \n", "Ultra 20x"},
		"blank":                        {" \t\n", ""},
		"none":                         {"", ""},
		"32 characters":                {strings.Repeat("é", 32), strings.Repeat("é", 32)},
		"32 characters inside spaces":  {"  " + strings.Repeat("x", 32) + "  ", strings.Repeat("x", 32)},
		"a no-break space around them": {" Ultra ", "Ultra"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := mustLoad(t, account(tc.plan))
			if got := cfg.Accounts[1].Plan; got != tc.want {
				t.Errorf("plan %q loads as %q, want %q", tc.plan, got, tc.want)
			}
		})
	}

	_, err := config.Load(writeConfig(t, account(strings.Repeat("é", 33))), home)
	if want := "accounts[1].plan is 33 characters, more than 32"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Load of a 33-character plan: error %v, want one containing %q", err, want)
	}
}

// An account's label is the title of its card, read without the spaces around
// it, so a blank one is unset and the label is derived, and as short as a
// card title needs.
func TestLoad_AccountLabel(t *testing.T) {
	account := func(label string) string {
		return `{"accounts":[{"configDir":` + q(t, abs(".claude")) + `},{"configDir":` + q(t, abs(".claude-b")) +
			`,"label":` + q(t, label) + `}]}`
	}
	for name, tc := range map[string]struct{ label, want string }{
		"a name":                       {"Main", "Main"},
		"a name in spaces":             {" \tWork laptop \n", "Work laptop"},
		"blank":                        {" \t\n", ""},
		"none":                         {"", ""},
		"32 characters":                {strings.Repeat("é", 32), strings.Repeat("é", 32)},
		"32 characters inside spaces":  {"  " + strings.Repeat("x", 32) + "  ", strings.Repeat("x", 32)},
		"a no-break space around them": {"\u00a0Main\u00a0", "Main"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := mustLoad(t, account(tc.label))
			if got := cfg.Accounts[1].Label; got != tc.want {
				t.Errorf("label %q loads as %q, want %q", tc.label, got, tc.want)
			}
		})
	}

	_, err := config.Load(writeConfig(t, account(strings.Repeat("é", 33))), home)
	if want := "accounts[1].label is 33 characters, more than 32"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Load of a 33-character label: error %v, want one containing %q", err, want)
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
			if _, err := config.Load(writeConfig(t, contents), home); err == nil {
				t.Fatalf("Load accepted %q", contents)
			}
		})
	}
}

func TestLoad_ErrorsNameTheFile(t *testing.T) {
	path := writeConfig(t, `{"port":80}`)

	_, err := config.Load(path, home)
	if err == nil {
		t.Fatal("Load accepted an invalid config")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the file %q", err, path)
	}
}

func TestLoad_UnreadableFileIsAnError(t *testing.T) {
	if _, err := config.Load(t.TempDir(), home); err == nil {
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

// Windows editors save files with a byte-order mark.
func TestLoad_StripsALeadingByteOrderMark(t *testing.T) {
	got := mustLoad(t, "\xef\xbb\xbf"+`{"port":9000}`)

	if got.Port != 9000 {
		t.Errorf("Load after a byte-order mark gave port %d, want 9000", got.Port)
	}
}

func TestLoad_AccountDirsMayStartWithTheHomeDir(t *testing.T) {
	cfg := mustLoad(t, `{"accounts":[{"configDir":"~/.claude-b"},{"configDir":`+q(t, `~\.claude-work`)+`}]}`)

	for i, tc := range []struct{ spelled, under string }{
		{spelled: "~/.claude-b", under: ".claude-b"},
		{spelled: `~\.claude-work`, under: ".claude-work"},
	} {
		account := cfg.Accounts[i]
		if account.ConfigDir != tc.spelled {
			t.Errorf("accounts[%d].ConfigDir = %q, want it kept as written, %q", i, account.ConfigDir, tc.spelled)
		}
		want := config.ResolveConfigDir(filepath.Join(home, tc.under), home)
		if got := account.Dir(home); got != want {
			t.Errorf("accounts[%d].Dir = %q, want %q", i, got, want)
		}
	}
}

func TestAccountSetting_DirIsTheNormalizedKey(t *testing.T) {
	account := config.AccountSetting{ConfigDir: abs(".claude-b/")}

	if got, want := account.Dir(home), config.ResolveConfigDir(abs(".claude-b"), home); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

func TestLoad_RejectsARelativeAccountDir(t *testing.T) {
	relative := []string{".claude-b", "claude/b", "./x", "../x", "~", "~user/.claude", "~.claude"}
	if runtime.GOOS == "windows" {
		relative = append(relative, "/Users/me/.claude", `\Users\me\.claude`, "C:.claude")
	}
	for _, dir := range relative {
		t.Run(dir, func(t *testing.T) {
			contents := `{"accounts":[{"configDir":` + q(t, abs(".claude")) + `},{"configDir":` + q(t, dir) + `}]}`

			_, err := config.Load(writeConfig(t, contents), home)
			if err == nil || !strings.Contains(err.Error(), "accounts[1].configDir") {
				t.Errorf("Load = %v, want an error naming accounts[1].configDir", err)
			}
		})
	}
}

func TestLoad_HomeDirAccountNeedsAKnownHome(t *testing.T) {
	_, err := config.Load(writeConfig(t, `{"accounts":[{"configDir":"~/.claude-b"}]}`), "")

	if err == nil || !strings.Contains(err.Error(), "accounts[0].configDir") {
		t.Errorf("Load with no home = %v, want an error naming accounts[0].configDir", err)
	}
}

func TestLoad_RejectsTwoAccountsForOneDir(t *testing.T) {
	cases := map[string][2]string{
		"same spelling":      {abs(".claude-b"), abs(".claude-b")},
		"trailing separator": {abs(".claude-b"), abs(".claude-b") + "/"},
		"dot segments":       {abs(".claude-b"), abs("x/../.claude-b")},
		"home dir spelling":  {abs(".claude-b"), "~/.claude-b"},
	}
	if runtime.GOOS == "windows" {
		cases["letter case"] = [2]string{abs(".claude-b"), strings.ToUpper(abs(".claude-b"))}
		cases["backslashes"] = [2]string{abs(".claude-b"), filepath.FromSlash(abs(".claude-b"))}
	}
	for name, dirs := range cases {
		t.Run(name, func(t *testing.T) {
			contents := `{"accounts":[{"configDir":` + q(t, abs(".claude")) + `},{"configDir":` + q(t, dirs[0]) +
				`},{"configDir":` + q(t, dirs[1]) + `}]}`

			_, err := config.Load(writeConfig(t, contents), home)
			if err == nil || !strings.Contains(err.Error(), "accounts[2].configDir duplicates accounts[1]") {
				t.Errorf("Load = %v, want accounts[2].configDir duplicates accounts[1]", err)
			}
		})
	}
}
