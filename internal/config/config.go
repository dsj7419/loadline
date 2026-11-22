// Package config locates Loadline's files on each platform and reads its
// configuration file and ingest token.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dsj7419/loadline/internal/domain"
)

// OpenMode says when the server opens the dashboard as it starts.
type OpenMode string

const (
	// OpenOncePerLogin opens the dashboard on the first start of a login
	// session and leaves later starts alone.
	OpenOncePerLogin OpenMode = "once_per_login"
	// OpenAlways opens the dashboard on every start.
	OpenAlways OpenMode = "always"
	// OpenNever never opens the dashboard.
	OpenNever OpenMode = "never"
)

var openModes = []OpenMode{OpenOncePerLogin, OpenAlways, OpenNever}

const (
	minPort = 1024
	maxPort = 65535
)

// Config is the user-editable configuration. Every field has a default, so a
// config file only needs the keys it changes.
type Config struct {
	// Port is the loopback port the dashboard and ingest endpoint listen on.
	Port int `json:"port"`
	// OpenOnStart says when the dashboard opens as the server starts.
	OpenOnStart OpenMode `json:"openOnStart"`
	// Browser is "auto" for the system default, a well-known browser name, or
	// the path of a browser executable.
	Browser string `json:"browser"`
	// AppWindow opens the dashboard in a Chromium app window, without tabs or
	// an address bar.
	AppWindow bool `json:"appWindow"`
	// IdleExitMinutes is how long the server waits with no live session
	// before it exits. Zero means never.
	IdleExitMinutes int `json:"idleExitMinutes"`
	// RetentionDays is how long recorded history is kept.
	RetentionDays int `json:"retentionDays"`
	// ClaudePath is the claude executable to run. Empty means look it up on
	// PATH.
	ClaudePath string           `json:"claudePath"`
	Alerts     AlertSettings    `json:"alerts"`
	Accounts   []AccountSetting `json:"accounts"`
}

// AlertSettings holds the thresholds that raise an alert.
type AlertSettings struct {
	// WeeklyPct is the weekly rate-limit usage, in percent, that alerts.
	WeeklyPct float64 `json:"weeklyPct"`
	// ContextPct is the context-window usage, in percent, that alerts.
	ContextPct float64 `json:"contextPct"`
	// NeedsYouSeconds is how long a session may wait for the user before it
	// alerts.
	NeedsYouSeconds int `json:"needsYouSeconds"`
}

// AccountSetting describes one Claude Code account by its config directory.
type AccountSetting struct {
	ConfigDir string `json:"configDir"`
	// Label is the name shown for the account. Empty means derive one.
	Label string `json:"label"`
	// DailyBudgetUSD is the daily spend that is considered a full budget for
	// an API account. Zero means no budget.
	DailyBudgetUSD float64 `json:"dailyBudgetUSD"`
}

// Defaults returns the configuration used when no file overrides it.
func Defaults() Config {
	return Config{
		Port:            8289,
		OpenOnStart:     OpenOncePerLogin,
		Browser:         "auto",
		AppWindow:       true,
		IdleExitMinutes: 30,
		RetentionDays:   30,
		Alerts: AlertSettings{
			WeeklyPct:       90,
			ContextPct:      85,
			NeedsYouSeconds: 20,
		},
	}
}

// utf8BOM is the byte-order mark some Windows editors start a file with.
var utf8BOM = []byte("\xef\xbb\xbf")

// Load reads the config file at path. A missing file gives Defaults. Keys the
// file sets override the defaults and every other key keeps its default value.
// A file with unknown keys or invalid values is an error. home is the user's
// home directory, which an account's configDir may start from (see
// AccountSetting.Dir).
func Load(path, home string) (Config, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := Defaults()
	if err := decodeStrict(bytes.TrimPrefix(data, utf8BOM), &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.validate(home); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// decodeStrict decodes exactly one JSON object into cfg, rejecting keys that
// Config does not define.
func decodeStrict(data []byte, cfg *Config) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("file is empty")
		}
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected content after the JSON object")
	}
	return nil
}

