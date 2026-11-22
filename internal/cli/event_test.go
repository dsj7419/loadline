package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
)

func TestLoadConfig_BadFileFallsBackToDefaults(t *testing.T) {
	paths := config.Paths{ConfigFile: filepath.Join(t.TempDir(), "config.json")}
	for name, content := range map[string]string{
		"truncated":       `{"port":`,
		"an unknown key":  `{"theme":"dark"}`,
		"a port too low":  `{"port":80}`,
		"not an object":   `[]`,
		"an empty file":   ``,
		"trailing values": `{"port":18299} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(paths.ConfigFile, []byte(content), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			if got := loadConfig(paths); !reflect.DeepEqual(got, config.Defaults()) {
				t.Errorf("loadConfig = %+v, want the defaults", got)
			}
		})
	}
}

func TestLoadConfig_ReadsAGoodFile(t *testing.T) {
	paths := config.Paths{ConfigFile: filepath.Join(t.TempDir(), "config.json")}
	if err := os.WriteFile(paths.ConfigFile, []byte(`{"port":18299}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if got := loadConfig(paths); got.Port != 18299 {
		t.Errorf("loadConfig port = %d, want the file's 18299", got.Port)
	}
}

func TestLoadConfig_MissingFileGivesDefaults(t *testing.T) {
	paths := config.Paths{ConfigFile: filepath.Join(t.TempDir(), "absent", "config.json")}

	if got := loadConfig(paths); !reflect.DeepEqual(got, config.Defaults()) {
		t.Errorf("loadConfig = %+v, want the defaults", got)
	}
}
