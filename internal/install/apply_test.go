package install_test

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/install"
)

var applyTime = time.Date(2026, 11, 21, 9, 30, 5, 0, time.UTC)

const backupSuffix = ".loadline-20261121-093005.bak"

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// dirEntries lists the names in dir, sorted.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// symlink makes link a symbolic link to target. Where the user may make
// none, as on Windows without the privilege, the test is skipped, except in
// CI (which sets CI=true), whose runners may make them: a skip there would
// pass unseen.
func symlink(t *testing.T, target, link string) {
	t.Helper()

	err := os.Symlink(target, link)
	switch {
	case err == nil:
	case os.Getenv("CI") == "true":
		t.Fatalf("cannot create symlinks in CI: %v", err)
	default:
		t.Skipf("cannot create symlinks here: %v", err)
	}
}

func TestApply_BackupAndAtomicRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := readFile(t, "testdata/settings_owner.in.json")
	out := readFile(t, "testdata/settings_owner.out.json")
	writeFile(t, path, original)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
	}

	backup, err := install.Apply(path, original, out, applyTime)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if want := path + backupSuffix; backup != want {
		t.Errorf("backup = %s, want %s", backup, want)
	}
	if got := readFile(t, backup); !bytes.Equal(got, original) {
		t.Error("the backup does not hold the original bytes")
	}
	if got := readFile(t, path); !bytes.Equal(got, out) {
		t.Error("the settings file does not hold the new bytes")
	}
	if got, want := dirEntries(t, dir), []string{"settings.json", "settings.json" + backupSuffix}; !slices.Equal(got, want) {
		t.Errorf("dir holds %q, want %q and no temporary files", got, want)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{path, backup} {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Errorf("%s has mode %o, want the original 640 narrowed to the owner, 600", p, got)
			}
		}
	}
}

// Settings and their backups hold the ingest token, so whatever the file's
// mode, group and others lose every permission and the owner keeps theirs.
func TestPrivateMode(t *testing.T) {
	for mode, want := range map[fs.FileMode]fs.FileMode{
		0o644: 0o600,
		0o640: 0o600,
		0o666: 0o600,
		0o600: 0o600,
		0o755: 0o700,
		0o444: 0o400,
		0o777: 0o700,
		0o000: 0o000,
	} {
		if got := install.PrivateMode(mode); got != want {
			t.Errorf("PrivateMode(%o) = %o, want %o", mode, got, want)
		}
	}
}

func TestApply_RefusesWhenFileChangedSincePlan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := readFile(t, "testdata/settings_owner.in.json")
	out := readFile(t, "testdata/settings_owner.out.json")
	edited := bytes.Replace(original, []byte(`"opus"`), []byte(`"sonnet"`), 1)
	writeFile(t, path, edited)

	backup, err := install.Apply(path, original, out, applyTime)

	if !errors.Is(err, install.ErrChangedSincePlan) {
		t.Fatalf("err = %v, want ErrChangedSincePlan", err)
	}
	if backup != "" {
		t.Errorf("backup = %q, want none", backup)
	}
	if got := readFile(t, path); !bytes.Equal(got, edited) {
		t.Error("Apply overwrote the newer edit")
	}
	if got := dirEntries(t, dir); !slices.Equal(got, []string{"settings.json"}) {
		t.Errorf("dir holds %q, want only settings.json", got)
	}
}

func TestApply_CreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	out := readFile(t, "testdata/settings_empty.out.json")

	backup, err := install.Apply(path, nil, out, applyTime)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if backup != "" {
		t.Errorf("backup = %q, want none for a new file", backup)
	}
	if got := readFile(t, path); !bytes.Equal(got, out) {
		t.Error("the new settings file does not hold the planned bytes")
	}
	if got := dirEntries(t, dir); !slices.Equal(got, []string{"settings.json"}) {
		t.Errorf("dir holds %q, want only settings.json", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("new file has mode %o, want 600 because it holds the token", got)
		}
	}
}

