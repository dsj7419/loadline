package doctor_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/windows"

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

// A link at the settings file is followed to where install writes the token
// even through a linked folder above it, such as a home folder linked by a
// junction. A junction stands in for the file's symbolic link here, which
// would take a privilege to make: both stand for another path, and the
// doctor follows both alike.
func TestSymlinked_FollowsTheFileThroughALinkedFolderAbove(t *testing.T) {
	root := t.TempDir()
	linkFolder(t, filepath.Join(root, "home"), filepath.Join(root, "dotfiles", "home"))
	elsewhere := filepath.Join(root, "elsewhere", "settings")
	settings := filepath.Join(root, "home", ".claude", "settings.json")
	linkFolder(t, settings, elsewhere)
	resolved, err := filepath.EvalSymlinks(elsewhere)
	if err != nil {
		t.Fatal(err)
	}

	got := doctor.Symlinked(settings)

	assertChecks(t, got, []doctor.Check{{
		Name:   "token in " + settings,
		Status: doctor.Warn,
		Detail: "the file is a symlink to " + resolved + ", so install writes the ingest token there; " +
			"keep that file out of any repository you share",
	}})
}

// A link of a kind Go cannot read, such as a symbolic link of the Windows
// Subsystem for Linux, still stands for another path. Where it points cannot
// be named, but why it cannot be followed can.
func TestSymlinked_SaysWhyALinkItCannotReadCannotBeFollowed(t *testing.T) {
	settings := settingsWithTag(t, fspermtest.TagLXSymlink, fsperm.NameSurrogate)

	got := doctor.Symlinked(settings)

	assertChecks(t, got, []doctor.Check{{
		Name:   "token in " + settings,
		Status: doctor.Warn,
		Detail: "the file is a symlink that cannot be followed: " + windows.ERROR_CANT_ACCESS_FILE.Error(),
	}})
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

// A Git for Windows installed for one user, with its cmd folder on PATH, and
// a status line that works, in the Git Bash form, under the Claude Code that
// PowerShell, cmd or the Start menu starts. Run inside Git Bash, or through
// Claude Code's own Bash tool, the doctor sees Git Bash's own folders at the
// front of PATH, among them mingw64\bin with a git.exe and no bin\bash.exe
// beside it. It must judge the settings as it does from any other terminal,
// not call them unwired and send the user to an install that would rewrite
// them for PowerShell.
func TestDoctor_InGitBashLeavesAWorkingStatusLineAlone(t *testing.T) {
	const git = `C:\Users\example\AppData\Local\Programs\Git`
	files := []string{
		git + `\cmd\git.exe`, git + `\bin\bash.exe`, git + `\mingw64\bin\git.exe`,
		git + `\usr\bin\bash.exe`, git + `\usr\bin\msys-2.0.dll`,
	}
	env := map[string]string{
		"MSYSTEM": "MINGW64",
		"EXEPATH": git + `\bin`,
		"PATH": `C:\Users\example\bin;` + git + `\mingw64\bin;` + git + `\usr\local\bin;` + git + `\usr\bin;` +
			`C:\Windows\system32;C:\Windows;` + git + `\cmd`,
	}
	w := newWorld(t)
	w.install(w.settings, `{"statusLine": {"type": "command", "command": "~/.claude/statusline.sh"}}`, install.ShellBash)
	w.deps.Getenv = func(name string) string { return env[name] }
	w.deps.IsFile = func(path string) bool { return slices.Contains(files, path) }
	w.deps.WorkDir = `C:\Users\example`
	// As the doctor command wires it: the shell install would choose here.
	w.deps.Shell = func(settings []byte) install.Shell {
		return install.DetectShell(install.SettingsEnv(settings, w.deps.Getenv), w.deps.IsFile)
	}

	got := w.run()

	assertChecks(t, settingsChecks(t, got), []doctor.Check{{Name: "settings " + w.settings, Status: doctor.OK, Detail: "wired"}})
}
