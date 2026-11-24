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
	"path/filepath"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// syncRoot is fsperm.SyncRoot. Tests replace it: whether Windows lets a test
// register a sync root depends on the machine, and nothing at hand makes it
// fail to answer.
var syncRoot = fsperm.SyncRoot

// movableAncestor is fsperm.MovableAncestor. Tests replace it: an account
// that is not elevated cannot give a folder another owner, least of all an
// account this computer no longer has.
var movableAncestor = fsperm.MovableAncestor

// inProfile is fsperm.InProfile. Tests replace it: the folders a test makes
// are wherever its temporary folder is, which need not be in the profile.
var inProfile = fsperm.InProfile

// movableAdvice ends the report of a folder above the data directory that
// someone else could move aside.
const movableAdvice = ", and with it replace the binary every hook runs; " +
	"set LOADLINE_HOME to a folder in your user profile, or take that right away"

// Secure prepares dir to hold Loadline's files, as install and the server
// do each time they run. It first lets the user back into a dir whose own
// permissions keep them from listing it, where that widens nobody else's
// access, as Make does, then refuses what Verify refuses, and then does what
// Make does.
//
// The check of the folders above dir goes up to the volume root, or when top
// is set, stops below top, which it trusts with every folder above it.
func Secure(dir, top string) error {
	dir = filepath.Clean(dir)
	if err := letBackIn(dir); err != nil {
		return err
	}
	if err := Verify(dir, top); err != nil {
		return err
	}
	return Make(dir)
}

// Make prepares dir as Secure does, but without judging the folders above
// it, as the creation of the token does on a hook's path: there, a folder
// others could move aside still keeps the token from them. It refuses a dir
// in a folder that a cloud service keeps in sync, which would copy the token
// to the cloud, a dir that is a link, one that holds anything that is not
// Loadline's, which making the dir private would take other users' access
// to, a link among Loadline's own files, and a browser profile that is no
// folder, or not private as Loadline makes it (PrepareBrowserProfile). On
// Windows a link is any reparse point but a deduplicated file, which is the
// file it was.
//
// Before it judges any of that, it lets the user back into a dir whose own
// permissions keep them from listing it (fsperm.RegainList), which widens
// nobody else's access, and the dir keeps that until it is made private. It
// refuses a dir it cannot let them back into that way, as when a denial
// keeps them out, which only taking it off would undo, or when they may not
// change its permissions, which making it private takes too. It then
// creates dir, and each missing folder above it, private to the user, or
// makes the existing dir private: it judges each of Loadline's own files and
// folders, refusing a link, a file with another name and one that belongs
// to someone else, and takes off each that it found none of that in
// whatever its access list holds of its own, which making the dir private
// leaves (fsperm.EntryPrivate), even when it goes on to refuse another. Only
// when it refuses none, and has judged the names of every file still being
// written there, does it have the dir pass its list down. The browser
// profile it judges by itself alone, and changes nothing there
// (browserProfile).
//
// Several Loadline processes may run it at once, and write files in dir
// meanwhile (Temporary), which it refuses for a name elsewhere, and for an
// owner as it refuses Loadline's own files. One with that name alone it
// puts right where it is as it does them (repairTemporary): one begun before
// the dir was private has the list it had then. It passes over one that
// goes meanwhile, and refuses one whose permissions the user may not read
// or change (keptOutProblem). It changes nothing that is private already:
// Windows passes a new access list on a folder down to everything in it,
// those files among them.
func Make(dir string) error {
	dir = filepath.Clean(dir)
	if err := letBackIn(dir); err != nil {
		return err
	}
	problem, err := contentProblem(dir)
	if err != nil {
		return err
	}
	if problem != "" {
		return errors.New(problem)
	}
	if err := fsperm.MkdirAll(dir); err != nil {
		return fmt.Errorf("create the data directory: %w", err)
	}
	// Nothing is passed down to what the walk has not judged: a list passed
	// down would go through a folder that keeps the user out until then to a
	// file there that has a name elsewhere too, and change the file there.
	// The dir, and then each folder that is not private, gets the private
	// list alone, which Windows passes down to nothing it holds, so that the
	// walk can list it. A file is cleaned as soon as it is judged, since its
	// list reaches nothing else, and a folder once the walk is through what
	// it holds, which its list reaches.
	private, err := fsperm.DirPrivate(dir)
	if err != nil {
		return err
	}
	if !private {
		if err := fsperm.PrivateHere(dir); err != nil {
			return err
		}
	}
	unlocked := map[string]bool{}
	found, err := ownPaths(dir, &repair{
		before: func(path string, folder bool) error {
			problem, err := ownerProblem(dir, path)
			if err != nil {
				return err
			}
			if problem != "" {
				return errors.New(problem)
			}
			if !folder {
				return clearOwnEntries(path)
			}
			unlocked[path], err = makePrivateHere(path)
			return err
		},
		temporary: func(path string) error { return repairTemporary(dir, path) },
		after: func(path string) error {
			if !unlocked[path] {
				return nil
			}
			return clearExplicit(path)
		},
	})
	if err != nil {
		return err
	}
	if problem := found.linkProblem(); problem != "" {
		return errors.New(problem)
	}
	// A temporary name the walk could not judge may be a file elsewhere too,
	// which the list passed down would reach: it is left for the next run.
	if private || found.unjudged {
		return nil
	}
	// Last, as well as everything above, what other Loadline processes
	// began writing before the dir was private and the walk did not put
	// right itself: a file with a second name beside it, or one it found
	// going, should it stay.
	return fsperm.PassDown(dir)
}

