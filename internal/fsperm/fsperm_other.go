//go:build !windows

package fsperm

import (
	"fmt"
	"io/fs"
	"os"
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
