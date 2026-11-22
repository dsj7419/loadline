// Package fsperm keeps the directories Loadline writes its secrets to
// private to the user who runs it, and tells when one is not. The data
// directory holds the ingest token and the binary every hook runs, and
// LOADLINE_HOME can put it in a folder that lets other users in.
package fsperm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// andList joins names as a sentence lists them: "a", "a and b", "a, b and c".
func andList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	last := len(names) - 1
	return strings.Join(names[:last], ", ") + " and " + names[last]
}

// MovableAncestor looks at the folders above dir that already exist, up to
// the volume root, or when top is set, up to top, which it trusts with every
// folder above it. It returns what lets someone other than the user, SYSTEM
// or Administrators delete or rename the nearest one that is not safe, and
// so put a folder of their own in its place, or "" when none can. Folders
// on the way that do not exist yet are left out: Loadline creates them
// private.
func MovableAncestor(dir, top string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("check the folders above %s: %w", dir, err)
	}
	for x := dir; ; x = filepath.Dir(x) {
		parent := filepath.Dir(x)
		if parent == x {
			return "", nil
		}
		if x != dir && exists(x) {
			if found, err := movable(x); err != nil || found != "" {
				return found, err
			}
		}
		if top != "" && samePath(parent, top) {
			return "", nil
		}
		if exists(parent) {
			if found, err := emptiable(parent); err != nil || found != "" {
				return found, err
			}
		}
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