// ValidatePort reports whether port can be the dashboard's port: one outside
// the privileged range.
func ValidatePort(port int) error {
	if port < minPort || port > maxPort {
		return fmt.Errorf("port %d is outside %d-%d", port, minPort, maxPort)
	}
	return nil
}

// validate reports the first setting that is out of range, naming its key.
func (c Config) validate(home string) error {
	if err := ValidatePort(c.Port); err != nil {
		return err
	}
	if !slices.Contains(openModes, c.OpenOnStart) {
		return fmt.Errorf("openOnStart %q is not one of %q, %q or %q",
			c.OpenOnStart, OpenOncePerLogin, OpenAlways, OpenNever)
	}
	if strings.TrimSpace(c.Browser) == "" {
		return errors.New("browser must not be empty")
	}
	if c.IdleExitMinutes < 0 {
		return fmt.Errorf("idleExitMinutes %d is below 0", c.IdleExitMinutes)
	}
	if c.RetentionDays < 1 {
		return fmt.Errorf("retentionDays %d is below 1", c.RetentionDays)
	}
	if err := c.Alerts.validate(); err != nil {
		return err
	}
	first := make(map[domain.ConfigDir]int, len(c.Accounts))
	for i, account := range c.Accounts {
		if err := account.validate(home); err != nil {
			return fmt.Errorf("accounts[%d].%w", i, err)
		}
		dir := account.Dir(home)
		if j, seen := first[dir]; seen {
			return fmt.Errorf("accounts[%d].configDir duplicates accounts[%d]", i, j)
		}
		first[dir] = i
	}
	return nil
}

func (a AlertSettings) validate() error {
	if a.WeeklyPct < 0 || a.WeeklyPct > 100 {
		return fmt.Errorf("alerts.weeklyPct %v is outside 0-100", a.WeeklyPct)
	}
	if a.ContextPct < 0 || a.ContextPct > 100 {
		return fmt.Errorf("alerts.contextPct %v is outside 0-100", a.ContextPct)
	}
	if a.NeedsYouSeconds < 1 {
		return fmt.Errorf("alerts.needsYouSeconds %d is below 1", a.NeedsYouSeconds)
	}
	return nil
}

// Dir returns the key of the account's config dir: ConfigDir, with a leading
// ~/ or ~\ read as home, normalized as ResolveConfigDir normalizes
// CLAUDE_CONFIG_DIR. Unlike that variable, which Claude Code reads without
// expanding it, a config file is written by hand, where ~ is the usual way
// to name the home directory. ConfigDir itself stays as the user wrote it,
// for display.
func (a AccountSetting) Dir(home string) domain.ConfigDir {
	dir := a.ConfigDir
	if rest, fromHome := cutHomePrefix(dir); fromHome {
		dir = filepath.Join(home, rest)
	}
	return ResolveConfigDir(dir, home)
}

// cutHomePrefix returns dir after a leading ~/ or ~\, and whether it had one.
func cutHomePrefix(dir string) (string, bool) {
	for _, prefix := range []string{"~/", `~\`} {
		if rest, ok := strings.CutPrefix(dir, prefix); ok {
			return rest, true
		}
	}
	return dir, false
}

// validate's errors start with the key within the account, so the caller
// prefixes them with the account's position. A relative configDir is refused
// rather than resolved against whatever directory the server started in.
func (a AccountSetting) validate(home string) error {
	if strings.TrimSpace(a.ConfigDir) == "" {
		return errors.New("configDir must not be empty")
	}
	_, fromHome := cutHomePrefix(a.ConfigDir)
	switch {
	case fromHome && home == "":
		return fmt.Errorf("configDir %q starts from the home directory, which is not known", a.ConfigDir)
	case !fromHome && !filepath.IsAbs(a.ConfigDir):
		return fmt.Errorf("configDir %q must be an absolute path or start with ~/", a.ConfigDir)
	}
	if a.DailyBudgetUSD < 0 {
		return fmt.Errorf("dailyBudgetUSD %v is below 0", a.DailyBudgetUSD)
	}
	return nil
}
