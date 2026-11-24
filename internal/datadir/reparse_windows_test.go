package datadir

// Reparse points among Loadline's paths, and as the data directory itself.
// Every check that refuses a link refuses each kind but a deduplicated file,
// in the words its kind calls for.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// junctionAt makes link a directory junction to target, as any user may in a
// folder they can create directories in.
func junctionAt(t *testing.T, link, target string) {
	t.Helper()
	//nolint:gosec // G204: the paths are the test's own temporary folders
	if out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
}

// entryAt makes path an ordinary file, holding a byte, or when folder is set,
// a folder.
func entryAt(t *testing.T, path string, folder bool) {
	t.Helper()
	if folder {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// linkTarget returns a file, or a folder, in top, outside the data directory,
// for a link to lead to.
func linkTarget(t *testing.T, top string, folder bool) string {
	t.Helper()
	target := filepath.Join(top, "elsewhere")
	if !folder {
		target += ".txt"
	}
	if _, err := os.Lstat(target); err != nil {
		entryAt(t, target, folder)
	}
	return target
}

// inspectAsReparsePoint makes the checks read path, for the rest of the
// test, as a reparse point of kind with tag, and every other path as it is.
// As Inspect does, they read every kind but a deduplicated file as a link.
func inspectAsReparsePoint(t *testing.T, path string, kind fsperm.ReparseKind, tag uint32) {
	t.Helper()
	previous := inspect
	inspect = func(p string) (fsperm.Entry, error) {
		entry, err := previous(p)
		if err == nil && p == path {
			entry.Reparse, entry.Tag, entry.Link = kind, tag, kind != fsperm.Deduplicated
		}
		return entry, err
	}
	t.Cleanup(func() { inspect = previous })
}

// reparsePointAt returns what makes path a file, or when folder is set, a
// folder, that holds a reparse point of kind with tag.
//
// That is a reparse point planted on it, which nothing on the volume
// handles, unless opened is set: then the system must open the path through
// the point, as it does through one its filter handles, which a planted
// point does not let it. Deduplication makes files alone reparse points, and
// a folder with a planted tag could not be listed. In either case path is an
// ordinary private file or folder, read as such a reparse point through
// inspect, which also shows the checks judge by Entry.Link, never by kind.
func reparsePointAt(kind fsperm.ReparseKind, tag uint32) func(t *testing.T, top, path string, folder, opened bool) {
	return func(t *testing.T, _, path string, folder, opened bool) {
		t.Helper()
		if !opened && (!folder || kind != fsperm.Deduplicated) {
			entryAt(t, path, folder)
			fspermtest.PlantReparsePoint(t, path, tag)
			return
		}
		if folder {
			if err := fsperm.MkdirAll(path); err != nil {
				t.Fatal(err)
			}
		} else {
			entryAt(t, path, false)
		}
		inspectAsReparsePoint(t, path, kind, tag)
	}
}

// problemOf is what Check reports of dir, as an error.
func problemOf(dir, top string) error {
	problem, err := Check(dir, top)
	if err != nil || problem == "" {
		return err
	}
	return errors.New(problem)
}

// The checks refuse a link among Loadline's own files, or as the data
// directory, because opening it would follow it somewhere else. Windows has
// links of many kinds, each a reparse point, and reparse points of other
// kinds besides, which Loadline cannot tell where they lead, or whether they
// do. Each is refused, in a sentence that says what it is, except a
// deduplicated file, which is the ordinary file it was.
func TestEveryLinkCheck_RefusesEachReparseKindButDeduplicated(t *testing.T) {
	kinds := []struct {
		name string
		// make makes path a file, or when folder is set, a folder, of the
		// kind, with whatever it leads to in top, which the system opens
		// the path through when opened is set.
		make func(t *testing.T, top, path string, folder, opened bool)
		// refusal is how a check's report of path starts, path being the
		// data directory when dataDir is set, or "" when the check takes it.
		refusal func(path string, dataDir bool) string
	}{
		{
			name: "a junction",
			make: func(t *testing.T, top, path string, _, _ bool) {
				t.Helper()
				junctionAt(t, path, linkTarget(t, top, true))
			},
			refusal: linkRefusal,
		},
		{
			name: "a symbolic link",
			make: func(t *testing.T, top, path string, folder, _ bool) {
				t.Helper()
				if err := os.Symlink(linkTarget(t, top, folder), path); err != nil {
					t.Skipf("make a symbolic link here: %v", err)
				}
			},
			refusal: linkRefusal,
		},
		{
			name: "a reparse point of another kind",
			make: reparsePointAt(fsperm.OtherReparse, fspermtest.TagThirdParty),
			refusal: func(path string, dataDir bool) string {
				advice := "remove it, then run loadline install"
				if dataDir {
					advice = "set LOADLINE_HOME to a new folder"
				}
				return path + " is a reparse point of a kind Loadline does not know (tag 0x0000ABCD), which may lead somewhere else: " + advice
			},
		},
		{
			name: "a cloud file",
			make: reparsePointAt(fsperm.CloudFile, fspermtest.TagOneDrive),
			refusal: func(path string, _ bool) string {
				return path + " is kept in sync with the cloud (tag 0x80000021), so the token and the binary every hook runs would be " +
					`copied there: set LOADLINE_HOME to a folder no cloud service keeps in sync, such as the default %LOCALAPPDATA%\loadline`
			},
		},
		{
			name:    "a deduplicated file",
			make:    reparsePointAt(fsperm.Deduplicated, fspermtest.TagDedup),
			refusal: func(string, bool) string { return "" },
		},
	}
	ways := []struct {
		name string
		// at is where the reparse point is in the data directory, or "" for
		// the data directory itself.
		at     string
		folder bool
		// opened is set where the check opens the path through the reparse
		// point before it judges what the path is: Verify and Check first
		// find the folders that really hold the data directory, which a
		// reparse point that nothing handles keeps them from, and refuse the
		// data directory for that.
		opened bool
		check  func(dir, top, path string) error
	}{
		{name: "Make of the data directory", folder: true, check: func(dir, _, _ string) error { return Make(dir) }},
		{name: "Verify of the data directory", folder: true, opened: true, check: func(dir, top, _ string) error { return Verify(dir, top) }},
		{name: "Check of the data directory", folder: true, opened: true, check: func(dir, top, _ string) error { return problemOf(dir, top) }},
		{name: "CheckLink of a file", at: "token", check: func(_, _, path string) error { return CheckLink(path) }},
		{name: "CheckLink of bin as SelfInstall checks it", at: "bin", folder: true, check: func(_, _, path string) error { return CheckLink(path) }},
		{name: "the walk of a file", at: "token", check: func(dir, _, _ string) error { return Make(dir) }},
		{name: "the walk of a folder", at: "spool", folder: true, check: func(dir, _, _ string) error { return Make(dir) }},
		{name: "Check of a file", at: "token", check: func(dir, top, _ string) error { return problemOf(dir, top) }},
	}
	for _, kind := range kinds {
		for _, way := range ways {
			// No comma in the name: a junction's path, which holds it, is
			// passed to cmd, which takes a comma for a separator.
			t.Run(kind.name+" for "+way.name, func(t *testing.T) {
				top := t.TempDir()
				dir := filepath.Join(top, "loadline")
				path := dir
				if way.at != "" {
					if err := fsperm.MkdirAll(dir); err != nil {
						t.Fatal(err)
					}
					path = filepath.Join(dir, way.at)
				}
				kind.make(t, top, path, way.folder, way.opened)
				want := kind.refusal(path, way.at == "")

				err := way.check(dir, top, path)

				switch {
				case want == "" && err != nil:
					t.Errorf("%v; want %s taken", err, path)
				case want != "" && (err == nil || !strings.HasPrefix(err.Error(), want)):
					t.Errorf("%v; want it refused, starting %q", err, want)
				}
			})
		}
	}
}

// linkRefusal is how a check's report of path, a link to another path,
// starts: the data directory's says where it leads.
func linkRefusal(path string, dataDir bool) string {
	if dataDir {
		return path + " is a link to "
	}
	return path + " is a link, which Loadline's own files must never be: remove it, then run loadline install"
}

// Data deduplication keeps a file's data in a store it shares with other
// files, and makes the file a reparse point that leads nowhere else: a
// deduplicated binary is the binary every hook runs. Install and the server
// take it, and take off it what was given to others, as they do for any.
func TestSecure_AcceptsADeduplicatedBinary(t *testing.T) {
	top := t.TempDir()
	dir := filepath.Join(top, "loadline")
	binary := filepath.Join(dir, "bin", "loadline.exe")
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	entryAt(t, binary, false)
	fspermtest.Grant(t, binary, windows.FILE_GENERIC_READ, windows.NO_INHERITANCE)
	fspermtest.PlantReparsePoint(t, binary, fspermtest.TagDedup)
	if others := fspermtest.Others(t, binary, fspermtest.Any); len(others) == 0 {
		t.Fatalf("%s admits nobody else, so the test proves nothing", binary)
	}

	if err := Secure(dir, top); err != nil {
		t.Fatalf("Secure = %v; want the deduplicated binary taken", err)
	}

	if others := fspermtest.Others(t, binary, fspermtest.Any); len(others) > 0 {
		t.Errorf("%s admits %v after Secure", binary, others)
	}
	if got, err := Check(dir, top); err != nil || got != "" {
		t.Errorf("Check = %q, %v; want \"\"", got, err)
	}
	if entry, err := fsperm.Inspect(binary); err != nil || entry.Reparse != fsperm.Deduplicated {
		t.Errorf("Inspect(%s) = %+v, %v; want it still deduplicated", binary, entry, err)
	}
}
