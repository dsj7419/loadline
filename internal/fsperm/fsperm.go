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

// MovableAncestor looks at dir and the folders above it that already exist,
// up to the volume root, or when top is set, up to top, which it trusts with
// every folder above it. It returns what lets someone other than the user,
// SYSTEM or Administrators delete or rename the nearest one that is not
// safe, or give themselves the right to, and so put a folder of their own in
// its place, or "" when none can. Folders on the way that do not exist yet
// are left out: Loadline creates them private.
//
// A folder is not safe when it belongs to anyone else, who may always
// change its permissions, or when it lets anyone else delete or rename
// itself, or what it holds, or change its permissions or owner. dir itself
// is judged by its owner alone, since making it private replaces the rest.
func MovableAncestor(dir, top string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("check the folders above %s: %w", dir, err)
	}
	if exists(dir) {
		if found, err := foreignOwner(dir); err != nil || found != "" {
			return found, err
		}
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
		// Every folder between dir and top is judged here, as the one that
		// holds the folder below it, before it is judged as itself.
		if exists(parent) {
			if found, err := foreignOwner(parent); err != nil || found != "" {
				return found, err
			}
			if found, err := emptiable(parent); err != nil || found != "" {
				return found, err
			}
		}
	}
}

// ownedBy reports folder as belonging to owner, someone other than those
// Loadline trusts with it.
func ownedBy(folder, owner string) string {
	return folder + " belongs to " + owner + ", who can give themselves the right to delete or rename it, or what it holds"
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
