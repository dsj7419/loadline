package install_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/install"
)

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "loadline.exe"
	}
	return "loadline"
}

func writeBinary(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "source-binary")
	writeFile(t, path, []byte(content))
	return path
}

func selfInstall(t *testing.T, src, dataDir string) string {
	t.Helper()

	got, err := install.SelfInstall(src, dataDir)
	if err != nil {
		t.Fatalf("SelfInstall: %v", err)
	}
	return got
}

func TestSelfInstall_CopiesTheBinary(t *testing.T) {
	dataDir := t.TempDir()
	src := writeBinary(t, "loadline v1")

	got := selfInstall(t, src, dataDir)

	target := filepath.Join(dataDir, "bin", binaryName())
	if want := filepath.ToSlash(target); got != want {
		t.Errorf("SelfInstall = %s, want %s", got, want)
	}
	if strings.Contains(got, `\`) {
		t.Errorf("SelfInstall = %s, want forward slashes", got)
	}
	if content := readFile(t, target); string(content) != "loadline v1" {
		t.Errorf("installed binary holds %q", content)
	}
	if entries := dirEntries(t, filepath.Dir(target)); !slices.Equal(entries, []string{binaryName()}) {
		t.Errorf("bin dir holds %q, want only the binary", entries)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Errorf("installed binary has mode %o, want it executable", info.Mode().Perm())
		}
	}
}

func TestSelfInstall_InstalledPathNamesTheTarget(t *testing.T) {
	dataDir := t.TempDir()

	planned := install.InstalledPath(dataDir)
	got := selfInstall(t, writeBinary(t, "loadline"), dataDir)

	if planned != got {
		t.Errorf("InstalledPath = %s, SelfInstall = %s, want them equal", planned, got)
	}
}

func TestSelfInstall_SkipsAnIdenticalTarget(t *testing.T) {
	dataDir := t.TempDir()
	src := writeBinary(t, "loadline v1")
	target := filepath.Join(dataDir, "bin", binaryName())
	selfInstall(t, src, dataDir)
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatal(err)
	}

	selfInstall(t, src, dataDir)

	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("target was rewritten at %s although it was identical", info.ModTime())
	}
}

func TestSelfInstall_ReplacesAnOlderBinary(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "bin", binaryName())
	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)

	selfInstall(t, writeBinary(t, "loadline v2, which is longer"), dataDir)

	if content := readFile(t, target); string(content) != "loadline v2, which is longer" {
		t.Errorf("installed binary holds %q, want v2", content)
	}
	if entries := dirEntries(t, filepath.Dir(target)); !slices.Equal(entries, []string{binaryName()}) {
		t.Errorf("bin dir holds %q, want only the binary", entries)
	}
}

func TestSelfInstall_ReplacesABinaryOfTheSameSize(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "bin", binaryName())
	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)

	selfInstall(t, writeBinary(t, "loadline v2"), dataDir)

	if content := readFile(t, target); !bytes.Equal(content, []byte("loadline v2")) {
		t.Errorf("installed binary holds %q, want v2", content)
	}
}

func TestPlanSelfInstall(t *testing.T) {
	dataDir := t.TempDir()
	src := writeBinary(t, "loadline v2")
	target := filepath.Join(dataDir, "bin", binaryName())
	path := "bin/" + binaryName()

	missing, err := install.PlanSelfInstall(src, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0].Path != path || missing[0].Action != install.ActionAdd || !strings.Contains(missing[0].Detail, src) {
		t.Errorf("without a binary: %+v, want add %s naming the source", missing, path)
	}

	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)
	older, err := install.PlanSelfInstall(src, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(older) != 1 || older[0].Path != path || older[0].Action != install.ActionUpdate {
		t.Errorf("with an older binary: %+v, want update %s", older, path)
	}

	selfInstall(t, src, dataDir)
	current, err := install.PlanSelfInstall(src, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 0 {
		t.Errorf("with the same binary: %+v, want no change", current)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("planning removed the binary: %v", err)
	}

	if _, err := install.PlanSelfInstall(filepath.Join(t.TempDir(), "missing"), dataDir); err == nil {
		t.Error("PlanSelfInstall succeeded without a source binary")
	}
}

func TestSelfInstall_MissingSourceFails(t *testing.T) {
	dataDir := t.TempDir()

	if _, err := install.SelfInstall(filepath.Join(t.TempDir(), "missing"), dataDir); err == nil {
		t.Fatal("SelfInstall succeeded without a source binary")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "bin", binaryName())); err == nil {
		t.Error("SelfInstall left a target behind")
	}
}

// A source that opens but cannot be read, as a directory does, must fail the
// install and leave nothing in bin: an empty file in the binary's place would
// break every hook.
func TestSelfInstall_ReportsACopyThatFails(t *testing.T) {
	dataDir := t.TempDir()
	unreadable := t.TempDir()

	got, err := install.SelfInstall(unreadable, dataDir)

	if err == nil {
		t.Errorf("SelfInstall = %q, want an error for a source that cannot be read", got)
	}
	if entries := dirEntries(t, filepath.Join(dataDir, "bin")); len(entries) != 0 {
		t.Errorf("bin holds %q after a failed copy, want nothing", entries)
	}
}

// The binary's folder must be what the data directory's check takes for
// Loadline's own.
func TestInstalledPath_TheDataDirectoryOwnsItsFolder(t *testing.T) {
	bin := filepath.Base(filepath.Dir(filepath.FromSlash(install.InstalledPath(t.TempDir()))))

	if !datadir.Owns(bin) {
		t.Errorf("datadir.Owns(%q) = false, want true", bin)
	}
}

// The copy of the binary belongs to its install until it is moved in, and
// the preparation of the data directory, which other installs and the
// server run meanwhile, must know it for one.
func TestSelfInstall_WritesFirstToAFileTheDataDirectoryLeavesToItsWriter(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "bin", binaryName())
	var temps []string
	install.SetRename(t, func(from, to string) error {
		if to == target {
			temps = append(temps, from)
		}
		return os.Rename(from, to)
	})

	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)

	if len(temps) != 1 || filepath.Dir(temps[0]) != filepath.Dir(target) || !datadir.Temporary(filepath.Base(temps[0])) {
		t.Errorf("SelfInstall wrote the binary first to %q, want one file beside %s that datadir.Temporary takes", temps, target)
	}
}

// asideLayout fills a data directory's bin folder with the binaries upgrades
// moved aside, which are the regular files named loadline.exe.old and
// loadline.exe.old-<n> for n from 1 to 100, among things that only look like
// them. It returns the data directory, those binaries in the order of their
// names, and everything else bin and the folder beside it hold, with its
// content ("" for a folder or a link).
func asideLayout(t *testing.T) (dataDir string, aside []string, others map[string]string) {
	t.Helper()

	dataDir = t.TempDir()
	target := filepath.Join(dataDir, "bin", binaryName())
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	aside = []string{target + ".old", target + ".old-1", target + ".old-7", target + ".old-100"}
	for _, path := range aside {
		writeFile(t, path, []byte("a binary an upgrade replaced"))
	}

	outside := filepath.Join(dataDir, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	others = map[string]string{
		target:              "the installed binary",
		target + ".old-x":   "not a number",
		target + ".old-0":   "numbered from 1",
		target + ".old-101": "past the last name",
		target + ".older":   "another name",
		filepath.Join(dataDir, "bin", "notes.txt"):  "the user's",
		filepath.Join(target+".old-2", "inside"):    "in a folder",
		filepath.Join(outside, binaryName()+".old"): "where a link leads",
	}
	if err := os.Mkdir(target+".old-2", 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range others {
		writeFile(t, path, []byte(content))
	}
	others[target+".old-2"] = ""
	link(t, target+".old-3", filepath.Join(outside, binaryName()+".old"))
	others[target+".old-3"] = ""
	return dataDir, aside, others
}

// link makes path a symbolic link to target. Where the user may make none,
// as on Windows outside Developer Mode, it makes a directory junction to
// target's folder, or to target when it is one, which Lstat takes for a link
// all the same.
func link(t *testing.T, path, target string) {
	t.Helper()

	err := os.Symlink(target, path)
	if err == nil {
		return
	}
	if runtime.GOOS != "windows" {
		t.Fatalf("link %s to %s: %v", path, target, err)
	}
	if info, statErr := os.Stat(target); statErr != nil || !info.IsDir() {
		target = filepath.Dir(target)
	}
	//nolint:gosec // G204: the paths are the test's own temporary folders
	if out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", path, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J %s %s: %v %s", path, target, err, out)
	}
}

// assertUntouched fails unless every entry of others is still there, each
// file with its content.
func assertUntouched(t *testing.T, others map[string]string) {
	t.Helper()

	for path, content := range others {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("%s is gone: %v", path, err)
			continue
		}
		if content == "" {
			continue
		}
		if got := readFile(t, path); string(got) != content {
			t.Errorf("%s holds %q, want %q", path, got, content)
		}
	}
}

func TestRemoveAside_RemovesOnlyTheBinariesAnUpgradeMovedAside(t *testing.T) {
	dataDir, aside, others := asideLayout(t)

	removed, err := install.RemoveAside(dataDir)

	if err != nil || !slices.Equal(removed, aside) {
		t.Errorf("RemoveAside = %q, %v; want %q and no error", removed, err, aside)
	}
	for _, path := range aside {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still there (%v)", path, err)
		}
	}
	assertUntouched(t, others)
}

// A dry run of install names what RemoveAside would remove, and must not
// remove it.
func TestAsideBinaries_FindsWhatRemoveAsideRemovesAndChangesNothing(t *testing.T) {
	dataDir, aside, others := asideLayout(t)

	found, err := install.AsideBinaries(dataDir)

	if err != nil || !slices.Equal(found, aside) {
		t.Errorf("AsideBinaries = %q, %v; want %q and no error", found, err, aside)
	}
	for _, path := range aside {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("AsideBinaries removed %s: %v", path, err)
		}
	}
	assertUntouched(t, others)
}

// Before anything is installed there is no bin folder, and nothing to do.
func TestRemoveAside_FindsNothingWithoutABinFolder(t *testing.T) {
	dataDir := t.TempDir()

	removed, err := install.RemoveAside(dataDir)

	if err != nil || len(removed) != 0 {
		t.Errorf("RemoveAside = %q, %v; want nothing removed and no error", removed, err)
	}
	if entries := dirEntries(t, dataDir); len(entries) != 0 {
		t.Errorf("RemoveAside left %q in the data directory, want nothing", entries)
	}
}

// A bin folder that is a link leads to files that are not Loadline's.
func TestRemoveAside_RefusesABinFolderThatIsALink(t *testing.T) {
	dataDir := t.TempDir()
	elsewhere := t.TempDir()
	theirs := filepath.Join(elsewhere, binaryName()+".old")
	writeFile(t, theirs, []byte("someone else's"))
	link(t, filepath.Join(dataDir, "bin"), elsewhere)

	found, findErr := install.AsideBinaries(dataDir)
	removed, removeErr := install.RemoveAside(dataDir)

	for name, err := range map[string]error{"AsideBinaries": findErr, "RemoveAside": removeErr} {
		if err == nil || !strings.Contains(err.Error(), "is a link") {
			t.Errorf("%s: err = %v, want the bin folder refused as a link", name, err)
		}
	}
	if len(found) != 0 || len(removed) != 0 {
		t.Errorf("found %q and removed %q through a link, want nothing", found, removed)
	}
	if content := readFile(t, theirs); string(content) != "someone else's" {
		t.Errorf("%s holds %q after RemoveAside", theirs, content)
	}
}

// A removal that fails for another reason than a running binary is
// reported, once the rest are removed.
func TestRemoveAside_ReportsWhatItCannotRemoveAndRemovesTheRest(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "bin", binaryName())
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, target+".old", []byte("a binary an upgrade replaced"))
	writeFile(t, target+".old-1", []byte("another"))
	failure := errors.New("the disk is failing")
	install.SetRemove(t, func(path string) error {
		if path == target+".old" {
			return &fs.PathError{Op: "remove", Path: path, Err: failure}
		}
		return os.Remove(path)
	})

	removed, err := install.RemoveAside(dataDir)

	if !errors.Is(err, failure) || !strings.Contains(err.Error(), target+".old") {
		t.Errorf("err = %v, want the failure, naming %s.old", err, target)
	}
	if !slices.Equal(removed, []string{target + ".old-1"}) {
		t.Errorf("RemoveAside removed %q, want %s.old-1", removed, target)
	}
	if _, err := os.Lstat(target + ".old"); err != nil {
		t.Errorf("the binary that could not be removed is gone: %v", err)
	}
}

// An install and a server that start together both look for the binaries
// an upgrade moved aside. One removes each, and the other finds it gone,
// which is what it wanted: no failure, and nothing it removed.
func TestRemoveAside_TakesABinaryRemovedMeanwhileForNoFailure(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "bin", binaryName())
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, target+".old", []byte("a binary an upgrade replaced"))
	install.SetRemove(t, func(path string) error {
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Remove(path)
	})

	removed, err := install.RemoveAside(dataDir)

	if err != nil || len(removed) != 0 {
		t.Errorf("RemoveAside = %q, %v; want nothing it removed and no error", removed, err)
	}
}
