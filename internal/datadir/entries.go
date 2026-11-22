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

// foreignEntry returns the name of the first entry of dir that is neither
// Loadline's nor a file the system leaves, or "" when there is none or dir
// does not exist. A folder or a link by a system file's name is someone
// else's: no system makes one.
func foreignEntry(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the data directory: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !Owns(name) && (!slices.Contains(systemNames, name) || !entry.Type().IsRegular()) {
			return name, nil
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

// foreignOwner is fsperm.ForeignOwner. Tests replace it: an account that is
// not elevated, or not root, cannot give a file another owner.
var foreignOwner = fsperm.ForeignOwner

// ownPaths returns what Loadline keeps in dir: each entry it owns, and
// everything below the folders among them, except the regular files that
// are still being written (Temporary). It follows no link: it stops at the
// first and returns it. A dir that does not exist holds nothing, and an
// entry that goes while it looks, as a spooled event does once drained, is
// left out.
func ownPaths(dir string) (paths []string, link string, err error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read the data directory: %w", err)
	}
	for _, entry := range entries {
		if !Owns(entry.Name()) {
			continue
		}
		found, link, err := walk(filepath.Join(dir, entry.Name()), entry.Type())
		paths = append(paths, found...)
		if err != nil || link != "" {
			return paths, link, err
		}
	}
	return paths, "", nil
}

// walk returns path, whose type its folder's listing gave as typ, and when
// it is a folder, everything below it, stopping at the first link and
// passing over the regular files that are still being written.
func walk(path string, typ fs.FileMode) (paths []string, link string, err error) {
	if Temporary(filepath.Base(path)) && typ.IsRegular() {
		// Not even looked at: it is its writer's, and on Windows, looking
		// at one a moment after its writer deleted it fails while the
		// deletion is pending. The listing, which opens nothing, tells a
		// file from a link, which no writer makes and which is refused.
		return nil, "", nil
	}
	var isLink bool
	var info fs.FileInfo
	err = settle(func() (err error) {
		if isLink, err = fsperm.IsLink(path); err != nil || isLink {
			return err
		}
		if info, err = os.Lstat(path); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		return nil
	})
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, "", nil
	case err != nil:
		return nil, "", err
	case isLink:
		return nil, path, nil
	}
	paths = []string{path}
	if !info.IsDir() {
		return paths, "", nil
	}
	entries, err := os.ReadDir(path)
	if errors.Is(err, fs.ErrNotExist) {
		return paths, "", nil
	}
	if err != nil {
		return paths, "", fmt.Errorf("read %s: %w", path, err)
	}
	for _, entry := range entries {
		found, link, err := walk(filepath.Join(path, entry.Name()), entry.Type())
		paths = append(paths, found...)
		if err != nil || link != "" {
			return paths, link, err
		}
	}
	return paths, "", nil
}
