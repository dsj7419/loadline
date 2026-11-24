//go:build !windows

package fsperm_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/fsperm"
)

func TestPrivate_TakesGroupAndOthersOffTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loadline")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "token")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := fsperm.Private(dir); err != nil {
		t.Fatalf("Private: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %#o, want 0700", got)
	}
	info, err = os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("the file inside has mode %#o, want its own 0600 left alone", got)
	}
}

func TestPrivate_MissingDirFails(t *testing.T) {
	err := fsperm.Private(filepath.Join(t.TempDir(), "missing"))

	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Private = %v, want an error that matches fs.ErrNotExist", err)
	}
}

func TestExposure_NamesWhoElseTheModeAdmits(t *testing.T) {
	tests := []struct {
		mode fs.FileMode
		want string
	}{
		{mode: 0o700, want: ""},
		{mode: 0o600, want: ""},
		{mode: 0o750, want: "its mode 0750 also admits group"},
		{mode: 0o701, want: "its mode 0701 also admits others"},
		{mode: 0o755, want: "its mode 0755 also admits group and others"},
		{mode: 0o644, want: "its mode 0644 also admits group and others"},
	}
	for _, tt := range tests {
		path := filepath.Join(t.TempDir(), "loadline")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, tt.mode); err != nil {
			t.Fatal(err)
		}

		got, err := fsperm.Exposure(path)

		if err != nil || got != tt.want {
			t.Errorf("Exposure of mode %#o = %q, %v; want %q", tt.mode, got, err, tt.want)
		}
	}
}

func TestExposure_MissingPathFails(t *testing.T) {
	_, err := fsperm.Exposure(filepath.Join(t.TempDir(), "missing"))

	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Exposure = %v, want an error that matches fs.ErrNotExist", err)
	}
}

func TestMkdirAll_CreatesEachMissingFolderWithMode0700(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "tools", "loadline")

	if err := fsperm.MkdirAll(dir); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	for _, path := range []string{filepath.Join(parent, "tools"), dir} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got&0o077 != 0 {
			t.Errorf("%s has mode %#o, want no permission for group or others", path, got)
		}
	}
}

func TestMovableAncestor_NamesAFolderOthersCanWriteWithoutTheStickyBit(t *testing.T) {
	top := t.TempDir()
	shared := filepath.Join(top, "shared")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		mode fs.FileMode
		want string
	}{
		{mode: 0o777, want: shared + " lets group and others rename the folders in it (mode 0777, without the sticky bit)"},
		{mode: 0o770, want: shared + " lets group rename the folders in it (mode 0770, without the sticky bit)"},
		{mode: 0o703, want: shared + " lets others rename the folders in it (mode 0703, without the sticky bit)"},
		{mode: 0o777 | fs.ModeSticky, want: ""},
		{mode: 0o755, want: ""},
	}
	for _, tt := range tests {
		if err := os.Chmod(shared, tt.mode); err != nil {
			t.Fatal(err)
		}

		got, err := fsperm.MovableAncestor(filepath.Join(shared, "loadline"), top)

		if err != nil || got.Problem != tt.want {
			t.Errorf("MovableAncestor under mode %v = %+v, %v; want %q", tt.mode, got, err, tt.want)
		}
	}
}

// A symbolic link on the way puts the data directory in the folders that
// hold the link's target, which are judged too.
func TestMovableAncestor_JudgesTheFoldersThatReallyHoldTheDirectory(t *testing.T) {
	top := t.TempDir()
	shared := filepath.Join(top, "shared")
	target := filepath.Join(shared, "real")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(top, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	got, err := fsperm.MovableAncestor(filepath.Join(link, "loadline"), top)

	if want := " lets group and others rename the folders in it"; err != nil || !strings.Contains(got.Problem, " is really ") || !strings.Contains(got.Problem, want) {
		t.Errorf("MovableAncestor = %+v, %v; want it to name the folder that really holds the directory, which%s", got, err, want)
	}
}

func TestIsLink_TellsASymlinkFromAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "token")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bin")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]bool{link: true, file: false, dir: false} {
		got, err := fsperm.IsLink(path)
		if err != nil || got != want {
			t.Errorf("IsLink(%s) = %v, %v; want %v", path, got, err, want)
		}
		// A symbolic link is the only link here, and it leads to another path.
		if got, err := fsperm.IsNameSurrogate(path); err != nil || got != want {
			t.Errorf("IsNameSurrogate(%s) = %v, %v; want %v", path, got, err, want)
		}
	}
}

