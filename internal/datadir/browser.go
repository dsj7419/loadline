package datadir

import (
	"errors"
	"fmt"
	"io/fs"
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
// one that belongs to someone else, one that is no folder, and one that is
// not private, which Loadline did not make so, in the words Make refuses
// them with. Making dir makes the data directory too when that is missing,
// so it first refuses a data directory that a cloud service keeps in sync,
// as Make does.
func PrepareBrowserProfile(dir string) error {
	dir = filepath.Clean(dir)
	if problem := cloudProblem(filepath.Dir(dir)); problem != "" {
		return errors.New(problem)
	}
	if err := CheckLink(dir); err != nil {
		return err
	}
	// The profile is in the data directory, which a refusal for its owner
	// may offer to take back.
	if err := problemError(ownerProblem(filepath.Dir(dir), dir)); err != nil {
		return err
	}
	// MkdirAll fails on a file in words that say nothing of what to do.
	if err := problemError(notFolderProblem(dir)); err != nil {
		return err
	}
	if err := fsperm.MkdirAll(dir); err != nil {
		return fmt.Errorf("create the dashboard's browser profile: %w", err)
	}
	// A dir that is gone by now is an error, never a profile to launch the
	// browser into: the browser would make it again, without Loadline's list.
	return problemError(notPrivateProblem(dir))
}

// profileProblem reports dir, the browser profile, as no folder
// (notFolderProblem) or as one Loadline did not make (notPrivateProblem),
// or returns "" when it is neither.
func profileProblem(dir string) (string, error) {
	if problem, err := notFolderProblem(dir); err != nil || problem != "" {
		return problem, err
	}
	return notPrivateProblem(dir)
}

// notFolderProblem reports dir, the browser profile, as no folder, which the
// browser cannot keep anything in, whatever its permissions say, or returns
// "" when it is one or does not exist.
func notFolderProblem(dir string) (string, error) {
	entry, err := inspect(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil || entry.Folder:
		return "", err
	}
	return dir + " is not a folder, so the dashboard's browser profile cannot be kept there: " +
		"remove it, and Loadline makes the profile in its place", nil
}

// notPrivateProblem reports dir, the browser profile, as one Loadline did
// not make when it is not private as Loadline makes it (fsperm.DirPrivate):
// protected, on Windows, and admitting only the user, SYSTEM and
// Administrators. It returns "" when it is. Such a profile is never
// repaired: the files it holds keep whatever they inherited before, so it
// is removed, and Loadline makes a new one.
func notPrivateProblem(dir string) (string, error) {
	private, err := fsperm.DirPrivate(dir)
	if err != nil || private {
		return "", err
	}
	return dir + " is not private, so the dashboard's browser profile there cannot be trusted: " +
		"remove it, and Loadline makes it again", nil
}

// problemError returns err when there is one, or else problem as an error,
// or nil when problem is "".
func problemError(problem string, err error) error {
	switch {
	case err != nil:
		return err
	case problem != "":
		return errors.New(problem)
	}
	return nil
}
