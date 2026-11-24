// Package datadir keeps Loadline's data directory the user's alone. The
// directory holds the ingest token, the binary every hook and status line
// runs, the spool and the history, and LOADLINE_HOME can put it anywhere.
// Install and the server secure it each time they run, and doctor reports
// what keeps it from being secure.
package datadir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// syncRoot is fsperm.SyncRoot. Tests replace it: whether Windows lets a test
// register a sync root depends on the machine, and nothing at hand makes it
// fail to answer.
var syncRoot = fsperm.SyncRoot

// movableAdvice ends the report of a folder above the data directory that
// someone else could move aside.
const movableAdvice = ", and with it replace the binary every hook runs; " +
	"set LOADLINE_HOME to a folder in your user profile, or take that right away"

// Secure prepares dir to hold Loadline's files, as install and the server
// do each time they run. It refuses what Verify refuses, and then does what
// Make does.
//
// The check of the folders above dir goes up to the volume root, or when top
// is set, stops below top, which it trusts with every folder above it.
func Secure(dir, top string) error {
	dir = filepath.Clean(dir)
	if err := Verify(dir, top); err != nil {
		return err
	}
	return Make(dir)
}

// Make prepares dir as Secure does, but without judging the folders above
// it, as the creation of the token does on a hook's path: there, a folder
// others could move aside still keeps the token from them. It refuses a dir
// in a folder that a cloud service keeps in sync, which would copy the token
// to the cloud, a dir that is a link, one that holds anything that is not
// Loadline's, which making the dir private would take other users' access
// to, and a link among Loadline's own files. On Windows a link is any
// reparse point but a deduplicated file, which is the file it was. It then
// creates dir, and each missing folder above it, private to the user, or
// makes the existing dir private: it judges each of Loadline's own files and
// folders, refusing a link, a file with another name and one that
// belongs to someone else, and takes off each that it found none of that in
// whatever its access list holds of its own, which making the dir private
// leaves (fsperm.EntryPrivate), even when it goes on to refuse another. Only
// when it refuses none does it have the dir pass its list down.
//
// Several Loadline processes may run it at once, and write files in dir
// meanwhile (Temporary), which it leaves alone. It changes nothing that is
// private already: Windows passes a new access list on a folder down to
// everything in it, those files among them.
func Make(dir string) error {
	dir = filepath.Clean(dir)
	problem, err := contentProblem(dir)
	if err != nil {
		return err
	}
	if problem != "" {
		return errors.New(problem)
	}
	if err := fsperm.MkdirAll(dir); err != nil {
		return fmt.Errorf("create the data directory: %w", err)
	}
	// Nothing is passed down to what the walk has not judged: a list passed
	// down would go through a folder that keeps the user out until then to a
	// file there that has a name elsewhere too, and change the file there.
	// The dir, and then each folder that is not private, gets the private
	// list alone, which Windows passes down to nothing it holds, so that the
	// walk can list it. A file is cleaned as soon as it is judged, since its
	// list reaches nothing else, and a folder once the walk is through what
	// it holds, which its list reaches.
	private, err := fsperm.DirPrivate(dir)
	if err != nil {
		return err
	}
	if !private {
		if err := fsperm.PrivateHere(dir); err != nil {
			return err
		}
	}
	unlocked := map[string]bool{}
	found, err := ownPaths(dir, &repair{
		before: func(path string, folder bool) error {
			problem, err := ownerProblem(path)
			if err != nil {
				return err
			}
			if problem != "" {
				return errors.New(problem)
			}
			if !folder {
				return clearOwnEntries(path)
			}
			unlocked[path], err = makePrivateHere(path)
			return err
		},
		after: func(path string) error {
			if !unlocked[path] {
				return nil
			}
			return clearExplicit(path)
		},
	})
	if err != nil {
		return err
	}
	if problem := found.linkProblem(); problem != "" {
		return errors.New(problem)
	}
	if private {
		return nil
	}
	// Last, as well as everything above, what other Loadline processes
	// began writing before the dir was private, which the walk left alone.
	return fsperm.PassDown(dir)
}

