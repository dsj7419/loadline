package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dsj7419/loadline/internal/domain"
)

const (
	// appDirName is the directory Loadline owns inside each platform
	// location.
	appDirName = "loadline"
	// homeEnv relocates every Loadline file under one directory. Tests and
	// end-to-end runs use it to stay clear of the real user directories.
	homeEnv = "LOADLINE_HOME"
)

// Paths are the files and directories Loadline reads and writes.
type Paths struct {
	// DataDir holds Loadline's state.
	DataDir string
	// ConfigFile is the optional user configuration file.
	ConfigFile string
	// TokenFile holds the ingest token shared by the server and the hooks.
	TokenFile string
	// SpoolDir holds events the hooks could not deliver to the server.
	SpoolDir string
	// DBFile is the SQLite database.
	DBFile string
}

// DefaultPaths returns the paths for this platform and user. When
// LOADLINE_HOME is set, everything lives under that absolute directory
// instead. It creates nothing; callers create the directories they write to.
func DefaultPaths() (Paths, error) {
	if home := os.Getenv(homeEnv); home != "" {
		if !filepath.IsAbs(home) {
			return Paths{}, fmt.Errorf("%s must be an absolute path, got %q", homeEnv, home)
		}
		home = filepath.Clean(home)
		return layout(home, home), nil
	}

	dataDir, configDir, err := platformDirs()
	if err != nil {
		return Paths{}, err
	}
	return layout(dataDir, configDir), nil
}

func layout(dataDir, configDir string) Paths {
	return Paths{
		DataDir:    dataDir,
		ConfigFile: filepath.Join(configDir, "config.json"),
		TokenFile:  filepath.Join(dataDir, "token"),
		SpoolDir:   filepath.Join(dataDir, "spool"),
		DBFile:     filepath.Join(dataDir, "loadline.db"),
	}
}

// ResolveConfigDir turns the value of CLAUDE_CONFIG_DIR into the key that
// identifies an account. An empty value means Claude Code's default,
// <home>/.claude. Like Claude Code, it does not expand "~", so a relative
// value resolves against the working directory. The key is cleaned, and on
// Windows, where paths ignore case, it is also lower-cased, so that every
// spelling of one directory maps to the same key.
func ResolveConfigDir(envValue, home string) domain.ConfigDir {
	dir := envValue
	if dir == "" {
		dir = filepath.Join(home, ".claude")
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		// The working directory is gone. A cleaned relative path still keys
		// the account consistently within this process.
		abs = filepath.Clean(dir)
	}
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return domain.ConfigDir(abs)
}
