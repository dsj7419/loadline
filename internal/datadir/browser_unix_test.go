//go:build !windows

package datadir_test

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/datadir"
)

// linkFolder makes link a symbolic link to target.
func linkFolder(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// lookPrivate gives path, a file, mode 0700, which fsperm.DirPrivate reads
// alone.
func lookPrivate(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

// described describes each path in dir, a link not followed: its mode, and
// for a link, where it leads.
func described(t *testing.T, dir string) map[string]string {
	t.Helper()
	found := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		found[path] = info.Mode().String()
		if entry.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		target, err := os.Readlink(path)
		found[path] += " -> " + target
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// plantWhatABrowserLeaves puts in profile the symbolic links a Chromium
// browser keeps there while it runs, which stay after a crash:
// SingletonLock, which leads to the host's name and the browser's process
// ID; SingletonSocket, to the socket in a folder of its own under the
// temporary folder; and SingletonCookie, to a random number. It returns a
// check that each still leads where it did, and that nothing in the profile
// changed its mode.
func plantWhatABrowserLeaves(t *testing.T, profile string) (unchanged func(*testing.T)) {
	t.Helper()
	for name, target := range map[string]string{
		"SingletonLock":   "host-123",
		"SingletonSocket": filepath.Join(t.TempDir(), "SingletonSocket"),
		"SingletonCookie": "8317226891402093271",
	} {
		if err := os.Symlink(target, filepath.Join(profile, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(profile, "Local State"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := described(t, profile)
	return func(t *testing.T) {
		t.Helper()
		if after := described(t, profile); !maps.Equal(after, before) {
			t.Errorf("what %s holds went from %v to %v", profile, before, after)
		}
	}
}

// The data directory's mode 0700 keeps everyone else out of the profile, and
// the profile's own mode keeps them out of it too, from the moment Loadline
// makes it.
func TestPrepareBrowserProfile_MakesItPrivate(t *testing.T) {
	home, _ := securedHome(t)
	profile := filepath.Join(home, "browser")

	if err := datadir.PrepareBrowserProfile(profile); err != nil {
		t.Fatalf("PrepareBrowserProfile: %v", err)
	}

	info, err := os.Lstat(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("%s has mode %v, want a folder with mode 0700", profile, info.Mode())
	}
}

// A browser folder that lets anyone else in is not one Loadline made, and
// nothing takes or changes it: the browser makes a new one.
func TestMake_RefusesABrowserProfileItDidNotMakePrivate(t *testing.T) {
	for _, mode := range []fs.FileMode{0o755, 0o750} {
		t.Run(mode.String(), func(t *testing.T) {
			home, top := securedHome(t)
			profile := filepath.Join(home, "browser")
			if err := os.Mkdir(profile, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(profile, mode); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(profile, "Local State")
			if err := os.WriteFile(state, []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(state, 0o644); err != nil {
				t.Fatal(err)
			}
			before := described(t, profile)

			refusedAsNotPrivate(t, home, top, profile)

			if after := described(t, profile); !maps.Equal(after, before) {
				t.Errorf("what %s holds went from %v to %v", profile, before, after)
			}
		})
	}
}