// Only Windows says which folders a cloud service keeps in sync.
func TestSyncRoot_NeverAnswersUnderHere(t *testing.T) {
	root, provider, under, err := fsperm.SyncRoot(t.TempDir())

	if under || root != "" || provider != "" || err != nil {
		t.Errorf("SyncRoot = %q, %q, %t, %v; want not under", root, provider, under, err)
	}
}

func TestInspect_TellsWhatAPathIs(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "token")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, filepath.Join(dir, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bin")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}

	got, err := fsperm.Inspect(file)
	if err != nil || got.Link || got.Folder || got.Names != 2 {
		t.Errorf("Inspect(%s) = %+v, %v; want a file of two names", file, got, err)
	}
	if other, err := fsperm.Inspect(filepath.Join(dir, "elsewhere")); err != nil || other.ID != got.ID {
		t.Errorf("Inspect of its second name = %+v, %v; want the same file as %+v", other, err, got)
	}
	if got, err := fsperm.Inspect(link); err != nil || !got.Link || got.Reparse != fsperm.NameSurrogate {
		t.Errorf("Inspect(%s) = %+v, %v; want a link to another path", link, got, err)
	}
	if folder, err := fsperm.Inspect(dir); err != nil || !folder.Folder || folder.Link || folder.Reparse != fsperm.NotReparse || folder.ID == got.ID {
		t.Errorf("Inspect(%s) = %+v, %v; want a folder, which is not the file", dir, folder, err)
	}
}

func TestDirPrivate_TellsMode0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loadline")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		mode fs.FileMode
		want bool
	}{
		{mode: 0o700, want: true},
		{mode: 0o750, want: false},
		{mode: 0o500, want: false},
		{mode: 0o700 | fs.ModeSetgid, want: false},
	} {
		if err := os.Chmod(dir, tt.mode); err != nil {
			t.Fatal(err)
		}

		got, err := fsperm.DirPrivate(dir)

		if err != nil || got != tt.want {
			t.Errorf("DirPrivate under mode %v = %t, %v; want %t", tt.mode, got, err, tt.want)
		}
	}
}

// folderOfMode makes a folder in a temporary folder and gives it mode, which
// the test end puts back to 0700, so that the folder can be removed.
func folderOfMode(t *testing.T, mode fs.FileMode) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "loadline")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir
}

// Listing a folder takes reading it, and looking at what it holds, as the
// walk does at each entry, takes searching it: a folder its owner may read but
// not search keeps them out as much as one they may not read.
func TestListable_TakesReadingAndSearching(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may list any folder, whatever its mode")
	}
	for mode, want := range map[fs.FileMode]bool{0o700: true, 0o500: true, 0o400: false, 0o600: false, 0o300: false, 0o000: false} {
		dir := folderOfMode(t, mode)

		if got, err := fsperm.Listable(dir); err != nil || got != want {
			t.Errorf("Listable of a folder of mode %#o = %t, %v; want %t", mode, got, err, want)
		}
	}
	if _, err := fsperm.Listable(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Listable of a missing folder = %v, want an error that matches fs.ErrNotExist", err)
	}
}

