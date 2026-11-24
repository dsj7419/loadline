//go:build !windows

package doctor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/doctor"
)

// linkFolder makes link a symbolic link to the folder target, after making
// target and the folder that is to hold link.
func linkFolder(t *testing.T, link, target string) {
	t.Helper()
	for _, dir := range []string{target, filepath.Dir(link)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// Claude Code rewrites settings.json when the user changes a setting in it,
// and the mode it leaves is its own choice. The file holds the ingest token.
func TestDoctor_SettingsOthersCanReadWarn(t *testing.T) {
	w := newWorld(t)
	if err := os.Chmod(w.settings, 0o644); err != nil {
		t.Fatal(err)
	}

	got := w.run()

	assertChecks(t, settingsChecks(t, got), []doctor.Check{
		{Name: "settings " + w.settings, Status: doctor.OK, Detail: "wired"},
		{
			Name:   "token in " + w.settings,
			Status: doctor.Warn,
			Detail: "its mode 0644 lets group and others in, and it holds the ingest token; " +
				"a rewrite by Claude Code can leave it so; run chmod go-rwx " + w.settings,
		},
	})
}
