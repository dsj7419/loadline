package install

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/fsperm"
)

// executableMode lets only the user run, read and replace the installed
// binary.
const executableMode fs.FileMode = 0o700

// InstalledPath returns where SelfInstall puts the binary,
// <dataDir>/bin/loadline (loadline.exe on Windows), with forward slashes as
// settings need. It creates nothing.
func InstalledPath(dataDir string) string {
	return filepath.ToSlash(installTarget(dataDir))
}

func installTarget(dataDir string) string {
	name := "loadline"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dataDir, "bin", name)
}

// SelfInstall copies the binary at src, normally the running one, to
// InstalledPath(dataDir), which settings refer to, so hooks keep working
// wherever the downloaded copy goes. It does nothing when the installed copy
// is already identical, and returns InstalledPath(dataDir). Otherwise it
// first makes the bin directory private to the user, and refuses one that
// is a link, which the copy would follow.
//
// The copy is written to a temporary file and renamed over the target.
// Windows will not replace an executable that is running, as the installed
// one is whenever a hook or the server is, but it will rename one, so then
// the running binary is moved aside to loadline.exe.old, replacing any older
// one, or to the first free loadline.exe.old-<n> when the older one is still
// running too, and the rename is tried again. RemoveAside removes what was
// moved aside once nothing runs it.
func SelfInstall(src, dataDir string) (string, error) {
	target := installTarget(dataDir)
	same, err := sameContent(src, target)
	if err != nil {
		return "", err
	}
	if same {
		return filepath.ToSlash(target), nil
	}

	bin := filepath.Dir(target)
	if err := datadir.CheckLink(bin); err != nil {
		return "", err
	}
	if err := fsperm.MkdirAll(bin); err != nil {
		return "", err
	}
	// Every hook runs the binary here, so only the user may replace it,
	// whatever the folder above passes down. Made private before the copy is
	// written, bin never lets anyone else at the new binary, even for a
	// moment, and the binaries already in it lose what they inherited.
	if err := fsperm.Private(bin); err != nil {
		return "", err
	}
	temp, err := copyToTemp(src, bin)
	if err != nil {
		return "", err
	}
	if err := replaceFile(temp, target); err != nil {
		_ = os.Remove(temp)
		return "", err
	}
	return filepath.ToSlash(target), nil
}

// PlanSelfInstall reports what SelfInstall would change, writing nothing: it
// adds the binary when there is none, updates it when it differs from src,
// and leaves an identical one alone.
func PlanSelfInstall(src, dataDir string) ([]Change, error) {
	target := installTarget(dataDir)
	same, err := sameContent(src, target)
	if err != nil || same {
		return nil, err
	}
	action := ActionUpdate
	if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
		action = ActionAdd
	}
	return []Change{{Path: "bin/" + filepath.Base(target), Action: action, Detail: "copy of " + src}}, nil
}

// sameContent reports whether the file at target holds what src holds. A
// missing target holds nothing; a missing src is an error.
func sameContent(src, target string) (bool, error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", src, err)
	}
	targetInfo, err := os.Stat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", target, err)
	}
	if srcInfo.Size() != targetInfo.Size() {
		return false, nil
	}

	srcSum, err := fileDigest(src)
	if err != nil {
		return false, err
	}
	targetSum, err := fileDigest(target)
	if err != nil {
		return false, err
	}
	return bytes.Equal(srcSum, targetSum), nil
}

func fileDigest(path string) ([]byte, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return digest.Sum(nil), nil
}

// copyToTemp copies src to a new executable file in dir and returns its path.
func copyToTemp(src, dir string) (string, error) {
	in, err := os.Open(filepath.Clean(src))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.CreateTemp(dir, ".loadline-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create a temporary file in %s: %w", dir, err)
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chmod(executableMode)
	}
	if err == nil {
		err = out.Sync()
	}
	if err := errors.Join(err, out.Close()); err != nil {
		_ = os.Remove(out.Name())
		return "", fmt.Errorf("copy %s to %s: %w", src, dir, err)
	}
	return out.Name(), nil
}

// rename is os.Rename. Tests replace it to make a file look held by another
// process.
var rename = os.Rename

// renameBudget bounds how long replacing the binary waits out another
// process's brief hold on it: an antivirus scan of the new copy, or the
// loader of a hook that has just started from the old one.
var renameBudget = 2 * time.Second

// maxRenameDelay caps the pause between attempts.
const maxRenameDelay = 100 * time.Millisecond