func TestApply_RefusesWhenMissingFileAppeared(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeFile(t, path, []byte(`{"model": "opus"}`))

	_, err := install.Apply(path, nil, []byte(`{}`), applyTime)

	if !errors.Is(err, install.ErrChangedSincePlan) {
		t.Fatalf("err = %v, want ErrChangedSincePlan", err)
	}
	if got := readFile(t, path); string(got) != `{"model": "opus"}` {
		t.Errorf("Apply overwrote a file that appeared after the plan: %s", got)
	}
}

func TestApply_RefusesMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "settings.json")

	if _, err := install.Apply(path, nil, []byte(`{}`), applyTime); err == nil {
		t.Error("Apply created a settings file in a directory that does not exist")
	}
}

// One second holds up to a hundred backups: <stamp>.bak, then <stamp>-2.bak
// to <stamp>-100.bak. The hundredth is one of Apply's own, kept and pruned
// like the rest, and there is no hundred and first.
func TestApply_MakesUpToAHundredBackupsInOneSecond(t *testing.T) {
	const stamp = ".loadline-20261121-093005"
	original := []byte(`{"model": "opus"}`)
	settingsWithBackups := func(t *testing.T, backups int) (dir, path string) {
		t.Helper()
		dir = t.TempDir()
		path = filepath.Join(dir, "settings.json")
		writeFile(t, path, original)
		writeFile(t, path+stamp+".bak", original)
		for n := 2; n <= backups; n++ {
			writeFile(t, fmt.Sprintf("%s%s-%d.bak", path, stamp, n), original)
		}
		return dir, path
	}

	// Apply takes milliseconds here. A search for a free name that never
	// ended would hold the suite until its timeout.
	apply := func(path string) (backup string, err error) {
		within(10*time.Second, "Apply", func() { backup, err = install.Apply(path, original, []byte(`{}`), applyTime) })
		return backup, err
	}

	t.Run("the hundredth", func(t *testing.T) {
		dir, path := settingsWithBackups(t, 99)

		backup, err := apply(path)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}

		if want := path + stamp + "-100.bak"; backup != want {
			t.Errorf("backup = %s, want %s", backup, want)
		}
		want := []string{
			"settings.json",
			"settings.json" + stamp + "-100.bak",
			"settings.json" + stamp + "-98.bak",
			"settings.json" + stamp + "-99.bak",
		}
		if got := dirEntries(t, dir); !slices.Equal(got, want) {
			t.Errorf("dir holds %q, want %q: the new backup and the two newest others", got, want)
		}
	})
	t.Run("a hundred and first", func(t *testing.T) {
		_, path := settingsWithBackups(t, 100)

		_, err := apply(path)

		if err == nil || !strings.Contains(err.Error(), "100 backups") {
			t.Errorf("Apply = %v, want it to refuse a hundred and first backup", err)
		}
		if got := readFile(t, path); !bytes.Equal(got, original) {
			t.Errorf("settings hold %s, want them left as they were", got)
		}
	})
}

func TestApply_KeepsEarlierBackupFromTheSameSecond(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	first := []byte(`{"model": "opus"}`)
	second := []byte(`{"model": "sonnet"}`)
	writeFile(t, path, first)

	firstBackup, err := install.Apply(path, first, second, applyTime)
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	secondBackup, err := install.Apply(path, second, first, applyTime)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	if firstBackup == secondBackup {
		t.Fatalf("both applies wrote the backup %s", firstBackup)
	}
	if got := readFile(t, firstBackup); !bytes.Equal(got, first) {
		t.Errorf("first backup holds %s, want %s", got, first)
	}
	if got := readFile(t, secondBackup); !bytes.Equal(got, second) {
		t.Errorf("second backup holds %s, want %s", got, second)
	}
}

func TestApply_WritesThroughASymlink(t *testing.T) {
	dotfiles := t.TempDir()
	target := filepath.Join(dotfiles, "settings.json")
	link := filepath.Join(t.TempDir(), "settings.json")
	original := []byte(`{"model": "opus"}`)
	writeFile(t, target, original)
	symlink(t, target, link)

	if _, err := install.Apply(link, original, []byte(`{}`), applyTime); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("Apply replaced the symlink with a regular file")
	}
	if got := readFile(t, target); string(got) != `{}` {
		t.Errorf("link target holds %s, want the new settings", got)
	}
	if got := dirEntries(t, dotfiles); !slices.Equal(got, []string{"settings.json"}) {
		t.Errorf("link target's dir holds %q, want only settings.json", got)
	}
}

