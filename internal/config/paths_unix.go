//go:build !windows && !darwin

package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// platformDirs follows the XDG base directory specification: state under
// $XDG_STATE_HOME (default ~/.local/state) and configuration under
// $XDG_CONFIG_HOME (default ~/.config).
func platformDirs() (dataDir, configDir string, err error) {
	state, err := xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return "", "", err
	}
	cfg, err := xdgDir("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return "", "", err
	}
	return filepath.Join(state, appDirName), filepath.Join(cfg, appDirName), nil
}

// xdgDir returns the directory named by the environment variable, or
// fallback under the home directory. The specification says to ignore a value
// that is not an absolute path.
func xdgDir(envName, fallback string) (string, error) {
	if dir := os.Getenv(envName); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate Loadline directories: %w", err)
	}
	return filepath.Join(home, fallback), nil
}
