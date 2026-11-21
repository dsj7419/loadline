package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// platformDirs returns ~/Library/Application Support/loadline, which macOS
// uses for both state and configuration.
func platformDirs() (dataDir, configDir string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("locate Loadline directories: %w", err)
	}
	dir := filepath.Join(home, "Library", "Application Support", appDirName)
	return dir, dir, nil
}