// repairTemporary puts right path, a file another Loadline process is
// writing in dir, the data directory, which the walk found under that name
// alone: it refuses the file when it belongs to someone else, as it refuses
// any of Loadline's own (ownerProblem), and otherwise takes off it whatever
// its access list holds besides what its folder passes down
// (clearOwnEntries), as a file begun there before the data directory was
// private holds. Its writer goes on through the handle it holds. A file
// begun since has the private list, and is left as it is. One that goes
// meanwhile it passes over, and one whose permissions keep the user from
// reading or changing them it refuses (temporaryRefusal).
func repairTemporary(dir, path string) error {
	problem, err := ownerProblem(dir, path)
	switch {
	case err != nil:
		problem, err = temporaryRefusal(path, err)
	case problem == "":
		problem, err = temporaryRefusal(path, clearOwnEntries(path))
	}
	return problemError(problem, err)
}

// makePrivateHere gives path, one of Loadline's folders, the private list
// when it is not private, passing it down to nothing it holds
// (fsperm.PrivateHere), and reports whether it did.
func makePrivateHere(path string) (bool, error) {
	changed := false
	err := settle(func() error {
		private, err := fsperm.EntryPrivate(path)
		if err != nil || private {
			return err
		}
		changed = true
		return fsperm.PrivateHere(path)
	})
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return changed, err
}

