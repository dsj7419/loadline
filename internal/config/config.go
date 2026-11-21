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

// Load reads the config file at path. A missing file gives Defaults. Keys the
// file sets override the defaults and every other key keeps its default value.
// A file with unknown keys or invalid values is an error.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := Defaults()
	if err := decodeStrict(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
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

// validate reports the first setting that is out of range, naming its key.
func (c Config) validate() error {
	if c.Port < minPort || c.Port > maxPort {
		return fmt.Errorf("port %d is outside %d-%d", c.Port, minPort, maxPort)
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
	for i, account := range c.Accounts {
		if err := account.validate(); err != nil {
			return fmt.Errorf("accounts[%d].%w", i, err)
		}
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

// validate's errors start with the key within the account, so the caller
// prefixes them with the account's position.
func (a AccountSetting) validate() error {
	if strings.TrimSpace(a.ConfigDir) == "" {
		return errors.New("configDir must not be empty")
	}
	if a.DailyBudgetUSD < 0 {
		return fmt.Errorf("dailyBudgetUSD %v is below 0", a.DailyBudgetUSD)
	}
	return nil
}
