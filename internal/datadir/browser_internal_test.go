package datadir

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// onFirstLook runs f, for the rest of the test, just after the checks first
// look at path itself, so that what the walk read of path no longer holds.
func onFirstLook(t *testing.T, path string, f func()) {
	t.Helper()
	previous := inspect
	var once sync.Once
	inspect = func(p string) (fsperm.Entry, error) {
		entry, err := previous(p)
		if p == path {
			once.Do(f)
		}
		return entry, err
	}
	t.Cleanup(func() { inspect = previous })
}

// profileGoingAway runs each check of a data directory whose browser profile
// goes, through gone, right after the walk finds it, and reports any that
// fails or reports a problem.
func profileGoingAway(t *testing.T, gone func(t *testing.T, profile string)) {
	t.Helper()
	for step, check := range map[string]func(dir, top string) error{
		"Make":   func(dir, _ string) error { return Make(dir) },
		"Verify": Verify,
		"Check": func(dir, top string) error {
			problem, err := Check(dir, top)
			if err != nil || problem == "" {
				return err
			}
			return errors.New(problem)
		},
	} {
		t.Run(step, func(t *testing.T) {
			top := t.TempDir()
			dir := filepath.Join(top, "loadline")
			if err := Secure(dir, top); err != nil {
				t.Fatal(err)
			}
			profile := filepath.Join(dir, browserProfile)
			if err := fsperm.MkdirAll(profile); err != nil {
				t.Fatal(err)
			}
			onFirstLook(t, profile, func() { gone(t, profile) })

			if err := check(dir, top); err != nil {
				t.Errorf("%s = %v; want the data directory taken, its profile gone", step, err)
			}
		})
	}
}

// A refused profile is to be removed, as the refusal says, and the user may
// remove it while a hook starts the server, which secures the data
// directory. One that goes after the walk found it holds nothing to refuse.
func TestMake_TakesADataDirectoryWhoseBrowserProfileIsRemovedMeanwhile(t *testing.T) {
	profileGoingAway(t, func(t *testing.T, profile string) {
		t.Helper()
		if err := os.Remove(profile); err != nil {
			t.Fatal(err)
		}
	})
}
