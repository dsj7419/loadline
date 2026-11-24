package install

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// ErrChangedSincePlan means the settings file no longer holds the bytes a
// plan was made from, so applying the plan would lose someone's edit.
var ErrChangedSincePlan = errors.New("the settings file changed after it was read")

const (
	// newFileMode keeps a settings file that Apply creates private, as it
	// holds the ingest token.
	newFileMode fs.FileMode = 0o600
	// backupTimeLayout stamps backups to the second.
	backupTimeLayout = "20060102-150405"
	// maxBackupsPerSecond bounds the search for a free backup name.
	maxBackupsPerSecond = 100
	// keptBackups is how many of its backups of a settings file Apply keeps.
	// Each holds the ingest token the file held, so they are not kept for
	// ever.
	keptBackups = 3
	// leftoverAge is how old one of Apply's temporary files must be before
	// another Apply takes it for the leftover of one that crashed. A younger
	// one may belong to an Apply that is still writing.
	leftoverAge = time.Hour
)

// testHookBeforeRecheck, when set by a test, runs just before Apply reads
// the settings file one last time ahead of replacing it.
var testHookBeforeRecheck func()

// finalPath is fsperm.FinalPath. Tests replace it: no file system at hand
// leaves the system unable to say where a file is.
var finalPath = fsperm.FinalPath

// Apply writes out to the settings file at path, if it still holds original;
// an empty original means the file did not exist when it was read. Otherwise
// it returns ErrChangedSincePlan and writes nothing.
//
// It first copies the current file to <path>.loadline-<20060102-150405>.bak,
// never overwriting an earlier backup, and returns that backup's path. It then
// writes out to a temporary file in the same directory, checks once more that
// the file still holds original, and renames the temporary file over it, so
// readers see either the old settings or the new, never a mix. A file that is
// a symlink stays one: the file it points to is replaced. The file and its
// backup keep the owner's permissions, but group and others lose theirs, as
// both may hold the ingest token. Temporary files that an earlier, crashed
// Apply left beside the file are removed once they are an hour old, and once
// the file is written, only the newest three of the backups Apply made of it
// are kept, the one it has just made among them.
func Apply(path string, original, out []byte, now time.Time) (string, error) {
	current, err := os.ReadFile(filepath.Clean(path))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if len(original) != 0 {
			return "", fmt.Errorf("%s: %w", path, ErrChangedSincePlan)
		}
		if err := writeAtomically(path, out, newFileMode, now, func() error { return unchanged(path, nil) }); err != nil {
			return "", err
		}
		pruneBackups(path, "")
		return "", nil
	case err != nil:
		return "", fmt.Errorf("read %s: %w", path, err)
	case !bytes.Equal(current, original):
		return "", fmt.Errorf("%s: %w", path, ErrChangedSincePlan)
	}

	target, err := linkTarget(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	mode := privateMode(info.Mode().Perm())
	backup, err := writeBackup(path, current, mode, now)
	if err != nil {
		return "", err
	}
	if err := writeAtomically(target, out, mode, now, func() error { return unchanged(path, current) }); err != nil {
		if errors.Is(err, ErrChangedSincePlan) {
			// Nothing was replaced, so the backup would only be clutter
			// that holds the token.
			_ = os.Remove(backup)
			return "", err
		}
		return backup, err
	}
	pruneBackups(path, backup)
	return backup, nil
}

// linkTarget returns the file that writing the settings file at path
// replaces: the one at the end of every link on the way. It resolves path as
// filepath.EvalSymlinks does, as Apply always has, and asks the system where
// the file is only when that fails, as it does on Windows at a junction on
// the way, such as one that moves a home or config folder to another drive.
// The system cannot say for every path EvalSymlinks resolves, such as one
// that names a volume without a drive letter by its GUID name. When neither
// resolves path, the system's error is the one returned: it says why, and it
// names path.
func linkTarget(path string) (string, error) {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		return target, nil
	}
	return finalPath(path)
}

// privateMode is mode without any permission for group or others.
func privateMode(mode fs.FileMode) fs.FileMode {
	return mode &^ 0o077
}

// unchanged reads the file at path once more and returns ErrChangedSincePlan
// unless it still holds original, or is still missing when original is nil.
func unchanged(path string, original []byte) error {
	if testHookBeforeRecheck != nil {
		testHookBeforeRecheck()
	}
	current, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		if original == nil {
			return nil
		}
		return fmt.Errorf("%s: %w", path, ErrChangedSincePlan)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if original == nil || !bytes.Equal(current, original) {
		return fmt.Errorf("%s: %w", path, ErrChangedSincePlan)
	}
	return nil
}

