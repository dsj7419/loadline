package datadir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// ownNames are the entries Loadline makes in the data directory: the token,
// the installed binary's folder, the spool, the server's log, the history
// and the files SQLite keeps beside it, the stamps the server and the status
// line leave, and, under LOADLINE_HOME, the config file.
var ownNames = []string{
	"token", "bin", "spool", "loadline.log",
	"loadline.db", "loadline.db-wal", "loadline.db-shm", "loadline.db-journal",
	"ensure.stamp", "opened.stamp", "config.json",
}

const (
	// tokenTempPrefix starts the names of the files a token is written to
	// before it is linked into place.
	tokenTempPrefix = ".token-"
	// rotatedLogPrefix starts the names of the logs the server rotated out,
	// which end in their number.
	rotatedLogPrefix = "loadline.log."
)

// Owns reports whether name is an entry Loadline makes in the data
// directory.
func Owns(name string) bool {
	if slices.Contains(ownNames, name) || strings.HasPrefix(name, tokenTempPrefix) {
		return true
	}
	n, ok := strings.CutPrefix(name, rotatedLogPrefix)
	if !ok {
		return false
	}
	_, err := strconv.ParseUint(n, 10, 32)
	return err == nil
}

// tempNames are the names of the files Loadline writes before it puts them
// in place under their own: the token's (config), a spooled event's
// (forward) and the installed binary's (install), each a prefix and a
// suffix around what os.CreateTemp makes unique.
var tempNames = [...]struct{ prefix, suffix string }{
	{tokenTempPrefix, ""},
	{".put-", ".tmp"},
	{".loadline-", ".tmp"},
}

// Temporary reports whether name is that of a file Loadline writes before it
// puts it in place under its own name. Such a file belongs to the process
// writing it, which may be another Loadline process at work right now, and
// which renames, links or deletes it at any moment. The preparation and the
// check of the data directory leave it alone, and judge it once it is in
// place.
func Temporary(name string) bool {
	for _, temp := range tempNames {
		if strings.HasPrefix(name, temp.prefix) && strings.HasSuffix(name, temp.suffix) {
			return true
		}
	}
	return false
}

// systemNames are the files Windows Explorer and the macOS Finder leave in a
// folder its user has looked at, which do not make it anyone else's.
var systemNames = []string{".DS_Store", "desktop.ini", "Thumbs.db"}

// systemFile reports whether entry is a file the system leaves. A folder or
// a link by one of their names is someone else's: no system makes one.
func systemFile(entry fs.DirEntry) bool {
	return slices.Contains(systemNames, entry.Name()) && entry.Type().IsRegular()
}

// foreignEntry returns the name of the first entry of dir that is neither
// Loadline's nor a file the system leaves, or "" when there is none or dir
// does not exist.
func foreignEntry(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the data directory: %w", err)
	}
	for _, entry := range entries {
		if !Owns(entry.Name()) && !systemFile(entry) {
			return entry.Name(), nil
		}
	}
	return "", nil
}

// foreignProblem reports dir, which holds name, as one Loadline must not
// take: making it private would take other users' access to everything in
// it away.
func foreignProblem(dir, name string) string {
	return dir + " holds " + name + ", which is not Loadline's: LOADLINE_HOME must be a folder of Loadline's own, " +
		"since Loadline makes it, and all it holds, private; set LOADLINE_HOME to a new folder"
}

// CheckLink returns the error install and the server refuse path with when
// it is a link, or nil when it is not or does not exist. Writing through a
// link among Loadline's own files would put them, the binary every hook runs
// among them, wherever it points.
func CheckLink(path string) error {
	path = filepath.Clean(path)
	link, err := fsperm.IsLink(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case link:
		return errors.New(linkProblem(path))
	}
	return nil
}

func linkProblem(path string) string {
	return path + " is a link, which Loadline's own files must never be: remove it, then run loadline install"
}

func moreNamesProblem(path string) string {
	return path + " has another name, a hard link, which Loadline's own files must never have, since making it " +
		"private would change the file under its other name too: remove it, then run loadline install"
}

// foreignOwner is fsperm.ForeignOwner. Tests replace it: an account that is
// not elevated, or not root, cannot give a file another owner.
var foreignOwner = fsperm.ForeignOwner

// inspect is fsperm.Inspect. Tests replace it: no volume at hand gives two
// files IDs that differ only above their low 64 bits, as ReFS can.
var inspect = fsperm.Inspect

// tree is what a walk of Loadline's paths in the data directory found.
type tree struct {
	// paths are Loadline's files and folders, each folder before what it
	// holds.
	paths []string
	// link is the first link among them, or the first file with another
	// name when moreNames is set, where the walk stopped, or "".
	link      string
	moreNames bool
	// locked is the first folder among them that the user may not list, or
	// "": what it holds is not in paths.
	locked string
}

