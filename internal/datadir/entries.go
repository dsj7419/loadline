package datadir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// ownNames are the entries Loadline makes in the data directory: the token,
// the installed binary's folder, the spool, the server's log, the history
// and the files SQLite keeps beside it, the stamps the server and the status
// line leave, the file a server holds locked while it holds the history,
// under LOADLINE_HOME the config file, and the dashboard's browser
// profile.
var ownNames = []string{
	"token", "bin", "spool", "loadline.log",
	"loadline.db", "loadline.db-wal", "loadline.db-shm", "loadline.db-journal",
	"ensure.stamp", "opened.stamp", "server.lock", "config.json", browserProfile,
}

const (
	// tokenTempPrefix starts the names of the files a token is written to
	// before it is linked into place.
	tokenTempPrefix = ".token-"
	// rotatedLogPrefix starts the names of the logs the server rotated out,
	// which end in their number.
	rotatedLogPrefix = "loadline.log."
)

// Owns reports whether name is an entry Loadline makes in the data
// directory.
func Owns(name string) bool {
	if slices.Contains(ownNames, name) || strings.HasPrefix(name, tokenTempPrefix) {
		return true
	}
	n, ok := strings.CutPrefix(name, rotatedLogPrefix)
	if !ok {
		return false
	}
	_, err := strconv.ParseUint(n, 10, 32)
	return err == nil
}

// tempNames are the names of the files Loadline writes before it puts them
// in place under their own: the token's (config), a spooled event's
// (forward) and the installed binary's (install), each a prefix and a
// suffix around what os.CreateTemp makes unique.
var tempNames = [...]struct{ prefix, suffix string }{
	{tokenTempPrefix, ""},
	{".put-", ".tmp"},
	{".loadline-", ".tmp"},
}

// Temporary reports whether name is that of a file Loadline writes before it
// puts it in place under its own name. Such a file belongs to the process
// writing it, which may be another Loadline process at work right now, and
// which renames, links or deletes it at any moment. The preparation and the
// check of the data directory change nothing of it, and judge it in full once
// it is in place, but look at its names first, and refuse one that is also a
// file elsewhere (judgeTemporary).
func Temporary(name string) bool {
	for _, temp := range tempNames {
		if strings.HasPrefix(name, temp.prefix) && strings.HasSuffix(name, temp.suffix) {
			return true
		}
	}
	return false
}

// systemNames are the files Windows Explorer and the macOS Finder leave in a
// folder its user has looked at, which do not make it anyone else's.
var systemNames = []string{".DS_Store", "desktop.ini", "Thumbs.db"}

// systemFile reports whether entry is a file the system leaves. A folder or
// a link by one of their names is someone else's: no system makes one.
func systemFile(entry fs.DirEntry) bool {
	return slices.Contains(systemNames, entry.Name()) && entry.Type().IsRegular()
}

// foreignEntry returns the name of the first entry of dir that is neither
// Loadline's nor a file the system leaves, or "" when there is none or dir
// does not exist.
func foreignEntry(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the data directory: %w", err)
	}
	for _, entry := range entries {
		if !Owns(entry.Name()) && !systemFile(entry) {
			return entry.Name(), nil
		}
	}
	return "", nil
}

// foreignProblem reports dir, which holds name, as one Loadline must not
// take: making it private would take other users' access to everything in
// it away.
func foreignProblem(dir, name string) string {
	return dir + " holds " + name + ", which is not Loadline's: LOADLINE_HOME must be a folder of Loadline's own, " +
		"since Loadline makes it, and all it holds, private; set LOADLINE_HOME to a new folder"
}

// CheckLink returns the error install and the server refuse path with when
// it is a link, or nil when it is not or does not exist. Writing through a
// link among Loadline's own files would put them, the binary every hook runs
// among them, wherever it points.
func CheckLink(path string) error {
	path = filepath.Clean(path)
	entry, err := inspect(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case entry.Link:
		return errors.New(linkProblem(path, entry))
	}
	return nil
}

