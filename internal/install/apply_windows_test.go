package install_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/install"
)

// A home folder or a Claude Code config folder reached through a junction,
// as when a profile or a dotfiles folder is moved to another drive, takes the
// settings just as a folder reached directly does: the file the junction
// leads to is written, and its backup goes beside it. A settings file that
// is itself a symbolic link below such a junction is written through, and
// its backup goes beside the link, as for a link with no junction above it.
// Making a symbolic link takes a privilege that a junction does not, so
// those rows skip without it, outside CI.
func TestApply_WritesThroughAJunctionAbove(t *testing.T) {
	for _, tc := range []struct {
		name string
		// junctioned is the folder, below the test's root, that a junction
		// stands for.
		junctioned string
		linkedFile bool
	}{
		{name: "config folder", junctioned: filepath.Join("home", ".claude")},
		{name: "home folder", junctioned: "home"},
		{name: "config folder and linked file", junctioned: filepath.Join("home", ".claude"), linkedFile: true},
		{name: "home folder and linked file", junctioned: "home", linkedFile: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			moved := filepath.Join(root, "moved")
			// configDir is where the config folder really is.
			configDir := filepath.Join(moved, "home", ".claude")
			link := filepath.Join(root, tc.junctioned)
			for _, dir := range []string{configDir, filepath.Dir(link)} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			junction(t, link, filepath.Join(moved, tc.junctioned))
			original := []byte(`{"model": "opus"}`)
			out := []byte(`{}`)
			settings := filepath.Join(configDir, "settings.json")
			// written is the file Apply must write: settings, or the file
			// it links to.
			written := settings
			dotfiles := filepath.Join(root, "dotfiles")
			if tc.linkedFile {
				if err := os.MkdirAll(dotfiles, 0o700); err != nil {
					t.Fatal(err)
				}
				written = filepath.Join(dotfiles, "settings.json")
				writeFile(t, written, original)
				symlink(t, written, settings)
			} else {
				writeFile(t, settings, original)
			}
			path := filepath.Join(root, "home", ".claude", "settings.json")

			backup, err := install.Apply(path, original, out, applyTime)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			if want := path + backupSuffix; backup != want {
				t.Errorf("backup = %s, want %s", backup, want)
			}
			if got := readFile(t, written); !bytes.Equal(got, out) {
				t.Errorf("%s holds %s, want the new settings", written, got)
			}
			if got := readFile(t, backup); !bytes.Equal(got, original) {
				t.Errorf("the backup holds %s, want the original settings", got)
			}
			if got, want := dirEntries(t, configDir), []string{"settings.json", "settings.json" + backupSuffix}; !slices.Equal(got, want) {
				t.Errorf("the config folder holds %q, want %q and no temporary files", got, want)
			}
			if !tc.linkedFile {
				return
			}
			if got := dirEntries(t, dotfiles); !slices.Equal(got, []string{"settings.json"}) {
				t.Errorf("the link target's folder holds %q, want only settings.json", got)
			}
			info, err := os.Lstat(settings)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Error("Apply replaced the symlink with a regular file")
			}
		})
	}
}

// Where filepath.EvalSymlinks fails at a junction and the system cannot say
// where the file is either, as for a junction to a volume without a drive
// letter, Apply writes nothing, and the system's reason is the one it gives:
// EvalSymlinks' own, "The system cannot find the path specified.", would not
// say why.
func TestApply_GivesTheSystemsReasonWhenNeitherWayResolves(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "moved", "claude")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".claude")
	junction(t, link, configDir)
	original := []byte(`{"model": "opus"}`)
	writeFile(t, filepath.Join(configDir, "settings.json"), original)
	path := filepath.Join(link, "settings.json")
	systemsReason := fmt.Errorf("resolve %s: %w", path, fsperm.ErrNoDriveLetter)
	install.SetFinalPath(t, func(string) (string, error) { return "", systemsReason })

	backup, err := install.Apply(path, original, []byte(`{}`), applyTime)

	if backup != "" || !errors.Is(err, fsperm.ErrNoDriveLetter) || err.Error() != systemsReason.Error() {
		t.Errorf("Apply = %q, %v; want no backup and the error %q", backup, err, systemsReason)
	}
	if got := readFile(t, filepath.Join(configDir, "settings.json")); !bytes.Equal(got, original) {
		t.Errorf("the settings file holds %s, want it untouched", got)
	}
	if got := dirEntries(t, configDir); !slices.Equal(got, []string{"settings.json"}) {
		t.Errorf("the config folder holds %q, want only settings.json", got)
	}
}