// makePrivateHere gives path, one of Loadline's folders, the private list
// when it is not private, passing it down to nothing it holds
// (fsperm.PrivateHere), and reports whether it did.
func makePrivateHere(path string) (bool, error) {
	changed := false
	err := settle(func() error {
		private, err := fsperm.EntryPrivate(path)
		if err != nil || private {
			return err
		}
		changed = true
		return fsperm.PrivateHere(path)
	})
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return changed, err
}

// clearExplicit takes off path whatever its access list holds of its own,
// so that only what the data directory passes down applies. A path that is
// being deleted is waited for (settle), and once gone, left out.
func clearExplicit(path string) error {
	err := settle(func() error { return fsperm.ClearExplicit(path) })
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// clearOwnEntries takes off path, one of Loadline's files or folders in the
// private data directory, whatever its access list holds of its own: an
// entry given explicitly, a denial too, since one on the spool or the binary
// keeps the hooks out, or a list of its own other than the private one. Only
// what the data directory passes down then applies. A path that is private
// already is left as it is. A path that is being deleted is waited for
// (settle), and once gone, left out.
func clearOwnEntries(path string) error {
	err := settle(func() error {
		private, err := fsperm.EntryPrivate(path)
		if err != nil || private {
			return err
		}
		return fsperm.ClearExplicit(path)
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Verify returns the error Secure would refuse dir with, which says what to
// do, or nil when Secure would go ahead. It refuses a dir that another user
// could move aside, by deleting or renaming one of the folders above it that
// already exist, and then put a folder of their own, and a binary of their
// own, at the path the hooks run. It refuses what Make refuses. It changes
// nothing.
func Verify(dir, top string) error {
	dir = filepath.Clean(dir)
	problem, err := refusal(dir, top)
	if err != nil {
		return err
	}
	if problem != "" {
		return errors.New(problem)
	}
	return nil
}

// Check reports, in a sentence that says what to do, the first thing that
// keeps dir from being the user's alone: what Verify refuses, or access that
// others have to dir or to one of Loadline's files in it. It returns "" when
// there is none. A dir that does not exist yet is reported with what install
// would refuse to make it for, judged from the folders above it that exist,
// or, when install would make it, is an error that matches fs.ErrNotExist.
func Check(dir, top string) (string, error) {
	dir = filepath.Clean(dir)
	if _, err := os.Lstat(dir); err != nil {
		return notMadeYet(dir, top, err)
	}
	if problem, err := refusal(dir, top); err != nil || problem != "" {
		return problem, err
	}
	if problem, err := exposed(dir, fsperm.Exposure); err != nil || problem != "" {
		return problem, err
	}
	found, err := ownPaths(dir, nil)
	if err != nil {
		return "", err
	}
	if found.locked != "" {
		return found.locked + " keeps you from seeing what it holds; run loadline install", nil
	}
	for _, path := range found.paths {
		if problem, err := entryProblem(path); err != nil || problem != "" {
			return problem, err
		}
	}
	return "", nil
}

// notMadeYet reports what install would refuse to make dir for, which could
// not be read (unread), or returns unread when dir is there but unreadable,
// or missing and install would make it. A server refuses such a dir before
// it makes anything there, so doctor finds none to judge, and says why here.
func notMadeYet(dir, top string, unread error) (string, error) {
	unread = fmt.Errorf("read the data directory: %w", unread)
	if !errors.Is(unread, fs.ErrNotExist) {
		return "", unread
	}
	problem, err := refusal(dir, top)
	switch {
	case err != nil:
		return "", err
	case problem == "":
		return "", unread
	}
	return dir + " does not exist yet, and loadline install would refuse to make it: " + problem, nil
}

// entryProblem reports path, one of Loadline's files or folders in the data
// directory, as not private when its access list lets anyone else in, or ""
// when it does not. A path that is being deleted is waited for (settle),
// and once gone, has no problem.
func entryProblem(path string) (string, error) {
	var problem string
	err := settle(func() (err error) {
		problem, err = exposed(path, fsperm.EntryExposure)
		return err
	})
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return problem, err
}

// refusal reports what Verify refuses dir for, or "" when nothing.
func refusal(dir, top string) (string, error) {
	if problem, err := movable(dir, top); err != nil || problem != "" {
		return problem, err
	}
	return contentProblem(dir)
}

// contentProblem reports what Make refuses dir for, or "" when nothing.
func contentProblem(dir string) (string, error) {
	// Written with a separator at its end, a link's path would have Lstat
	// look at where it leads, and every check here would judge that.
	dir = filepath.Clean(dir)
	if problem := cloudProblem(dir); problem != "" {
		return problem, nil
	}
	if problem, err := dirLinkProblem(dir); err != nil || problem != "" {
		return problem, err
	}
	foreign, err := foreignEntry(dir)
	if err != nil {
		return "", err
	}
	if foreign != "" {
		return foreignProblem(dir, foreign), nil
	}
	// A folder the user may not list is no refusal: Make puts its list back
	// to what the data directory passes down, and judges what it holds then.
	found, err := ownPaths(dir, nil)
	if err != nil {
		return "", err
	}
	if problem := found.linkProblem(); problem != "" {
		return problem, nil
	}
	for _, path := range found.paths {
		if problem, err := ownerProblem(path); err != nil || problem != "" {
			return problem, err
		}
	}
	return "", nil
}

// cloudProblem reports dir as in a folder that a cloud service keeps in
// sync, which would copy the token and the binary every hook runs to the
// cloud, or "" when Windows does not say it is. Only that answer refuses.
// Windows cannot answer for a folder on a file system its cloud filter does
// not serve, such as exFAT or a network share, where no service it serves
// keeps anything in sync, so that it could not answer is no reason to
// refuse dir, and what kept it from answering is left out.
func cloudProblem(dir string) string {
	root, provider, under, _ := syncRoot(dir)
	if !under {
		return ""
	}
	return dir + " is in " + root + ", which " + provider + " keeps in sync with the cloud, so the token and the binary " +
		`every hook runs would be copied there: set LOADLINE_HOME to a folder outside it, such as the default %LOCALAPPDATA%\loadline`
}

// dirLinkProblem reports dir as a link, in the words its kind calls for, or
// "" when it is not one or does not exist. Made private, a link would admit
// only the user while the folder it leads to, which then holds the token and
// the binary every hook runs, stayed as open as it is.
func dirLinkProblem(dir string) (string, error) {
	entry, err := inspect(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist), err == nil && !entry.Link:
		return "", nil
	case err != nil:
		return "", err
	}
	switch entry.Reparse {
	case fsperm.CloudFile:
		return cloudFileProblem(dir, entry.Tag), nil
	case fsperm.OtherReparse:
		return unknownReparseProblem(dir, entry.Tag) + "set LOADLINE_HOME to a new folder", nil
	default:
		// Where it leads only helps say what to do: a link that cannot be
		// read is refused just the same.
		problem := dir + " is a link, which the data directory must never be: set LOADLINE_HOME to the folder it leads to itself"
		if target, err := os.Readlink(dir); err == nil {
			problem = dir + " is a link to " + target + ", which the data directory must never be: set LOADLINE_HOME to " + target + " itself"
		}
		return problem, nil
	}
}

// ownerProblem reports path, one of Loadline's files or folders, as
// belonging to someone else, who may always let anyone at it, whatever
// Loadline makes of its access list, or "" when it does not. A path that is
// being deleted is waited for (settle), and once gone, has no problem.
func ownerProblem(path string) (string, error) {
	var owner string
	err := settle(func() (err error) {
		owner, err = foreignOwner(path)
		return err
	})
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil || owner == "":
		return "", err
	}
	return path + " belongs to " + owner + ", who can always change who may read or replace it, " +
		"which Loadline's own files must not allow: remove it, then run loadline install", nil
}

// exposed reports path as not private when exposure finds someone else it
// admits, or "" when there is none.
func exposed(path string, exposure func(string) (string, error)) (string, error) {
	found, err := exposure(path)
	if err != nil || found == "" {
		return "", err
	}
	return path + " is not private: " + found + "; run loadline install", nil
}

// movable reports a folder above dir that someone else could move aside,
// and what to do about it, or "" when there is none.
func movable(dir, top string) (string, error) {
	found, err := fsperm.MovableAncestor(dir, top)
	if err != nil || found == "" {
		return "", err
	}
	return found + movableAdvice, nil
}