// linkProblem reports path, one of Loadline's own files or folders, which
// Inspect read as entry, a link, in the words its kind calls for.
func linkProblem(path string, entry fsperm.Entry) string {
	const advice = "remove it, then run loadline install"
	switch entry.Reparse {
	case fsperm.CloudFile:
		return cloudFileProblem(path, entry.Tag)
	case fsperm.OtherReparse:
		return unknownReparseProblem(path, entry.Tag) + advice
	default:
		return path + " is a link, which Loadline's own files must never be: " + advice
	}
}

// unknownReparseProblem starts the report of path, a reparse point with tag
// of a kind Loadline does not know, whose advice the caller adds.
func unknownReparseProblem(path string, tag uint32) string {
	return fmt.Sprintf("%s is a reparse point of a kind Loadline does not know (tag 0x%08X), which may lead somewhere else: ", path, tag)
}

// cloudFileProblem reports path, a reparse point with tag that a cloud
// service keeps in sync, though Windows did not say which service, or which
// folder of its holds path.
func cloudFileProblem(path string, tag uint32) string {
	return fmt.Sprintf("%s is kept in sync with the cloud (tag 0x%08X), so the token and the binary every hook runs would be copied there: "+
		`set LOADLINE_HOME to a folder no cloud service keeps in sync, such as the default %%LOCALAPPDATA%%\loadline`, path, tag)
}

func moreNamesProblem(path string) string {
	return path + " has another name, a hard link, which Loadline's own files must never have, since making it " +
		"private would change the file under its other name too: remove it, then run loadline install"
}

// foreignOwner is fsperm.ForeignOwner. Tests replace it: an account that is
// not elevated, or not root, cannot give a file another owner.
var foreignOwner = fsperm.ForeignOwner

// inspect is fsperm.Inspect. Tests replace it: no volume at hand gives two
// files IDs that differ only above their low 64 bits, as ReFS can, and no
// reparse point a test can make, but a link, lets the system open the path
// through it, as one its filter handles does.
var inspect = fsperm.Inspect

// betweenLookAndCount runs in each attempt of judgeTemporary, between its
// look at a temporary name and its count of that file's names in the
// folder. Tests replace it: nothing else can change a file's names at that
// moment.
var betweenLookAndCount = func(string) {}

// tree is what a walk of Loadline's paths in the data directory found.
type tree struct {
	// paths are Loadline's files and folders, each folder before what it
	// holds.
	paths []string
	// link is the first link among them, or the first file with another
	// name when moreNames is set, where the walk stopped, or "". linked is
	// what the walk read of it.
	link      string
	linked    fsperm.Entry
	moreNames bool
	// locked is the first folder among them that the user may not list, or
	// "": what it holds is not in paths.
	locked string
	// profile is the dashboard's browser profile when the walk found it, or
	// "". It is among paths, and what it holds is not (browserProfile).
	profile string
	// unjudged reports a temporary name the walk could not judge
	// (goneOrGoing), which may be a file elsewhere too: no list may be passed
	// down to it.
	unjudged bool
}

// linkProblem reports what the walk stopped at, or "" when it went through.
func (t tree) linkProblem() string {
	switch {
	case t.link == "":
		return ""
	case t.moreNames:
		return moreNamesProblem(t.link)
	}
	return linkProblem(t.link, t.linked)
}

// ownPaths walks what Loadline keeps in dir: each entry it owns, the files
// the system leaves there, which are made private with it, and everything
// below the folders among them, except the regular files that are still
// being written (Temporary), which it judges by their names alone
// (judgeTemporary) and leaves to their writers, and what the dashboard's
// browser profile holds, which the browser keeps (browserProfile). It
// follows no link: it stops at the first, and at the first file with a
// name elsewhere. A dir that does not exist holds nothing, and an entry that
// goes while it looks, as a spooled event does once drained, is left out.
//
// fix, when it is set, may change each path as the walk comes to it and,
// for a folder, once the walk is through it (repair), but never the browser
// profile, and never a folder that holds a temporary name the walk could not
// judge. Without it, a folder the user may not list is noted in locked, not
// an error.
func ownPaths(dir string, fix *repair) (tree, error) {
	var found tree
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return found, nil
	}
	if err != nil {
		return found, fmt.Errorf("read the data directory: %w", err)
	}
	for _, entry := range entries {
		if !Owns(entry.Name()) && !systemFile(entry) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if entry.Name() == browserProfile {
			err = found.judgeProfile(path)
		} else {
			err = found.walk(path, entry.Type(), fix)
		}
		if err != nil || found.link != "" {
			return found, err
		}
	}
	return found, nil
}

