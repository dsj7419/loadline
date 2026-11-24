//go:build !windows

package fsperm

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

// DirPrivate reports whether dir has mode 0700, and no other mode bit,
// which is what Private gives it.
func DirPrivate(dir string) (bool, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", dir, err)
	}
	return info.Mode()&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) == privateDirMode, nil
}

// PassDown is Private here: a mode passes nothing down.
func PassDown(dir string) error { return Private(dir) }

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

// finalPath returns path with every symbolic link in it followed. Tests
// replace it: no file system at hand fails to say.
var finalPath = func(path string) (string, error) {
	final, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	return final, nil
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

// ForeignOwner tells who owns path when that is anyone but the user or root,
// or returns the zero Owner when it is one of them. The owner may always
// change path's mode.
//
// It never says an account is gone. The user database it reads,
// /etc/passwd, does not hold the accounts of LDAP, SSSD or systemd-homed,
// so a user ID that it cannot name may be an account that signs in all the
// same.
func ForeignOwner(path string) (Owner, error) {
	uid, err := ownerOf(path)
	if err != nil {
		return Owner{}, err
	}
	if uid == 0 || int64(uid) == int64(os.Getuid()) {
		return Owner{}, nil
	}
	return Owner{Name: accountName(uid)}, nil
}

// OwnerName names the owner of path, whoever it is: unlike ForeignOwner, it
// names the user and root too.
func OwnerName(path string) (string, error) {
	uid, err := ownerOf(path)
	if err != nil {
		return "", err
	}
	return accountName(uid), nil
}

// accountName names the account with user ID uid, or gives its number when
// no account answers to it.
func accountName(uid uint32) string {
	id := strconv.FormatUint(uint64(uid), 10)
	if account, err := user.LookupId(id); err == nil {
		return account.Username
	}
	return "the account with user ID " + id
}

// CanChangeList reports whether this process may change the mode of dir: only
// its owner and root may.
func CanChangeList(dir string) (bool, error) {
	uid, err := ownerOf(dir)
	if err != nil {
		return false, err
	}
	euid := os.Geteuid()
	return euid == 0 || int64(uid) == int64(euid), nil
}

// RegainList lets this process list dir, a folder whose mode keeps its owner
// out, by giving the owner every right on it (mode|0700), which widens
// nobody else's access, and reports whether it changed the mode. A folder it
// can list already is left as it is. When dir still cannot be listed then,
// as when an access control list beside the mode denies it, RegainList puts
// the mode back and returns ErrStillLocked, as it does for a folder whose mode
// this process may not change (CanChangeList). Two processes that run it on
// dir at once, and both find it still kept out, may leave the owner's
// rights on it: the one that put back the mode it read after the other
// changed it. That lets in nobody else.
func RegainList(dir string) (bool, error) {
	listable, err := canList(dir)
	if err != nil || listable {
		return false, err
	}
	changeable, err := CanChangeList(dir)
	switch {
	case err != nil:
		return false, err
	case !changeable:
		return false, ErrStillLocked
	}
	info, err := os.Stat(dir)
	if err != nil {
		return false, err
	}
	mode := info.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
	if err := chmod(dir, mode|privateDirMode); err != nil {
		return false, fmt.Errorf("give the folder's owner every right on it: %w", err)
	}
	listable, err = canList(dir)
	if err == nil && listable {
		return true, nil
	}
	if restoreErr := chmod(dir, mode); restoreErr != nil {
		const kept = "put back the mode of %s, which keeps every right for its owner, "
		if err != nil {
			return true, fmt.Errorf(kept+"after listing it failed (%w): %w", dir, err, restoreErr)
		}
		return true, fmt.Errorf(kept+"though you still cannot list it: %w", dir, restoreErr)
	}
	if err != nil {
		return false, err
	}
	return false, ErrStillLocked
}

// ListableOnceRegained reports, changing nothing, whether dir, a folder this
// process cannot list, could be listed once RegainList gave its owner every
// right on it. It could unless the owner has every right already: then
// something besides the mode keeps them out, such as an access control list
// that macOS keeps beside it, or a security module, which RegainList cannot
// change.
func ListableOnceRegained(dir string) (bool, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return false, err
	}
	if info.Mode()&privateDirMode != privateDirMode {
		return true, nil
	}
	return Listable(dir)
}

// canList is Listable, which RegainList asks before and after it changes the
// mode, and chmod is os.Chmod, with which it changes it. Tests replace them:
// no folder at hand fails that second question with an error, or refuses
// its owner the mode it had a moment before.
var (
	canList = Listable
	chmod   = os.Chmod
)

// passable returns why this process may not pass through dir to what it
// holds, which takes the right to search it, or nil when it may: resolving
// "." in dir takes just that right.
func passable(dir string) error {
	_, err := os.Lstat(filepath.Clean(dir) + string(filepath.Separator) + ".")
	return err
}

// plainName matches an account name a shell reads as itself, which no option
// could be taken for.
var plainName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// ResetAdvice says what an administrator can do to dir, a folder that keeps
// the user out and whose mode Loadline cannot change: give it to the user,
// who may change its mode then. Only the folder itself changes owner:
// Loadline judges the owner of each of its files once it may list them, and
// another account's files are not the user's to take. It gives the command
// to paste, the path in single quotes, each single quote in it closing them,
// escaped with a backslash, and opening them again, unless the path holds
// what a shell, or one of another system an administrator may paste it
// into, reads as something else (PasteUnsafe), or a backslash, which fish
// reads as an escape even inside single quotes: it names the tool and what
// to change then. The user is named by account, or by number when the name
// is not a plain one.
func ResetAdvice(dir string) string {
	if PasteUnsafe(dir) || strings.Contains(dir, `\`) {
		return "give " + dir + " to you with chown"
	}
	id := strconv.Itoa(os.Geteuid())
	if account, err := user.LookupId(id); err == nil && plainName.MatchString(account.Username) {
		id = account.Username
	}
	return "give it to you with sudo chown " + id + " '" + strings.ReplaceAll(dir, "'", `'\''`) + "'"
}

// InProfile never says a path is in the user's profile here. It tells only
// where Loadline may offer to take back a folder that an account this
// computer no longer has owns, and only Windows tells that an account is
// gone.
func InProfile(string) (bool, error) { return false, nil }

// PrivateHere is Private here: a mode passes nothing down.
func PrivateHere(dir string) error { return Private(dir) }

// Inspect tells what path itself is, a link not followed.
func Inspect(path string) (Entry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Entry{}, fmt.Errorf("read %s: %w", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Entry{}, fmt.Errorf("read %s: the system gives no count of its names", path)
	}
	link, reparse := info.Mode()&fs.ModeSymlink != 0, NotReparse
	if link {
		reparse = NameSurrogate
	}
	return Entry{
		Link:    link,
		Reparse: reparse,
		Folder:  info.IsDir(),
		Names:   uint64(stat.Nlink),                      //nolint:unconvert,nolintlint // Nlink is uint16 on macOS and uint64 on Linux
		ID:      indexFileID(uint64(stat.Dev), stat.Ino), //nolint:gosec,unconvert,nolintlint // Dev is int32 on macOS and uint64 on Linux; a device number is never negative
	}, nil
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

// IsNameSurrogate reports whether path itself is a symbolic link, the only
// link here, which stands for another path.
func IsNameSurrogate(path string) (bool, error) { return IsLink(path) }

// SyncRoot never says path is in a folder that a cloud service keeps in
// sync: only Windows tells which folders those are.
func SyncRoot(string) (root, provider string, under bool, err error) { return "", "", false, nil }

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
