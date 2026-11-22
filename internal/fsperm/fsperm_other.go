//go:build !windows

package fsperm

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
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

// ownerOf returns the user ID that owns path. Tests replace it: an account
// that is not root cannot give a folder another owner.
var ownerOf = func(path string) (uint32, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("read the owner of %s: %w", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("read the owner of %s: the system gives none", path)
	}
	return stat.Uid, nil
}

// ForeignOwner names the owner of path when that is anyone but the user or
// root, or returns "" when it is one of them. The owner may always change
// path's mode.
func ForeignOwner(path string) (string, error) {
	uid, err := ownerOf(path)
	if err != nil {
		return "", err
	}
	if uid == 0 || int64(uid) == int64(os.Getuid()) {
		return "", nil
	}
	id := strconv.FormatUint(uint64(uid), 10)
	name := "the account with user ID " + id
	if account, err := user.LookupId(id); err == nil {
		name = account.Username
	}
	return name, nil
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

// Transient reports nothing here as a refusal that passes in a moment: a
// file that is deleted or renamed goes from its name at once.
func Transient(error) bool { return false }

// EntryPrivate reports every file and folder inside a private folder as
// private here: each carries only its mode, and the folder's mode 0700 keeps
// everyone else from reaching it.
func EntryPrivate(string) (bool, error) { return true, nil }

// EntryExposure says nothing here of a file or folder inside a private
// folder: the folder's mode 0700 keeps everyone else from reaching it.
func EntryExposure(string) (string, error) { return "", nil }