// repair is what Make does to Loadline's paths as it walks them.
type repair struct {
	// before runs on each path once it is known to be no link, before the
	// walk looks into it, so that a folder whose list keeps the user out
	// can be listed.
	before func(path string, folder bool) error
	// after runs on each folder once the walk has been through what it
	// holds and found nothing there to refuse.
	after func(path string) error
}

// judgeNames tells what path itself is, and returns errMoreNames when it is
// a file with a name besides its own and its own temporary ones (leftovers).
// A second name puts the file wherever that name is too, and whatever is
// done to it here is done to it there. A temporary name beside it in its own
// folder is no such name: the token is written under one and then linked
// into place, and that name stays when its writer is ended before it
// removes it, or cannot remove it while a scanner holds the file. When tidy
// is set, judgeNames tries to remove those leftovers, as far as it can for
// a moment, and judges the file again.
func judgeNames(path string, tidy bool) (fsperm.Entry, error) {
	entry, err := inspect(path)
	if err != nil || entry.Link || entry.Folder || entry.Names == 1 {
		return entry, err
	}
	own, err := leftovers(path, entry.ID)
	if err != nil {
		return entry, err
	}
	if tidy && len(own) > 0 {
		for _, name := range own {
			_ = settleWithin(leftoverBudget, func() error { return os.Remove(name) })
		}
		return judgeNames(path, false)
	}
	if entry.Names > uint64(len(own))+1 {
		return entry, errMoreNames
	}
	return entry, nil
}

// leftovers returns the temporary names in path's folder (Temporary) that
// are names of the file id tells of, path's own.
func leftovers(path string, id fsperm.FileID) ([]string, error) {
	return namesIn(filepath.Dir(path), id, Temporary)
}

// namesIn returns the names in dir of the file id tells of, among those of
// its regular files that pass keep.
func namesIn(dir string, id fsperm.FileID, keep func(name string) bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if !keep(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		name := filepath.Join(dir, entry.Name())
		// One that cannot be looked at, as while its writer deletes it, is
		// not counted: the walk then waits for it to go (settle).
		if other, err := inspect(name); err == nil && other.ID == id {
			names = append(names, name)
		}
	}
	return names, nil
}

// anyName keeps every name in namesIn.
func anyName(string) bool { return true }

// temporaryVerdict is what judgeTemporary makes of a temporary name.
type temporaryVerdict uint8

const (
	// inPlace is a file with that name alone, or all of whose names are in
	// its folder: the token while its writer links it into place, and after,
	// when the writer left the temporary name behind (judgeNames). It is its
	// writer's, and left alone.
	inPlace temporaryVerdict = iota + 1
	// linkedElsewhere is a file with a name in another folder, which every
	// look found for as long as the walk waits (settle). No writer makes
	// one, and whatever is done to the file here is done to it there.
	linkedElsewhere
	// goneOrGoing is a name whose file went, or was replaced by a folder,
	// since its folder was listed, or that Windows would not let the walk
	// look at for as long as it waits, as while the file's deletion is
	// pending. What its file is, and where else it is, goes unjudged.
	goneOrGoing
	// aLink is a link, which no writer makes.
	aLink
)