// A hard link is no link to follow: a settings file that shares its data
// with a file elsewhere is written at its own name, the one it was given by,
// and nothing is made beside the other.
func TestApply_WritesAHardLinkedFileAtItsOwnName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	elsewhere := t.TempDir()
	original := []byte(`{"model": "opus"}`)
	writeFile(t, filepath.Join(elsewhere, "settings.json"), original)
	if err := os.Link(filepath.Join(elsewhere, "settings.json"), path); err != nil {
		t.Skipf("cannot create hard links here: %v", err)
	}

	if _, err := install.Apply(path, original, []byte(`{}`), applyTime); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := readFile(t, path); string(got) != `{}` {
		t.Errorf("%s holds %s, want the new settings", path, got)
	}
	if got, want := dirEntries(t, dir), []string{"settings.json", "settings.json" + backupSuffix}; !slices.Equal(got, want) {
		t.Errorf("dir holds %q, want %q", got, want)
	}
	if got := dirEntries(t, elsewhere); !slices.Equal(got, []string{"settings.json"}) {
		t.Errorf("the other link's dir holds %q, want only settings.json", got)
	}
	if got := readFile(t, filepath.Join(elsewhere, "settings.json")); !bytes.Equal(got, original) {
		t.Errorf("the other link holds %s, want the original settings it had", got)
	}
}

// Apply resolves a settings file as filepath.EvalSymlinks does, as it always
// has, and asks the system where the file is only when that fails. So a file
// is still written where the system cannot say, such as on a volume without
// a drive letter that the path names by its GUID name.
func TestApply_WritesAFileTheSystemCannotPlace(t *testing.T) {
	for _, tc := range []struct {
		name       string
		linkedFile bool
	}{
		{name: "plain file"},
		{name: "linked file", linkedFile: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			install.SetFinalPath(t, func(path string) (string, error) {
				return "", fmt.Errorf("resolve %s: %w", path, fsperm.ErrNoDriveLetter)
			})
			path := filepath.Join(t.TempDir(), "settings.json")
			original := []byte(`{"model": "opus"}`)
			written := path
			if tc.linkedFile {
				written = filepath.Join(t.TempDir(), "settings.json")
				writeFile(t, written, original)
				symlink(t, written, path)
			} else {
				writeFile(t, path, original)
			}

			backup, err := install.Apply(path, original, []byte(`{}`), applyTime)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			if want := path + backupSuffix; backup != want {
				t.Errorf("backup = %s, want %s", backup, want)
			}
			if got := readFile(t, written); string(got) != `{}` {
				t.Errorf("%s holds %s, want the new settings", written, got)
			}
		})
	}
}

// Where filepath.EvalSymlinks resolves a settings file, Apply writes where it
// leads, as it always has, and does not ask the system at all: whatever the
// system would say, here a file elsewhere, goes untouched.
func TestApply_AsksTheSystemOnlyWhereEvalSymlinksFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	elsewhere := t.TempDir()
	decoy := filepath.Join(elsewhere, "settings.json")
	original := []byte(`{"model": "opus"}`)
	writeFile(t, path, original)
	writeFile(t, decoy, original)
	install.SetFinalPath(t, func(string) (string, error) { return decoy, nil })

	if _, err := install.Apply(path, original, []byte(`{}`), applyTime); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := readFile(t, path); string(got) != `{}` {
		t.Errorf("%s holds %s, want the new settings", path, got)
	}
	if got := readFile(t, decoy); !bytes.Equal(got, original) {
		t.Errorf("%s, where the system would have led, holds %s, want it untouched", decoy, got)
	}
	if got := dirEntries(t, elsewhere); !slices.Equal(got, []string{"settings.json"}) {
		t.Errorf("%s holds %q, want only settings.json", elsewhere, got)
	}
}

