package doctor_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/doctor"
	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
	"github.com/dsj7419/loadline/internal/install"
)

// linkFolder makes link a junction to the folder target, after making
// target and the folder that is to hold link. A junction needs no privilege.
func linkFolder(t *testing.T, link, target string) {
	t.Helper()
	for _, dir := range []string{target, filepath.Dir(link)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	//nolint:gosec // G204: the paths are the test's own temporary folders
	if out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
}

// A config dir that is itself a link into a dotfiles repository, a junction
// here, puts settings.json, and the token install writes into it, in that
// repository just as a linked file does.
func TestDoctor_ALinkedConfigDirWarnsLikeALinkedFile(t *testing.T) {
	w := newWorld(t)
	dotfiles := filepath.Join(t.TempDir(), "dotfiles", "claude")
	configDir := filepath.Join(t.TempDir(), ".claude")
	linkFolder(t, configDir, dotfiles)
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

// Data deduplication keeps a file's data in a store that other files on the
// volume share, and makes the file a reparse point that leads nowhere else:
// the token install writes into it stays where its name says.
func TestSymlinked_DoesNotCallADeduplicatedFileALink(t *testing.T) {
	settings := settingsWithTag(t, fspermtest.TagDedup, fsperm.Deduplicated)

	if got := doctor.Symlinked(settings); len(got) != 0 {
		t.Errorf("Symlinked(a deduplicated file):\n%swant no warning", list(got))
	}
}

// A file that a cloud service keeps in sync is a reparse point as well, and
// no link leads from it to another name either.
func TestSymlinked_DoesNotCallACloudFileALink(t *testing.T) {
	settings := settingsWithTag(t, fspermtest.TagOneDrive, fsperm.CloudFile)

	if got := doctor.Symlinked(settings); len(got) != 0 {
		t.Errorf("Symlinked(a cloud file):\n%swant no warning", list(got))
	}
}

// settingsWithTag makes a settings file a reparse point with tag, as a file
// system filter does, and checks that it reads as kind. No filter handles the
// point, so the file cannot be read through it: Run would stop at reading
// it, before it looks for links.
func settingsWithTag(t *testing.T, tag uint32, kind fsperm.ReparseKind) string {
	t.Helper()
	settings := filepath.Join(t.TempDir(), ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fspermtest.PlantReparsePoint(t, settings, tag)
	entry, err := fsperm.Inspect(settings)
	if err != nil || entry.Reparse != kind {
		t.Fatalf("Inspect(the planted file) = %+v, %v; want reparse kind %d, or the test proves nothing", entry, err, kind)
	}
	return settings
}