func writeBackup(path string, data []byte, mode fs.FileMode, now time.Time) (string, error) {
	stem := path + ".loadline-" + now.Format(backupTimeLayout)
	for n := 1; n <= maxBackupsPerSecond; n++ {
		name := stem + ".bak"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.bak", stem, n)
		}
		file, err := os.OpenFile(filepath.Clean(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create backup of %s: %w", path, err)
		}
		if err := finish(file, data, mode); err != nil {
			_ = os.Remove(name)
			return "", fmt.Errorf("write backup %s: %w", name, err)
		}
		return name, nil
	}
	return "", fmt.Errorf("create backup of %s: %d backups from this second already exist", path, maxBackupsPerSecond)
}

// pruneBackups removes, as well as it can, all but keptBackups of the
// backups writeBackup made of the file at path: made, the one just made,
// which is "" when there is none, and the newest of the others. Their names
// say how new they are, and a clock that was set back can leave older ones
// that look newer than made. It knows them by the names writeBackup gives
// them, so it never removes any other file.
func pruneBackups(path, made string) {
	dir, base := filepath.Dir(path), filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	keep := keptBackups
	var backups []backupName
	for _, entry := range entries {
		b, ok := parseBackupName(base, entry.Name())
		if !ok || !entry.Type().IsRegular() {
			continue
		}
		if made != "" && entry.Name() == filepath.Base(made) {
			keep--
			continue
		}
		backups = append(backups, b)
	}
	slices.SortFunc(backups, func(a, b backupName) int {
		return cmp.Or(strings.Compare(b.stamp, a.stamp), cmp.Compare(b.n, a.n))
	})
	for _, b := range backups[min(keep, len(backups)):] {
		_ = os.Remove(filepath.Join(dir, b.name))
	}
}

// backupName is the name of a backup writeBackup made: its time stamp, and
// which backup of that second it is, counting from 1.
type backupName struct {
	name  string
	stamp string
	n     int
}

// parseBackupName reads name as writeBackup names a backup of the file
// called base, <base>.loadline-<stamp>.bak or <base>.loadline-<stamp>-<n>.bak
// with n from 2 to maxBackupsPerSecond, and reports whether it is one.
func parseBackupName(base, name string) (backupName, bool) {
	rest, ok := strings.CutPrefix(name, base+".loadline-")
	if !ok {
		return backupName{}, false
	}
	rest, ok = strings.CutSuffix(rest, ".bak")
	if !ok || len(rest) < len(backupTimeLayout) {
		return backupName{}, false
	}
	stamp, suffix := rest[:len(backupTimeLayout)], rest[len(backupTimeLayout):]
	if _, err := time.Parse(backupTimeLayout, stamp); err != nil {
		return backupName{}, false
	}
	if suffix == "" {
		return backupName{name: name, stamp: stamp, n: 1}, true
	}
	digits, ok := strings.CutPrefix(suffix, "-")
	n, err := strconv.Atoi(digits)
	if !ok || err != nil || n < 2 || n > maxBackupsPerSecond || strconv.Itoa(n) != digits {
		return backupName{}, false
	}
	return backupName{name: name, stamp: stamp, n: n}, true
}

// writeAtomically replaces the file at path with data by renaming a
// temporary file over it, once recheck finds nothing changed.
func writeAtomically(path string, data []byte, mode fs.FileMode, now time.Time, recheck func() error) error {
	dir, prefix := filepath.Dir(path), "."+filepath.Base(path)+".loadline-"
	removeLeftovers(dir, prefix, now)
	temp, err := os.CreateTemp(dir, prefix+"*"+tempSuffix)
	if err != nil {
		return fmt.Errorf("create a temporary file beside %s: %w", path, err)
	}
	if err := finish(temp, data, mode); err != nil {
		_ = os.Remove(temp.Name())
		return fmt.Errorf("write %s: %w", temp.Name(), err)
	}
	if err := recheck(); err != nil {
		_ = os.Remove(temp.Name())
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		_ = os.Remove(temp.Name())
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

const tempSuffix = ".tmp"

// removeLeftovers removes, as well as it can, the temporary files in dir
// whose names start with prefix and that are older than leftoverAge.
func removeLeftovers(dir, prefix string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, tempSuffix) {
			continue
		}
		if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) >= leftoverAge {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// finish writes data to a new file, gives it mode, flushes it to disk and
// closes it.
func finish(file *os.File, data []byte, mode fs.FileMode) error {
	_, err := file.Write(data)
	if err == nil {
		err = file.Chmod(mode)
	}
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
