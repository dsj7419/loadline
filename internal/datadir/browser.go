package datadir

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// browserProfile is the folder in the data directory that the dashboard's
// browser profile is kept in.
//
// Loadline makes it private, with an access list of its own on Windows,
// before the browser first launches, and takes no browser folder it did not
// make so. What the folder holds is then written by the user's own browser
// under that list, and Loadline never looks below it: the browser sets
// access lists of its own there (for its network sandbox, on Windows) and
// makes symbolic links (for its process lock, on Linux and macOS), which
// Loadline refuses among its own files. The data directory's list, passed
// down, stops at the folder's own on Windows, so it never rewrites them
// either; on Linux and macOS the data directory's mode keeps everyone else
// out of the folder.
const browserProfile = "browser"

// PrepareBrowserProfile makes dir, the dashboard's browser profile
// (config.Paths.BrowserProfileDir), ready for the browser to launch in. It
// creates dir private when it is missing, and refuses a dir that is a link,
// or one that is not private, which Loadline did not make so.
func PrepareBrowserProfile(dir string) error {
	dir = filepath.Clean(dir)
	if err := CheckLink(dir); err != nil {
		return err
	}
	if err := fsperm.MkdirAll(dir); err != nil {
		return fmt.Errorf("create the dashboard's browser profile: %w", err)
	}
	problem, err := profileProblem(dir)
	if err != nil {
		return err
	}
	if problem != "" {
		return errors.New(problem)
	}
	return nil
}

// profileProblem reports dir, the browser profile, as one Loadline did not
// make when it is not private as Loadline makes it (fsperm.DirPrivate):
// protected, on Windows, and admitting only the user, SYSTEM and
// Administrators. It returns "" when it is. Such a profile is never
// repaired: the files it holds keep whatever they inherited before, so it
// is removed, and Loadline makes a new one.
func profileProblem(dir string) (string, error) {
	private, err := fsperm.DirPrivate(dir)
	if err != nil || private {
		return "", err
	}
	return dir + " is not private, so the dashboard's browser profile there cannot be trusted: " +
		"remove it, and Loadline makes it again", nil
}