func TestApply_RefusesWhenFileWasDeletedSincePlan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := readFile(t, "testdata/settings_owner.in.json")

	backup, err := install.Apply(path, original, readFile(t, "testdata/settings_owner.out.json"), applyTime)

	if !errors.Is(err, install.ErrChangedSincePlan) {
		t.Fatalf("err = %v, want ErrChangedSincePlan for a file deleted after the plan", err)
	}
	if backup != "" {
		t.Errorf("backup = %q, want none", backup)
	}
	if got := dirEntries(t, dir); len(got) != 0 {
		t.Errorf("dir holds %q, want the deleted settings file left deleted", got)
	}
}

// An editor can save the file while Apply writes its backup and the new
// copy; the bytes are checked once more just before the rename.
func TestApply_RefusesAnEditJustBeforeTheRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := readFile(t, "testdata/settings_owner.in.json")
	writeFile(t, path, original)
	edited := []byte(`{"model": "sonnet"}`)
	install.SetBeforeRecheck(t, func() { writeFile(t, path, edited) })

	backup, err := install.Apply(path, original, readFile(t, "testdata/settings_owner.out.json"), applyTime)

	if !errors.Is(err, install.ErrChangedSincePlan) {
		t.Fatalf("err = %v, want ErrChangedSincePlan", err)
	}
	if backup != "" {
		t.Errorf("backup = %q, want none for a refused write", backup)
	}
	if got := readFile(t, path); !bytes.Equal(got, edited) {
		t.Errorf("settings hold %s, want the edit kept", got)
	}
	if got := dirEntries(t, dir); !slices.Equal(got, []string{"settings.json"}) {
		t.Errorf("dir holds %q, want no backup and no temporary file left", got)
	}
}

// A temporary file that a crashed Apply left behind can hold the token, so
// the next Apply removes those an hour old or older. Newer ones may belong to
// an Apply still running, and other files are not Loadline's.
func TestApply_RemovesItsOwnOldLeftovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := readFile(t, "testdata/settings_owner.in.json")
	writeFile(t, path, original)
	leftovers := map[string]time.Duration{
		".settings.json.loadline-1111.tmp": 2 * time.Hour,
		".settings.json.loadline-2222.tmp": 10 * time.Minute,
		".settings.json.loadline-5555.tmp": time.Hour,
		".other.json.loadline-3333.tmp":    2 * time.Hour,
		"settings.json.loadline-4444.tmp":  2 * time.Hour,
	}
	for name, age := range leftovers {
		p := filepath.Join(dir, name)
		writeFile(t, p, []byte("x-loadline-token=secret"))
		if err := os.Chtimes(p, applyTime.Add(-age), applyTime.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := install.Apply(path, original, readFile(t, "testdata/settings_owner.out.json"), applyTime); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{
		".other.json.loadline-3333.tmp",
		".settings.json.loadline-2222.tmp",
		"settings.json",
		"settings.json" + backupSuffix,
		"settings.json.loadline-4444.tmp",
	}
	if got := dirEntries(t, dir); !slices.Equal(got, want) {
		t.Errorf("dir holds %q, want %q", got, want)
	}
}

// Every backup holds the token the settings held, so Apply keeps only the
// newest three of those it made of a file. It knows its own by their names,
// and leaves every other file alone, however alike its name.
func TestApply_KeepsOnlyTheNewestThreeBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := readFile(t, "testdata/settings_owner.in.json")
	writeFile(t, path, original)
	older := []string{
		"settings.json.loadline-20251231-235959.bak",
		"settings.json.loadline-20261120-080000.bak",
		"settings.json.loadline-20261121-093004.bak",
		"settings.json.loadline-20261121-093004-2.bak",
		"settings.json.loadline-20261121-093004-10.bak",
	}
	others := []string{
		"settings.json.bak",
		"settings.json.loadline-mine.bak",
		"settings.json.loadline-20261121-0930.bak",
		"settings.json.loadline-20261399-000000.bak",
		"settings.json.loadline-20261121-093003-1.bak",
		"settings.json.loadline-20261121-093003-02.bak",
		"settings.json.loadline-20261121-093003-101.bak",
		"settings.json.loadline-20261121-093003.bak.old",
		"other.json.loadline-20200101-000000.bak",
		"Xsettings.json.loadline-20200101-000000.bak",
	}
	for _, name := range append(slices.Clone(older), others...) {
		writeFile(t, filepath.Join(dir, name), []byte("x-loadline-token=secret"))
	}
	if err := os.Mkdir(filepath.Join(dir, "settings.json.loadline-20200101-000000.bak"), 0o700); err != nil {
		t.Fatal(err)
	}

	backup, err := install.Apply(path, original, readFile(t, "testdata/settings_owner.out.json"), applyTime)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := append([]string{
		"settings.json",
		filepath.Base(backup),
		"settings.json.loadline-20200101-000000.bak",
		"settings.json.loadline-20261121-093004-10.bak",
		"settings.json.loadline-20261121-093004-2.bak",
	}, others...)
	slices.Sort(want)
	if got := dirEntries(t, dir); !slices.Equal(got, want) {
		t.Errorf("dir holds %q, want %q", got, want)
	}
}