// replaceFile renames temp over target. Windows refuses to replace a binary
// that is running, for as long as it runs, so a refusal is not worth waiting
// out: target is moved aside instead, and temp takes its place.
func replaceFile(temp, target string) error {
	err := rename(temp, target)
	if err == nil {
		return nil
	}
	_, statErr := os.Stat(target)
	switch {
	case errors.Is(statErr, fs.ErrNotExist):
		// A first install. Nothing is at target, so a refusal can only be a
		// brief hold, such as a scan of the new copy.
		if err := renameRetrying(temp, target, time.Now().Add(renameBudget), isHeld); err != nil {
			return fmt.Errorf("install %s: %w", target, err)
		}
		return nil
	case statErr != nil:
		return fmt.Errorf("install %s: %w", target, errors.Join(err, statErr))
	}

	deadline := time.Now().Add(renameBudget)
	aside, asideErr := moveAside(target, deadline)
	if asideErr != nil {
		return fmt.Errorf("install %s: %w", target, errors.Join(err, asideErr))
	}
	// Nothing is at target now, so a refusal can only be a brief hold.
	if err := renameRetrying(temp, target, deadline, isHeld); err != nil {
		// Put the previous binary back so the hooks that run it keep working.
		if rollbackErr := renameRetrying(aside, target, time.Now().Add(renameBudget), isHeld); rollbackErr != nil {
			return fmt.Errorf("install %s: %w; the previous binary is left at %s, as putting it back failed: %w",
				target, err, aside, rollbackErr)
		}
		return fmt.Errorf("install %s: %w", target, err)
	}
	return nil
}

// maxAsideNames bounds the search for a free name to move a binary aside to.
const maxAsideNames = 100

// asideName returns the name moveAside tries n-th for target: target.old for
// n = 0, and target.old-<n> for n from 1 to maxAsideNames.
func asideName(target string, n int) string {
	if n == 0 {
		return target + ".old"
	}
	return target + ".old-" + strconv.Itoa(n)
}

// moveAside renames target to target.old, replacing an older one, and when
// that older one is running too, to the first name target.old-<n> not yet
// taken. It returns the new name. It waits until deadline for another
// process to let go of target.
func moveAside(target string, deadline time.Time) (string, error) {
	aside := asideName(target, 0)
	// A running .old refuses to be replaced for good, so only a sharing or
	// lock violation, which comes from a hold on target, is worth waiting out.
	err := renameRetrying(target, aside, deadline, isSharingViolation)
	if err == nil {
		return aside, nil
	}
	for n := 1; n <= maxAsideNames; n++ {
		candidate := asideName(target, n)
		if _, statErr := os.Lstat(candidate); !errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		// Nothing holds a name that does not exist yet, so any refusal here
		// is a brief hold on target.
		if renameErr := renameRetrying(target, candidate, deadline, isHeld); renameErr != nil {
			return "", errors.Join(err, renameErr)
		}
		return candidate, nil
	}
	return "", fmt.Errorf("%w; %s to -%d are taken too", err, asideName(target, 1), maxAsideNames)
}

// renameRetrying renames from to to, trying again after a growing pause
// while the failure is retryable and deadline allows, and at least once.
func renameRetrying(from, to string, deadline time.Time, retryable func(error) bool) error {
	delay := time.Millisecond
	for {
		err := rename(from, to)
		if err == nil || !retryable(err) || !time.Now().Add(delay).Before(deadline) {
			return err
		}
		time.Sleep(delay)
		delay = min(2*delay, maxRenameDelay)
	}
}

// AsideBinaries returns the binaries that upgrades moved aside in the bin
// folder of dataDir, in the order moveAside names them: each regular file
// named as moveAside names one. It changes nothing. It refuses a bin folder
// that is a link, since what it leads to is not Loadline's.
func AsideBinaries(dataDir string) ([]string, error) {
	target := installTarget(dataDir)
	if err := datadir.CheckLink(filepath.Dir(target)); err != nil {
		return nil, err
	}
	var found []string
	for n := range maxAsideNames + 1 {
		path := asideName(target, n)
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			// The names share one folder, so the rest would fail alike.
			return found, fmt.Errorf("read %s: %w", path, err)
		case info.Mode().IsRegular():
			found = append(found, path)
		}
	}
	return found, nil
}

// remove is os.Remove. Tests replace it to make a removal fail.
var remove = os.Remove

// RemoveAside removes the binaries AsideBinaries finds, and returns those it
// removed. Windows refuses to delete a binary while it runs, as an old
// server or hook may for long after the upgrade, so one held is left for a
// later run without a word. Any other failure is returned, once the rest
// are removed, for the caller to report: this is housekeeping, which never
// makes an install or a server fail.
func RemoveAside(dataDir string) ([]string, error) {
	found, err := AsideBinaries(dataDir)
	failures := []error{err}
	var removed []string
	for _, path := range found {
		err := remove(path)
		switch {
		case err == nil:
			removed = append(removed, path)
		case isHeld(err), errors.Is(err, fs.ErrNotExist):
			// Held, or gone already: an install and a server that start
			// together both remove these, and one finds the other was first.
		default:
			failures = append(failures, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	return removed, errors.Join(failures...)
}