// linkProblem reports what the walk stopped at, or "" when it went through.
func (t tree) linkProblem() string {
	switch {
	case t.link == "":
		return ""
	case t.moreNames:
		return moreNamesProblem(t.link)
	}
	return linkProblem(t.link)
}

// ownPaths walks what Loadline keeps in dir: each entry it owns, the files
// the system leaves there, which are made private with it, and everything
// below the folders among them, except the regular files that are still
// being written (Temporary). It follows no link: it stops at the
// first. A dir that does not exist holds nothing, and an entry that goes
// while it looks, as a spooled event does once drained, is left out.
//
// fix, when it is set, may change each path as the walk comes to it and,
// for a folder, once the walk is through it (repair). Without it, a folder
// the user may not list is noted in locked, not an error.
func ownPaths(dir string, fix *repair) (tree, error) {
	var found tree
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return found, nil
	}
	if err != nil {
		return found, fmt.Errorf("read the data directory: %w", err)
	}
	for _, entry := range entries {
		if !Owns(entry.Name()) && !systemFile(entry) {
			continue
		}
		if err := found.walk(filepath.Join(dir, entry.Name()), entry.Type(), fix); err != nil || found.link != "" {
			return found, err
		}
	}
	return found, nil
}

// repair is what Make does to Loadline's paths as it walks them.
type repair struct {
	// before runs on each path once it is known to be no link, before the
	// walk looks into it, so that a folder whose list keeps the user out
	// can be listed.
	before func(path string, folder bool) error
	// after runs on each folder once the walk has been through what it
	// holds and found nothing there to refuse.
	after func(path string) error
}

// judgeNames tells what path itself is, and returns errMoreNames when it is
// a file with a name besides its own and its own temporary ones (leftovers).
// A second name puts the file wherever that name is too, and whatever is
// done to it here is done to it there. A temporary name beside it in its own
// folder is no such name: the token is written under one and then linked
// into place, and that name stays when its writer is ended before it
// removes it, or cannot remove it while a scanner holds the file. When tidy
// is set, judgeNames tries to remove those leftovers, as far as it can for
// a moment, and judges the file again.
func judgeNames(path string, tidy bool) (fsperm.Entry, error) {
	entry, err := inspect(path)
	if err != nil || entry.Link || entry.Folder || entry.Names == 1 {
		return entry, err
	}
	own, err := leftovers(path, entry.ID)
	if err != nil {
		return entry, err
	}
	if tidy && len(own) > 0 {
		for _, name := range own {
			_ = settleWithin(leftoverBudget, func() error { return os.Remove(name) })
		}
		return judgeNames(path, false)
	}
	if entry.Names > uint64(len(own))+1 {
		return entry, errMoreNames
	}
	return entry, nil
}

// leftovers returns the temporary names in path's folder (Temporary) that
// are names of the file id tells of, path's own.
func leftovers(path string, id fsperm.FileID) ([]string, error) {
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if !Temporary(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		name := filepath.Join(dir, entry.Name())
		// One that cannot be looked at, as while its writer deletes it, is
		// not counted: the walk then waits for it to go (settle).
		if other, err := inspect(name); err == nil && other.ID == id {
			names = append(names, name)
		}
	}
	return names, nil
}

// walk adds path, whose type its folder's listing gave as typ, and when it
// is a folder, everything below it, stopping at the first link and passing
// over the regular files that are still being written.
func (t *tree) walk(path string, typ fs.FileMode, fix *repair) error {
	if Temporary(filepath.Base(path)) && typ.IsRegular() {
		// Not even looked at: it is its writer's, and on Windows, looking
		// at one a moment after its writer deleted it fails while the
		// deletion is pending. The listing, which opens nothing, tells a
		// file from a link, which no writer makes and which is refused.
		return nil
	}
	// What the path itself is, read even when its own list keeps the user
	// out, as one in a folder that was just made listable may.
	var entry fsperm.Entry
	err := settle(func() (err error) {
		entry, err = judgeNames(path, fix != nil)
		return err
	})
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, errMoreNames):
		t.link, t.moreNames = path, true
		return nil
	case err != nil:
		return err
	case entry.Link:
		t.link = path
		return nil
	}
	t.paths = append(t.paths, path)
	if fix != nil {
		if err := fix.before(path, entry.Folder); err != nil {
			return err
		}
	}
	if !entry.Folder {
		return nil
	}
	entries, err := os.ReadDir(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, fs.ErrPermission) && fix == nil:
		if t.locked == "" {
			t.locked = path
		}
		return nil
	case err != nil:
		return fmt.Errorf("read %s: %w", path, err)
	}
	for _, entry := range entries {
		if err := t.walk(filepath.Join(path, entry.Name()), entry.Type(), fix); err != nil || t.link != "" {
			return err
		}
	}
	if fix != nil {
		return fix.after(path)
	}
	return nil
}
