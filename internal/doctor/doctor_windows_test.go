package doctor_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/doctor"
	"github.com/dsj7419/loadline/internal/install"
)

// A config dir that is itself a link into a dotfiles repository, a junction
// here, puts settings.json, and the token install writes into it, in that
// repository just as a linked file does. A junction needs no privilege.
func TestDoctor_ALinkedConfigDirWarnsLikeALinkedFile(t *testing.T) {
	w := newWorld(t)
	dotfiles := filepath.Join(t.TempDir(), "dotfiles", "claude")
	if err := os.MkdirAll(dotfiles, 0o700); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(t.TempDir(), ".claude")
	//nolint:gosec // G204: the paths are the test's own temporary folders
	if out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", configDir, dotfiles).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(configDir) })
	settings := filepath.Join(configDir, "settings.json")
	w.install(settings, `{}`, install.ShellBash)
	w.deps.SettingsFiles = []string{settings}

	got := w.run()

	assertChecks(t, settingsChecks(t, got), []doctor.Check{
		{Name: "settings " + settings, Status: doctor.OK, Detail: "wired"},
		{
			Name:   "token in " + settings,
			Status: doctor.Warn,
			Detail: "its folder " + configDir + " is a link to " + dotfiles + ", so install writes the ingest token into " +
				filepath.Join(dotfiles, "settings.json") + "; keep that folder out of any repository you share",
		},
	})
}
