// Package datadir keeps Loadline's data directory the user's alone. The
// directory holds the ingest token, the binary every hook and status line
// runs, the spool and the history, and LOADLINE_HOME can put it anywhere.
// Install and the server secure it each time they run, and doctor reports
// what keeps it from being secure.
package datadir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// movableAdvice ends the report of a folder above the data directory that
// someone else could move aside.
const movableAdvice = ", and with it replace the binary every hook runs; " +
	"set LOADLINE_HOME to a folder in your user profile, or take that right away"

// Secure prepares dir to hold Loadline's files. It refuses what Verify
// refuses. It then creates dir, and each missing folder above it, private to
// the user, or makes the existing dir private and takes off Loadline's own
// files what access they were given explicitly, which making the folder
// private leaves.
//
// The check of the folders above dir goes up to the volume root, or when top
// is set, stops below top, which it trusts with every folder above it.
func Secure(dir, top string) error {
	if err := Verify(dir, top); err != nil {
		return err
	}
	if err := fsperm.MkdirAll(dir); err != nil {
		return fmt.Errorf("create the data directory: %w", err)
	}
	if err := fsperm.Private(dir); err != nil {
		return err
	}
	paths, link, err := ownPaths(dir)
	if err != nil {
		return err
	}
	if link != "" {
		return errors.New(linkProblem(link))
	}
	for _, path := range paths {
		if err := fsperm.ClearExplicit(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Verify returns the error Secure would refuse dir with, which says what to
// do, or nil when Secure would go ahead. It refuses a dir that another user
// could move aside, by deleting or renaming one of the folders above it that
// already exist, and then put a folder of their own, and a binary of their
// own, at the path the hooks run. It refuses a link among Loadline's own
// files in dir, which writing to would follow. It changes nothing.
func Verify(dir, top string) error {
	problem, err := refusal(dir, top)
	if err != nil {
		return err
	}
	if problem != "" {
		return errors.New(problem)
	}
	return nil
}

// Check reports, in a sentence that says what to do, the first thing that
// keeps dir from being the user's alone: what Verify refuses, or access that
// others have to dir or to one of Loadline's files in it. It returns "" when
// there is none. A missing dir is an error that matches fs.ErrNotExist.
func Check(dir, top string) (string, error) {
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("read the data directory: %w", err)
	}
	if problem, err := refusal(dir, top); err != nil || problem != "" {
		return problem, err
	}
	if problem, err := exposed(dir, fsperm.Exposure); err != nil || problem != "" {
		return problem, err
	}
	paths, _, err := ownPaths(dir)
	if err != nil {
		return "", err
	}
	for _, path := range paths {
		problem, err := exposed(path, fsperm.EntryExposure)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || problem != "" {
			return problem, err
		}
	}
	return "", nil
}

// refusal reports what Verify refuses dir for, or "" when nothing.
func refusal(dir, top string) (string, error) {
	if problem, err := movable(dir, top); err != nil || problem != "" {
		return problem, err
	}
	_, link, err := ownPaths(dir)
	if err != nil || link == "" {
		return "", err
	}
	return linkProblem(link), nil
}

// exposed reports path as not private when exposure finds someone else it
// admits, or "" when there is none.
func exposed(path string, exposure func(string) (string, error)) (string, error) {
	found, err := exposure(path)
	if err != nil || found == "" {
		return "", err
	}
	return path + " is not private: " + found + "; run loadline install", nil
}

// movable reports a folder above dir that someone else could move aside,
// and what to do about it, or "" when there is none.
func movable(dir, top string) (string, error) {
	found, err := fsperm.MovableAncestor(dir, top)
	if err != nil || found == "" {
		return "", err
	}
	return found + movableAdvice, nil
}
