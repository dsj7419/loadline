//go:build !windows

package doctor_test

import (
	"os"
	"testing"

	"github.com/dsj7419/loadline/internal/doctor"
)

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
