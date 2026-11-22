//go:build !windows

package fsperm

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// privateDirMode lets only the user list, enter and change a directory.
const privateDirMode fs.FileMode = 0o700

// Private gives dir mode 0700. The files Loadline writes there are created
// for the user alone already, so their own modes are left as they are.
func Private(dir string) error {
	if err := os.Chmod(dir, privateDirMode); err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	return nil
}

// MkdirAll creates dir and every missing folder above it, each with mode
// 0700 from the moment it exists. A folder that already exists is left as it
// is.
func MkdirAll(dir string) error {
	if err := os.MkdirAll(dir, privateDirMode); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return nil
}

// Exposure says who, besides the user, the mode of path lets in: group,
// others or both. It returns "" when the mode admits nobody else.
func Exposure(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read the mode of %s: %w", path, err)
	}
	mode := info.Mode().Perm()
	var who []string
	if mode&0o070 != 0 {
		who = append(who, "group")
	}
	if mode&0o007 != 0 {
		who = append(who, "others")
	}
	if len(who) == 0 {
		return "", nil
	}
	return fmt.Sprintf("its mode %#o also admits %s", mode, andList(who)), nil
}

// movable says nothing here: whether others may rename a folder depends on
// the folder that holds it, which emptiable judges.
func movable(string) (string, error) { return "", nil }

// emptiable says who besides the user may rename the folders that folder
// holds: group or others, when they may write to it and it lacks the sticky
// bit, which would leave each folder to its owner. It returns "" when nobody
// may.
func emptiable(folder string) (string, error) {
	info, err := os.Stat(folder)
	if err != nil {
		return "", fmt.Errorf("read the mode of %s: %w", folder, err)
	}
	mode := info.Mode()
	if mode&fs.ModeSticky != 0 {
		return "", nil
	}
	var who []string
	if mode&0o020 != 0 {
		who = append(who, "group")
	}
	if mode&0o002 != 0 {
		who = append(who, "others")
	}
	if len(who) == 0 {
		return "", nil
	}
	return fmt.Sprintf("%s lets %s rename the folders in it (mode %#o, without the sticky bit)", folder, andList(who), mode.Perm()), nil
}

// samePath reports whether a and b name the same folder.
func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// ClearExplicit does nothing here: a file carries only its mode, and the
// 0700 folder that holds it keeps everyone else out whatever that is.
func ClearExplicit(string) error { return nil }

// IsLink reports whether path itself is a symbolic link.
func IsLink(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return info.Mode()&fs.ModeSymlink != 0, nil
}

// EntryExposure says nothing here of a file or folder inside a private
// folder: the folder's mode 0700 keeps everyone else from reaching it.
func EntryExposure(string) (string, error) { return "", nil }