// judgeTemporary tells what is under path, a temporary name that its
// folder's listing gave as a regular file, and changes nothing. A look at
// the file and a count of its names in the folder make one attempt. A count
// short of the names the look found is no verdict yet, since the file's
// writer may have removed one of them meanwhile: the attempt is made again
// (settle), and only a shortfall that lasts is a name elsewhere. A healthy
// file, whose count matches at once, or which has one name, is never waited
// for.
func judgeTemporary(path string) (fsperm.Entry, temporaryVerdict, error) {
	var entry fsperm.Entry
	err := settle(func() (err error) {
		entry, err = inspect(path)
		if err != nil || entry.Link || entry.Folder || entry.Names == 1 {
			return err
		}
		betweenLookAndCount(path)
		names, err := namesIn(filepath.Dir(path), entry.ID, anyName)
		switch {
		case err != nil:
			return err
		case uint64(len(names)) < entry.Names:
			return errMoreNames
		}
		return nil
	})
	switch {
	case errors.Is(err, fs.ErrNotExist), fsperm.Transient(err):
		return entry, goneOrGoing, nil
	case errors.Is(err, errMoreNames):
		return entry, linkedElsewhere, nil
	case err != nil:
		return entry, 0, err
	case entry.Link:
		return entry, aLink, nil
	case entry.Folder:
		return entry, goneOrGoing, nil
	}
	return entry, inPlace, nil
}

// judge tells what path itself is, and adds it to the paths when it is no
// link and has no name elsewhere (judgeNames, which tidies when tidy is
// set). judged is false when the walk is to go no further with path: it is
// gone, or it is what the walk stopped at.
func (t *tree) judge(path string, tidy bool) (entry fsperm.Entry, judged bool, err error) {
	// What the path itself is, read even when its own list keeps the user
	// out, as one in a folder that was just made listable may.
	err = settle(func() (err error) {
		entry, err = judgeNames(path, tidy)
		return err
	})
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return entry, false, nil
	case errors.Is(err, errMoreNames):
		t.link, t.moreNames = path, true
		return entry, false, nil
	case err != nil:
		return entry, false, err
	case entry.Link:
		t.link, t.linked = path, entry
		return entry, false, nil
	}
	t.paths = append(t.paths, path)
	return entry, true, nil
}

// judgeProfile adds path, the dashboard's browser profile, judged itself as
// any of Loadline's folders is, and neither changes it nor goes below it
// (browserProfile).
func (t *tree) judgeProfile(path string) error {
	_, judged, err := t.judge(path, false)
	if judged {
		t.profile = path
	}
	return err
}

// judgeTemporaryName judges path, a regular file that is still being
// written (Temporary), by its names alone (judgeTemporary), and notes what
// the walk is to refuse, or could not judge. It adds no path: the file is
// its writer's, and nothing changes it.
func (t *tree) judgeTemporaryName(path string) error {
	entry, verdict, err := judgeTemporary(path)
	if err != nil {
		return err
	}
	switch verdict {
	case aLink:
		t.link, t.linked = path, entry
	case linkedElsewhere:
		t.link, t.moreNames = path, true
	case goneOrGoing:
		t.unjudged = true
	case inPlace:
	}
	return nil
}

// walk adds path, whose type its folder's listing gave as typ, and when it
// is a folder, everything below it, stopping at the first link or file with
// a name elsewhere, and leaving the regular files that are still being
// written to their writers once it has judged their names.
func (t *tree) walk(path string, typ fs.FileMode, fix *repair) error {
	if Temporary(filepath.Base(path)) && typ.IsRegular() {
		return t.judgeTemporaryName(path)
	}
	entry, judged, err := t.judge(path, fix != nil)
	if err != nil || !judged {
		return err
	}
	if fix != nil {
		if err := fix.before(path, entry.Folder); err != nil {
			return err
		}
	}
	if !entry.Folder {
		return nil
	}
	entries, err := os.ReadDir(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, fs.ErrPermission) && fix == nil:
		if t.locked == "" {
			t.locked = path
		}
		return nil
	case err != nil:
		return fmt.Errorf("read %s: %w", path, err)
	}
	// The folder's list, once put right, reaches what the folder holds, as
	// the data directory's does (Make): a name below that went unjudged
	// keeps it as it is, private with the list of its own it was given to be
	// listed, as a refused run leaves it. Later runs find it private, and
	// leave it so.
	unjudgedBefore := t.unjudged
	t.unjudged = false
	defer func() { t.unjudged = t.unjudged || unjudgedBefore }()
	for _, entry := range entries {
		if err := t.walk(filepath.Join(path, entry.Name()), entry.Type(), fix); err != nil || t.link != "" {
			return err
		}
	}
	if fix == nil || t.unjudged {
		return nil
	}
	return fix.after(path)
}