// clearExplicit takes off path whatever its access list holds of its own,
// so that only what the data directory passes down applies. A path that is
// being deleted is waited for (settle), and once gone, left out.
func clearExplicit(path string) error {
	err := settle(func() error { return fsperm.ClearExplicit(path) })
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// clearOwnEntries takes off path, one of Loadline's files or folders in the
// private data directory, whatever its access list holds of its own: an
// entry given explicitly, a denial too, since one on the spool or the binary
// keeps the hooks out, or a list of its own other than the private one. Only
// what the data directory passes down then applies. A path that is private
// already is left as it is. A path that is being deleted is waited for
// (settle), and once gone, left out.
func clearOwnEntries(path string) error {
	err := settle(func() error {
		private, err := fsperm.EntryPrivate(path)
		if err != nil || private {
			return err
		}
		return fsperm.ClearExplicit(path)
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Verify returns the error Secure would refuse dir with, which says what to
// do, or nil when Secure would go ahead. It refuses a dir that another user
// could move aside, by deleting or renaming one of the folders above it that
// already exist, and then put a folder of their own, and a binary of their
// own, at the path the hooks run. It refuses what Make refuses. It changes
// nothing. A dir whose own permissions keep the user from listing it, which
// Secure lets them back into first, it refuses when Secure could not, and
// otherwise judges no further: neither what it holds nor where it really is
// can be seen until then, and Secure judges both once it has.
func Verify(dir, top string) error {
	dir = filepath.Clean(dir)
	problem, regainable, err := lockout(dir)
	if err == nil && problem == "" && !regainable {
		problem, err = refusal(dir, top)
	}
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
// others have to dir or to one of Loadline's files in it, a file still being
// written there with that name alone among them. It returns "" when there
// is none. A dir whose own permissions keep the user from listing it is
// reported first, saying whether install can let them back in. A dir that
// does not exist yet is reported with what install would refuse to make it
// for, judged from the folders above it that exist, or, when install would
// make it, is an error that matches fs.ErrNotExist. It changes nothing.
func Check(dir, top string) (string, error) {
	dir = filepath.Clean(dir)
	if _, err := os.Lstat(dir); err != nil {
		return notMadeYet(dir, top, err)
	}
	problem, regainable, err := lockout(dir)
	switch {
	case err != nil || problem != "":
		return problem, err
	case regainable:
		return dir + letBackInAdvice, nil
	}
	if problem, err := refusal(dir, top); err != nil || problem != "" {
		return problem, err
	}
	if problem, err := exposed(dir, fsperm.Exposure); err != nil || problem != "" {
		return problem, err
	}
	found, err := ownPaths(dir, nil)
	if err != nil {
		return "", err
	}
	if found.locked != "" {
		return found.locked + " keeps you from seeing what it holds; run loadline install", nil
	}
	for _, path := range found.paths {
		if problem, err := entryProblem(path); err != nil || problem != "" {
			return problem, err
		}
	}
	return temporaryProblem(found.temporary, entryProblem)
}

// temporaryFileProblem reports what Make would refuse path, a file still
// being written in dir with that name alone, for, as Verify does: one that
// belongs to someone else (ownerProblem), or one whose permissions Make
// would put right, since they are not what its folder passes down, but the
// user may not change (canChangeList, keptOut). It returns "" when there is
// none. A refused read of the file's owner or permissions it returns as the
// error it is, which temporaryProblem tells apart.
func temporaryFileProblem(dir, path string) (string, error) {
	if problem, err := ownerProblem(dir, path); err != nil || problem != "" {
		return problem, err
	}
	var private bool
	err := settle(func() (err error) {
		private, err = fsperm.EntryPrivate(path)
		return err
	})
	if err != nil || private {
		return "", err
	}
	changeable, err := canChangeList(path)
	if err != nil || changeable {
		return "", err
	}
	return keptOut(path)
}

// temporaryProblem returns the first problem judge finds with one of paths,
// files still being written (Temporary), or "" when it finds none, passing
// over a file that has gone or is going, and taking one that Windows keeps
// judge from for a problem (temporaryRefusal).
func temporaryProblem(paths []string, judge func(path string) (string, error)) (string, error) {
	for _, path := range paths {
		problem, err := judge(path)
		if err != nil {
			problem, err = temporaryRefusal(path, err)
		}
		if err != nil || problem != "" {
			return problem, err
		}
	}
	return "", nil
}

// notMadeYet reports what install would refuse to make dir for, which could
// not be read (unread), or returns unread when dir is there but unreadable,
// or missing and install would make it. A server refuses such a dir before
// it makes anything there, so doctor finds none to judge, and says why here.
func notMadeYet(dir, top string, unread error) (string, error) {
	unread = fmt.Errorf("read the data directory: %w", unread)
	if !errors.Is(unread, fs.ErrNotExist) {
		return "", unread
	}
	problem, err := refusal(dir, top)
	switch {
	case err != nil:
		return "", err
	case problem == "":
		return "", unread
	}
	return dir + " does not exist yet, and loadline install would refuse to make it: " + problem, nil
}

// entryProblem reports path, one of Loadline's files or folders in the data
// directory, as not private when its access list lets anyone else in, or ""
// when it does not. A path that is being deleted is waited for (settle),
// and once gone, has no problem.
func entryProblem(path string) (string, error) {
	var problem string
	err := settle(func() (err error) {
		problem, err = exposed(path, fsperm.EntryExposure)
		return err
	})
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return problem, err
}

// refusal reports what Verify refuses dir for, or "" when nothing.
func refusal(dir, top string) (string, error) {
	if problem, err := movable(dir, top); err != nil || problem != "" {
		return problem, err
	}
	return contentProblem(dir)
}

// contentProblem reports what Make refuses dir for, or "" when nothing.
func contentProblem(dir string) (string, error) {
	// Written with a separator at its end, a link's path would have Lstat
	// look at where it leads, and every check here would judge that.
	dir = filepath.Clean(dir)
	if problem := cloudProblem(dir); problem != "" {
		return problem, nil
	}
	if problem, err := dirLinkProblem(dir); err != nil || problem != "" {
		return problem, err
	}
	foreign, err := foreignEntry(dir)
	if err != nil {
		return "", err
	}
	if foreign != "" {
		return foreignProblem(dir, foreign), nil
	}
	// A folder the user may not list is no refusal: Make puts its list back
	// to what the data directory passes down, and judges what it holds then.
	found, err := ownPaths(dir, nil)
	if err != nil {
		return "", err
	}
	if problem := found.linkProblem(); problem != "" {
		return problem, nil
	}
	for _, path := range found.paths {
		if problem, err := ownerProblem(dir, path); err != nil || problem != "" {
			return problem, err
		}
	}
	judgeIn := func(path string) (string, error) { return temporaryFileProblem(dir, path) }
	if problem, err := temporaryProblem(found.temporary, judgeIn); err != nil || problem != "" {
		return problem, err
	}
	if found.profile != "" {
		// The user removes a refused profile, as the refusal says, and may do so
		// while a hook secures the data directory. One that is being deleted is
		// waited for (settle), and once gone, has no problem.
		var problem string
		err = settle(func() (err error) {
			problem, err = profileProblem(found.profile)
			return err
		})
		if !errors.Is(err, fs.ErrNotExist) && (err != nil || problem != "") {
			return problem, err
		}
	}
	return unchangeable(dir)
}

// cloudProblem reports dir as in a folder that a cloud service keeps in
// sync, which would copy the token and the binary every hook runs to the
// cloud, or "" when Windows does not say it is. Only that answer refuses.
// Windows cannot answer for a folder on a file system its cloud filter does
// not serve, such as exFAT or a network share, where no service it serves
// keeps anything in sync, so that it could not answer is no reason to
// refuse dir, and what kept it from answering is left out.
func cloudProblem(dir string) string {
	root, provider, under, _ := syncRoot(dir)
	if !under {
		return ""
	}
	// Windows names the service, but not always a folder that can be found
	// for the root.
	synced := root + ", which " + provider
	if root == "" {
		synced = "a folder that " + provider
	}
	return dir + " is in " + synced + " keeps in sync with the cloud, so the token and the binary every hook runs " +
		`would be copied there: set LOADLINE_HOME to a folder outside it, such as the default %LOCALAPPDATA%\loadline`
}

// dirLinkProblem reports dir as a link, in the words its kind calls for, or
// "" when it is not one or does not exist. Made private, a link would admit
// only the user while the folder it leads to, which then holds the token and
// the binary every hook runs, stayed as open as it is.
func dirLinkProblem(dir string) (string, error) {
	entry, err := inspect(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist), err == nil && !entry.Link:
		return "", nil
	case err != nil:
		return "", err
	}
	switch entry.Reparse {
	case fsperm.CloudFile:
		return cloudFileProblem(dir, entry.Tag), nil
	case fsperm.OtherReparse:
		return unknownReparseProblem(dir, entry.Tag) + "set LOADLINE_HOME to a new folder", nil
	default:
		// Where it leads only helps say what to do: a link that cannot be
		// read is refused just the same.
		problem := dir + " is a link, which the data directory must never be: set LOADLINE_HOME to the folder it leads to itself"
		if target, err := os.Readlink(dir); err == nil {
			problem = dir + " is a link to " + target + ", which the data directory must never be: set LOADLINE_HOME to " + target + " itself"
		}
		return problem, nil
	}
}

// ownerProblem reports path, one of Loadline's files or folders in dir, the
// data directory, as belonging to someone else, who may always let anyone at
// it, whatever Loadline makes of its access list, or "" when it does not. To
// an account this computer no longer has, it offers to take dir back with
// all it holds, or else path alone (takeBackDataDir). A path that is being
// deleted is waited for (settle), and once gone, has no problem.
func ownerProblem(dir, path string) (string, error) {
	var owner fsperm.Owner
	err := settle(func() (err error) {
		owner, err = foreignOwner(path)
		return err
	})
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil || owner == (fsperm.Owner{}):
		return "", err
	}
	return path + " belongs to " + owner.Name + ", who can always change who may read or replace it, " +
		"which Loadline's own files must not allow: remove it, then run loadline install" + takeBackDataDir(owner, dir, path), nil
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

// movable reports dir, or a folder above it, that someone else could move
// aside, and what to do about it, or "" when there is none. To an account
// this computer no longer has, it offers to take that folder back: dir as
// takeBackDataDir does, and a folder above it alone (takeBack).
func movable(dir, top string) (string, error) {
	found, err := movableAncestor(dir, top)
	if err != nil || found.Problem == "" {
		return "", err
	}
	// MovableAncestor names dir as given, made absolute, and every folder
	// above it, or among those that really hold it, otherwise.
	offer := takeBack(found.Owner, found.Folder, false)
	if abs, err := filepath.Abs(dir); err == nil && found.Folder == abs {
		offer = takeBackDataDir(found.Owner, found.Folder, found.Folder)
	}
	return found.Problem + movableAdvice + offer, nil
}

// takeBackDataDir offers to take back dir, the data directory, with all it
// holds, which takes back each of Loadline's files and folders at once, when
// owner, who owns dir or something in it, is an account this computer no
// longer has, dir is in the user's profile, and takeown /r would reach
// nothing but what dir holds (holdsOnlyLoadlines), which it walks dir to
// tell only then. Otherwise it offers reported alone, what the report
// names: dir itself, or the file or folder in it that owner owns, which is
// what taking back changes.
func takeBackDataDir(owner fsperm.Owner, dir, reported string) string {
	// No walk of the data directory for an owner that is offered nothing,
	// or for a data directory outside the profile, which takeBack would
	// offer nothing for with all it holds.
	if !owner.Gone {
		return ""
	}
	if held, err := fsperm.Resolved(dir); err == nil {
		if in, err := inProfile(held); err == nil && in && holdsOnlyLoadlines(dir) {
			return takeBack(owner, dir, true)
		}
	}
	return takeBack(owner, reported, false)
}

// takeBack offers the command that takes path back for the user
// (takeBackAdvice), and when recursive is set, all it holds too, when owner,
// who owns it or what it holds, is an account this computer no longer has,
// and path is in the user's profile, or returns "". Only then is it surely
// the user's: what an account that may still exist owns, or what is outside
// the profile, may be someone else's, which the user must not take.
//
// The command names path as the system resolves it (fsperm.Resolved), every
// link followed and each short name in full, which is where the profile is
// asked about, and what takeown then takes, whatever link or short name the
// path as given holds, and the offer calls it a folder or a file, as it is.
// A path that cannot be resolved, placed or looked at is offered nothing,
// and the report stands as it is.
func takeBack(owner fsperm.Owner, path string, recursive bool) string {
	if !owner.Gone {
		return ""
	}
	held, err := fsperm.Resolved(path)
	if err != nil {
		return ""
	}
	if in, err := inProfile(held); err != nil || !in {
		return ""
	}
	what := aFolderAndAllItHolds
	if !recursive {
		entry, err := inspect(held)
		if err != nil {
			return ""
		}
		what = aFolder
		if !entry.Folder {
			what = aFile
		}
	}
	return takeBackAdvice(held, what)
}

// taken is what a take-back command takes, which its offer names.
type taken uint8

const (
	// aFolder is a folder alone.
	aFolder taken = iota
	// aFolderAndAllItHolds is a folder and everything below it (/r).
	aFolderAndAllItHolds
	// aFile is a file.
	aFile
)

// holdsOnlyLoadlines reports whether takeown /r, given dir, the data
// directory, would take nothing but what dir holds. takeown /r goes into
// every folder below the one it is given, through a junction or a symbolic
// link to wherever that leads, and takes each file it comes to under all of
// its names, which a hard link can put anywhere else. So dir must hold
// nothing at its top but Loadline's own entries and the files the system
// leaves (foreignEntry), and nothing anywhere below it may be a reparse
// point of any kind or a file with a name outside dir. That covers what the
// browser keeps in its profile, which the checks of Loadline's own files do
// not go into (ownPaths), and the files Loadline is still writing, which
// they judge by their names in their own folder alone (judgeTemporary), and
// only after the check of the folders above, which offers to take back the
// data directory itself, has reported. A folder the user may not list could
// hold either, and so could whatever cannot be read, so each of those counts
// as one too. It lists folders and looks at each entry's attributes alone:
// it reads no file's content and changes nothing.
func holdsOnlyLoadlines(dir string) bool {
	if foreign, err := foreignEntry(dir); err != nil || foreign != "" {
		return false
	}
	names := fileNames{has: map[fsperm.FileID]uint64{}, reached: map[fsperm.FileID]uint64{}}
	if !names.walk(dir) {
		return false
	}
	for id, has := range names.has {
		if names.reached[id] != has {
			return false
		}
	}
	return true
}

// fileNames counts, for each file a walk comes to, how many names it has
// and by how many of them the walk came to it.
type fileNames struct {
	has, reached map[fsperm.FileID]uint64
}

// walk goes through everything below folder, without following a link,
// and counts each file's names. It reports false at the first reparse point,
// folder it cannot list, or path it cannot look at.
func (n fileNames) walk(folder string) bool {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return false
	}
	for _, listed := range entries {
		path := filepath.Join(folder, listed.Name())
		entry, err := inspect(path)
		if err != nil || entry.Reparse != fsperm.NotReparse {
			return false
		}
		if entry.Folder {
			if !n.walk(path) {
				return false
			}
			continue
		}
		n.has[entry.ID] = entry.Names
		n.reached[entry.ID]++
	}
	return true
}

// takeBackAdvice ends the report of path, a folder that an account this
// computer no longer has owns, or whose files it owns, or such a file
// itself, with what takes back what, which the offer names a folder or a
// file as it is: the command to paste into Command Prompt or PowerShell,
// with the path in double quotes. A path that either would read as another
// there, or a terminal would show otherwise than it is (fsperm.PasteUnsafe),
// is given the tool's name instead, and no command that would act on
// something other than what it seems to name. Only Windows tells that an
// account is gone, so the offer is made on Windows alone; a shell of macOS
// or Linux would need a backslash kept out of the command too, as the
// advice for a folder Loadline cannot let its user back into does there
// (fsperm.ResetAdvice).
func takeBackAdvice(path string, what taken) string {
	noun := "folder"
	if what == aFile {
		noun = "file"
	}
	lead := "; it belongs to an account this computer no longer has, as after a profile was moved or restored: if the " + noun + " is yours, "
	if fsperm.PasteUnsafe(path) {
		which := path
		if what == aFolderAndAllItHolds {
			which += " and all it holds"
		}
		return lead + "take ownership of " + which + " with takeown and then run loadline install"
	}
	command := `takeown /f "` + path + `"`
	if what == aFolderAndAllItHolds {
		command += " /r"
	}
	return lead + "run " + command + " and then loadline install"
}
