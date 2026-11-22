package install

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
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
)

// Apply writes out to the settings file at path, if it still holds original;
// an empty original means the file did not exist when it was read. Otherwise
// it returns ErrChangedSincePlan and writes nothing.
//
// It first copies the current file to <path>.loadline-<20060102-150405>.bak,
// never overwriting an earlier backup, and returns that backup's path. It then
// writes out to a temporary file in the same directory and renames it over the
// file, so readers see either the old settings or the new, never a mix. A file
// that is a symlink stays one: the file it points to is replaced. The file's
// permissions are kept.
func Apply(path string, original, out []byte, now time.Time) (string, error) {
	current, err := os.ReadFile(filepath.Clean(path))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if len(original) != 0 {
			return "", fmt.Errorf("%s: %w", path, ErrChangedSincePlan)
		}
		return "", writeAtomically(path, out, newFileMode)
	case err != nil:
		return "", fmt.Errorf("read %s: %w", path, err)
	case !bytes.Equal(current, original):
		return "", fmt.Errorf("%s: %w", path, ErrChangedSincePlan)
	}

	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	backup, err := writeBackup(path, current, info.Mode().Perm(), now)
	if err != nil {
		return "", err
	}
	if err := writeAtomically(target, out, info.Mode().Perm()); err != nil {
		return backup, err
	}
	return backup, nil
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

// writeAtomically replaces the file at path with data by renaming a
// temporary file over it.
func writeAtomically(path string, data []byte, mode fs.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".loadline-*.tmp")
	if err != nil {
		return fmt.Errorf("create a temporary file beside %s: %w", path, err)
	}
	if err := finish(temp, data, mode); err != nil {
		_ = os.Remove(temp.Name())
		return fmt.Errorf("write %s: %w", temp.Name(), err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		_ = os.Remove(temp.Name())
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
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