// A folder whose mode keeps its owner from listing it gets every right for
// its owner back, and keeps what it gives others: nobody else gains
// anything. One its owner may list already is left as it is.
func TestRegainList_GivesTheOwnerBackAFolderOfTheirs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may list any folder, whatever its mode")
	}
	for _, tt := range []struct {
		mode, want fs.FileMode
		changed    bool
	}{
		{mode: 0o000, want: 0o700, changed: true},
		{mode: 0o300, want: 0o700, changed: true},
		{mode: 0o400, want: 0o700, changed: true},
		{mode: 0o600, want: 0o700, changed: true},
		{mode: 0o005, want: 0o705, changed: true},
		{mode: 0o500, want: 0o500},
	} {
		dir := folderOfMode(t, tt.mode)

		changed, err := fsperm.RegainList(dir)

		if err != nil || changed != tt.changed {
			t.Errorf("RegainList of a folder of mode %#o = %t, %v; want %t", tt.mode, changed, err, tt.changed)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != tt.want {
			t.Errorf("after RegainList, the mode of a folder of mode %#o is %#o; want %#o", tt.mode, got, tt.want)
		}
		if listable, err := fsperm.Listable(dir); err != nil || !listable {
			t.Errorf("after RegainList, Listable of a folder of mode %#o = %t, %v; want true", tt.mode, listable, err)
		}
	}
}

// Giving the owner every right lets them list a folder unless they have every
// right already: then something besides the mode keeps them out, such as an
// access control list that macOS keeps beside it, and RegainList cannot.
func TestListableOnceRegained_TellsWhetherTheModeIsWhatKeepsTheOwnerOut(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may list any folder, whatever its mode")
	}
	for _, mode := range []fs.FileMode{0o000, 0o400, 0o700} {
		dir := folderOfMode(t, mode)

		if got, err := fsperm.ListableOnceRegained(dir); err != nil || !got {
			t.Errorf("ListableOnceRegained of a folder of mode %#o = %t, %v; want true", mode, got, err)
		}
	}
}

// The advice gives a command to paste only for a path the shell reads as
// itself inside single quotes, each single quote in it closing them, escaped
// with a backslash, and opening them again. A path that holds what a shell,
// or a shell of another system an administrator may paste it into, would
// expand or end a line at gets the tool's name and what to change instead,
// as does one that holds a backslash, which fish reads as an escape even
// inside single quotes, or a format character, such as a bidirectional
// override or a zero-width space, which shows the path otherwise than the
// command acts on it. Either way the owner of the folder alone changes:
// Loadline judges the owner of each of its files once it may list them.
func TestResetAdvice_QuotesThePathOrNamesTheTool(t *testing.T) {
	for dir, quoted := range map[string]string{
		"/srv/loadline":              "'/srv/loadline'",
		"/srv/shared tools/loadline": "'/srv/shared tools/loadline'",
		"/srv/R&D/loadline":          "'/srv/R&D/loadline'",
		"/srv/it's/loadline":         `'/srv/it'\''s/loadline'`,
		"/srv/Zoë Ōta 山田/loadline":   "'/srv/Zoë Ōta 山田/loadline'",
	} {
		got := fsperm.ResetAdvice(dir)
		user := strings.TrimSuffix(strings.TrimPrefix(got, "give it to you with sudo chown "), " "+quoted)
		if !strings.HasPrefix(got, "give it to you with sudo chown ") || !strings.HasSuffix(got, " "+quoted) ||
			user == "" || strings.HasPrefix(user, "-") || strings.ContainsAny(user, " '\"") {
			t.Errorf("ResetAdvice(%q) = %q, want sudo chown, the user's name and %s", dir, got, quoted)
		}
	}
	for _, dir := range []string{
		`/srv/"quoted"/loadline`, "/srv/%HOME%/loadline", "/srv/$work/loadline", "/srv/$(id)/loadline", "/srv/wow!/loadline",
		"/srv/`id`/loadline", "/srv/“left/loadline", "/srv/right”/loadline", "/srv/„low/loadline", "/srv/line\nbreak/loadline",
		`/srv/back\slash/loadline`, `/srv/loadline\`, "/srv/right\u202eto left/loadline", "/srv/isolated\u2066part\u2069/loadline",
		"/srv/zero\u200bwidth/loadline",
	} {
		if got, want := fsperm.ResetAdvice(dir), "give "+dir+" to you with chown"; got != want {
			t.Errorf("ResetAdvice(%q) = %q, want %q", dir, got, want)
		}
	}
}
