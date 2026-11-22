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

// ownPaths returns what Loadline keeps in dir: each entry it owns, and
// everything below the folders among them. It follows no link: it stops at
// the first and returns it. A dir that does not exist holds nothing, and an
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
		found, link, err := walk(filepath.Join(dir, entry.Name()))
		paths = append(paths, found...)
		if err != nil || link != "" {
			return paths, link, err
		}
	}
	return paths, "", nil
}

// walk returns path and, when it is a folder, everything below it, stopping
// at the first link.
func walk(path string) (paths []string, link string, err error) {
	isLink, err := fsperm.IsLink(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, "", nil
	case err != nil:
		return nil, "", err
	case isLink:
		return nil, path, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", path, err)
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
		found, link, err := walk(filepath.Join(path, entry.Name()))
		paths = append(paths, found...)
		if err != nil || link != "" {
			return paths, link, err
		}
	}
	return paths, "", nil
}