func TestApply_CreatingTheFileKeepsOnlyTheNewestThreeBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	for _, name := range []string{
		"settings.json.loadline-20261101-000000.bak",
		"settings.json.loadline-20261102-000000.bak",
		"settings.json.loadline-20261103-000000.bak",
		"settings.json.loadline-20261104-000000.bak",
	} {
		writeFile(t, filepath.Join(dir, name), []byte("x-loadline-token=secret"))
	}

	if _, err := install.Apply(path, nil, []byte(`{}`), applyTime); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{
		"settings.json",
		"settings.json.loadline-20261102-000000.bak",
		"settings.json.loadline-20261103-000000.bak",
		"settings.json.loadline-20261104-000000.bak",
	}
	if got := dirEntries(t, dir); !slices.Equal(got, want) {
		t.Errorf("dir holds %q, want %q", got, want)
	}
}

func TestApply_ARefusedWriteRemovesNoBackup(t *testing.T) {
	backups := []string{
		"settings.json.loadline-20261101-000000.bak",
		"settings.json.loadline-20261102-000000.bak",
		"settings.json.loadline-20261103-000000.bak",
		"settings.json.loadline-20261104-000000.bak",
	}
	original := []byte(`{"model": "opus"}`)
	edited := []byte(`{"model": "sonnet"}`)
	tests := map[string]func(t *testing.T, path string){
		"changed before Apply": func(t *testing.T, path string) {
			t.Helper()
			writeFile(t, path, edited)
		},
		"changed just before the rename": func(t *testing.T, path string) {
			t.Helper()
			writeFile(t, path, original)
			install.SetBeforeRecheck(t, func() { writeFile(t, path, edited) })
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json")
			for _, backup := range backups {
				writeFile(t, filepath.Join(dir, backup), []byte("x-loadline-token=secret"))
			}
			change(t, path)

			if _, err := install.Apply(path, original, []byte(`{}`), applyTime); !errors.Is(err, install.ErrChangedSincePlan) {
				t.Fatalf("Apply = %v, want ErrChangedSincePlan", err)
			}

			if got, want := dirEntries(t, dir), append([]string{"settings.json"}, backups...); !slices.Equal(got, want) {
				t.Errorf("dir holds %q, want %q", got, want)
			}
		})
	}
}

// A clock that was set back leaves backups that look newer than the one
// Apply makes now, which it reports and so must keep.
func TestApply_KeepsTheBackupItJustMade(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := readFile(t, "testdata/settings_owner.in.json")
	writeFile(t, path, original)
	for _, name := range []string{
		"settings.json.loadline-20991231-235957.bak",
		"settings.json.loadline-20991231-235958.bak",
		"settings.json.loadline-20991231-235959.bak",
	} {
		writeFile(t, filepath.Join(dir, name), []byte("x-loadline-token=secret"))
	}

	backup, err := install.Apply(path, original, readFile(t, "testdata/settings_owner.out.json"), applyTime)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{
		"settings.json",
		filepath.Base(backup),
		"settings.json.loadline-20991231-235958.bak",
		"settings.json.loadline-20991231-235959.bak",
	}
	if got := dirEntries(t, dir); !slices.Equal(got, want) {
		t.Errorf("dir holds %q, want %q", got, want)
	}
}
