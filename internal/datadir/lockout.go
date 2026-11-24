package datadir

import (
	"errors"
	"io/fs"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// canChangeList is fsperm.CanChangeList. Tests replace it: an account that is
// not elevated cannot give a folder another owner, which would keep it from
// changing the folder's list.
var canChangeList = fsperm.CanChangeList

// ownerName is fsperm.OwnerName. Tests replace it, for the same reason, and
// because a folder that keeps its owner from this process gives it nothing
// else either.
var ownerName = fsperm.OwnerName

// letBackInAdvice ends doctor's report of a data directory that keeps the user
// out, which install can let them back into.
const letBackInAdvice = " keeps you out, though you can let yourself back in; run loadline install, which does that and makes it private"

// letBackIn lets the user back into dir when its own permissions keep them
// from listing it, as far as that widens nobody else's access
// (fsperm.RegainList), or returns the error that refuses dir: when they may
// not change its permissions, or a denial keeps them out whatever is added.
func letBackIn(dir string) error {
	locked, changeable, err := shutOut(dir)
	if err != nil || !locked {
		return err
	}
	if changeable {
		if _, err := fsperm.RegainList(dir); !errors.Is(err, fsperm.ErrStillLocked) {
			return err
		}
	}
	return errors.New(lockedOutProblem(dir))
}

// lockout reports, changing nothing, whether dir keeps the user from listing
// it, and so from judging what it holds: regainable when letBackIn would let
// them back in (fsperm.ListableOnceRegained), and otherwise the sentence
// letBackIn would refuse dir with.
func lockout(dir string) (problem string, regainable bool, err error) {
	locked, changeable, err := shutOut(dir)
	if err != nil || !locked {
		return "", false, err
	}
	if changeable {
		regainable, err = fsperm.ListableOnceRegained(dir)
		if err != nil || regainable {
			return "", regainable, err
		}
	}
	return lockedOutProblem(dir), false, nil
}

// shutOut reports whether dir keeps the user from listing it, and if so,
// whether they may change its permissions. A dir that does not exist keeps
// nobody out, and neither does a link, which Make refuses for what it is
// before it judges anything else (dirLinkProblem), whether or not what it
// leads to can be listed.
func shutOut(dir string) (locked, changeable bool, err error) {
	entry, err := inspect(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist), err == nil && entry.Link:
		return false, false, nil
	case err != nil:
		return false, false, err
	}
	listable, err := fsperm.Listable(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist), err == nil && listable:
		return false, false, nil
	case err != nil:
		return false, false, err
	}
	changeable, err = canChangeList(dir)
	if err != nil {
		return false, false, err
	}
	return true, changeable, nil
}

// unchangeable reports dir, a folder the user may list, as one whose
// permissions they may not change, which making it private takes, or ""
// when they may, it is private already or it does not exist.
func unchangeable(dir string) (string, error) {
	private, err := fsperm.DirPrivate(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist), err == nil && private:
		return "", nil
	case err != nil:
		return "", err
	}
	changeable, err := canChangeList(dir)
	if err != nil || changeable {
		return "", err
	}
	return dir + " cannot be made private, since you may not change who may use it; " + ownerClause(dir) +
		": set LOADLINE_HOME to a folder of your own, or have an administrator " + fsperm.ResetAdvice(dir) +
		", then run loadline install", nil
}

// lockedOutProblem refuses dir, whose permissions keep the user out and which
// Loadline cannot let them back into without widening who else may use it.
func lockedOutProblem(dir string) string {
	return dir + " keeps you out, and Loadline cannot let you back in without widening who else may use it; " + ownerClause(dir) +
		": an administrator can " + fsperm.ResetAdvice(dir) + ", then run loadline install"
}

// ownerClause names the owner of dir, who may always change its permissions, as
// far as that can be read: a folder that gives this process nothing may not
// let it read its owner either.
func ownerClause(dir string) string {
	name, err := ownerName(dir)
	if err != nil {
		return "its owner cannot be read"
	}
	return "it belongs to " + name
}
