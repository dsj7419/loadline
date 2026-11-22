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
// is already identical, and returns InstalledPath(dataDir).
//
// The copy is written to a temporary file and renamed over the target.
// Windows will not replace an executable that is running, as the installed
// one is whenever a hook or the server is, but it will rename one, so then
// the running binary is moved aside to loadline.exe.old, replacing any older
// one, and the rename is tried again.
func SelfInstall(src, dataDir string) (string, error) {
	target := installTarget(dataDir)
	same, err := sameContent(src, target)
	if err != nil {
		return "", err
	}
	if same {
		return filepath.ToSlash(target), nil
	}

	if err := os.MkdirAll(filepath.Dir(target), executableMode); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}
	temp, err := copyToTemp(src, filepath.Dir(target))
	if err != nil {
		return "", err
	}
	if err := replaceFile(temp, target); err != nil {
		_ = os.Remove(temp)
		return "", err
	}
	return filepath.ToSlash(target), nil
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

func replaceFile(temp, target string) error {
	err := os.Rename(temp, target)
	if err == nil {
		return nil
	}
	if _, statErr := os.Stat(target); statErr != nil {
		return fmt.Errorf("install %s: %w", target, err)
	}

	aside := target + ".old"
	if asideErr := os.Rename(target, aside); asideErr != nil {
		return fmt.Errorf("install %s: %w", target, errors.Join(err, asideErr))
	}
	if err := os.Rename(temp, target); err != nil {
		// Put the previous binary back so the hooks that run it keep working.
		_ = os.Rename(aside, target)
		return fmt.Errorf("install %s: %w", target, err)
	}
	return nil
}
