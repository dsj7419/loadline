package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// platformDirs returns Loadline's state directory under %LOCALAPPDATA% and its
// configuration directory under %APPDATA%.
func platformDirs() (dataDir, configDir string, err error) {
	local, err := requireEnv("LOCALAPPDATA")
	if err != nil {
		return "", "", err
	}
	roaming, err := requireEnv("APPDATA")
	if err != nil {
		return "", "", err
	}
	return filepath.Join(local, appDirName), filepath.Join(roaming, appDirName), nil
}

func requireEnv(name string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", fmt.Errorf("locate Loadline directories: %%%s%% is not set", name)
	}
	return value, nil
}
