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

// Entry is what Inspect tells of a path itself.
type Entry struct {
	// Link reports a symbolic link, or on Windows a junction or another
	// reparse point, which opening the path would follow somewhere else.
	Link bool
	// Folder reports a folder.
	Folder bool
	// Names is how many names the file has: more than one when it was hard
	// linked, and it is then also wherever its other names are.
	Names uint64
}

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
//
// A link on the way, a junction or a symbolic link, or on Windows a drive
// letter that subst made, puts dir among the folders that hold the link's
// target, which dir's own names do not show. So the folders that really
// hold dir are judged too, as the system resolves them, against top
// resolved the same way.
func MovableAncestor(dir, top string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("check the folders above %s: %w", dir, err)
	}
	if found, err := movableChain(dir, top); err != nil || found != "" {
		return found, err
	}
	// The folders that really hold it go unjudged when they cannot be found,
	// so such a path is refused; one in the user's profile resolves.
	const unresolved = "find the folders that hold %s: %w; set LOADLINE_HOME to a folder in your user profile"
	held, err := resolved(dir)
	if err != nil {
		return "", fmt.Errorf(unresolved, dir, err)
	}
	if samePath(held, dir) {
		return "", nil
	}
	heldTop := top
	if top != "" {
		if heldTop, err = resolved(top); err != nil {
			return "", fmt.Errorf(unresolved, top, err)
		}
	}
	found, err := movableChain(held, heldTop)
	if err != nil || found == "" {
		return "", err
	}
	// dir stays as the user gave it, so that they know it, as does any
	// folder above it found by dir's own names. held, and the folder found
	// among those that really hold dir, are as the system resolved them,
	// which is the truth on disk: every link followed and, on Windows, each
	// name in its long form, whatever short 8.3 name dir gave.
	return dir + " is really " + held + ", and " + found, nil
}

// movableChain judges dir and the folders above it, as MovableAncestor
// does, by the names in dir alone.
func movableChain(dir, top string) (string, error) {
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

// foreignOwner reports folder as belonging to someone other than those
// Loadline trusts with it (ForeignOwner), or "" when it does not.
func foreignOwner(folder string) (string, error) {
	owner, err := ForeignOwner(folder)
	if err != nil || owner == "" {
		return "", err
	}
	return folder + " belongs to " + owner + ", who can give themselves the right to delete or rename it, or what it holds", nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// resolved returns path with every link in it followed (finalPath), as far
// as path exists, and the rest of it as written. The rest does not exist
// yet, and Loadline creates it private.
func resolved(path string) (string, error) {
	var rest []string
	for existing := path; ; existing = filepath.Dir(existing) {
		if exists(existing) {
			final, err := finalPath(existing)
			if err != nil {
				return "", err
			}
			return filepath.Join(append([]string{final}, rest...)...), nil
		}
		if filepath.Dir(existing) == existing {
			return path, nil
		}
		rest = append([]string{filepath.Base(existing)}, rest...)
	}
}
