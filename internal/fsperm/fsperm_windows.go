package fsperm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// privateDescriptor is the security descriptor of a private directory: a
// protected access list, so that nothing the folder above passes down
// applies, that gives full control to the current user, SYSTEM and
// Administrators, and to nobody else, and that every file and directory
// created below inherits.
func privateDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("identify the current user: %w", err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return nil, fmt.Errorf("build a private access list: %w", err)
	}
	return sd, nil
}

// Private gives dir the access list of privateDescriptor, unless it has that
// list already. Windows applies a new list to everything dir holds,
// replacing what each of those inherited; access a file was given explicitly
// stays. Left as it is, a dir that is private already passes nothing again
// to the files that other Loadline processes may be writing in it.
func Private(dir string) error {
	sd, err := privateDescriptor()
	if err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	private, err := hasList(dir, dacl)
	if err == nil && !private {
		err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	}
	runtime.KeepAlive(sd)
	if err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	return nil
}

// hasList reports whether path has a protected access list that holds the
// entries of want, in the same order, and nothing else.
func hasList(path string, want *windows.ACL) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	defer runtime.KeepAlive(sd)
	return isList(sd, want)
}

// isList reports whether sd has a protected access list that holds the
// entries of want, in the same order, and nothing else.
func isList(sd *windows.SECURITY_DESCRIPTOR, want *windows.ACL) (bool, error) {
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false, err
	}
	have, _, err := sd.DACL()
	switch {
	case errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND), err == nil && have == nil:
		return false, nil
	case err != nil:
		return false, err
	case have.AceCount != want.AceCount:
		return false, nil
	}
	for i := range uint32(want.AceCount) {
		var haveACE, wantACE *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(have, i, &haveACE); err != nil {
			return false, err
		}
		if err := windows.GetAce(want, i, &wantACE); err != nil {
			return false, err
		}
		if !sameEntry(haveACE, wantACE) {
			return false, nil
		}
	}
	return true, nil
}

// fileAllAccess is FILE_ALL_ACCESS, what the private list gives each of the
// user, SYSTEM and Administrators.
const fileAllAccess windows.ACCESS_MASK = 0x1f01ff

// EntryPrivate reports whether path, a file or folder in a private folder,
// has exactly what that folder passes down: the three entries giving full
// control to the user, SYSTEM and Administrators, each inherited, and for a
// folder, each passed on to what it holds. A folder may instead have the
// private list itself, protected, as each folder Loadline makes has. Any
// other list is not private: one with an entry given to path explicitly, a
// denial among them, one protected from what the folder passes down, one
// that brought its entries from somewhere else, or one that lacks an entry,
// which keeps the user out of a file of their own.
func EntryPrivate(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("read the access list of %s: %w", path, err)
	}
	defer runtime.KeepAlive(sd)
	private, err := isPrivate(sd, info.IsDir())
	if err != nil {
		return false, fmt.Errorf("read the access list of %s: %w", path, err)
	}
	return private, nil
}

func isPrivate(sd *windows.SECURITY_DESCRIPTOR, folder bool) (bool, error) {
	control, _, err := sd.Control()
	if err != nil {
		return false, err
	}
	if control&windows.SE_DACL_PROTECTED != 0 {
		if !folder {
			return false, nil
		}
		private, err := privateDescriptor()
		if err != nil {
			return false, err
		}
		defer runtime.KeepAlive(private)
		want, _, err := private.DACL()
		if err != nil {
			return false, err
		}
		return isList(sd, want)
	}
	dacl, _, err := sd.DACL()
	switch {
	case errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND), err == nil && dacl == nil:
		return false, nil
	case err != nil:
		return false, err
	}
	trusted, err := trustedSIDs()
	if err != nil {
		return false, err
	}
	if int(dacl.AceCount) != len(trusted) {
		return false, nil
	}
	flags := uint8(windows.INHERITED_ACE)
	if folder {
		flags |= windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	seen := make([]bool, len(trusted))
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, err
		}
		if ace.Header.AceFlags != flags || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != fileAllAccess {
			return false, nil
		}
		// Each of the three once: with as many entries as there are of
		// them, none is then missing.
		k := slices.IndexFunc(trusted, entrySID(ace).Equals)
		if k < 0 || seen[k] {
			return false, nil
		}
		seen[k] = true
	}
	return true, nil
}

// sameEntry reports whether have is the entry want, an allow entry, is: of
// its kind, for its trustee, with its rights, passed down as it is.
func sameEntry(have, want *windows.ACCESS_ALLOWED_ACE) bool {
	return have.Header.AceType == want.Header.AceType && have.Header.AceFlags == want.Header.AceFlags &&
		have.Mask == want.Mask && entrySID(have).Equals(entrySID(want))
}

// entrySID returns the trustee of ace, an allow entry, or any other laid out
// as one up to its SID.
func entrySID(ace *windows.ACCESS_ALLOWED_ACE) *windows.SID {
	return (*windows.SID)(unsafe.Pointer(&ace.SidStart)) //nolint:gosec // G103: an allow entry's SID starts at SidStart
}

// MkdirAll creates dir and every missing folder above it, each with the
// access list of privateDescriptor from the moment it exists, so that nobody
// else can put anything in it first. A folder that already exists is left as
// it is.
func MkdirAll(dir string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	switch {
	case err == nil && info.IsDir():
		return nil
	case err == nil:
		return fmt.Errorf("create %s: %w", dir, syscall.ENOTDIR)
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if parent := filepath.Dir(dir); parent != dir {
		if err := MkdirAll(parent); err != nil {
			return err
		}
	}

	sd, err := privateDescriptor()
	if err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	attrs := windows.SecurityAttributes{SecurityDescriptor: sd}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	err = windows.CreateDirectory(name, &attrs)
	runtime.KeepAlive(sd)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		// Created a moment ago by another Loadline process, or by someone
		// else: either way it exists now and is judged as any existing
		// folder is.
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return nil
}

// ClearExplicit removes the access entries path was given explicitly, so
// that only what the folder holding it passes down applies to it, and lets
// that folder's list flow in again if path had a list of its own. In a
// private folder that leaves path to the user, SYSTEM and Administrators.
func ClearExplicit(path string) error {
	// "D:" is an empty access list, not a missing one, which would let
	// everyone in.
	sd, err := windows.SecurityDescriptorFromString("D:")
	if err != nil {
		return fmt.Errorf("clear the access list of %s: %w", path, err)
	}
	empty, _, err := sd.DACL()
	if err != nil || empty == nil {
		return fmt.Errorf("clear the access list of %s: an empty list: %w", path, errors.Join(err, windows.ERROR_INVALID_ACL))
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, empty, nil)
	runtime.KeepAlive(sd)
	if err != nil {
		return fmt.Errorf("clear the access list of %s: %w", path, err)
	}
	return nil
}

// IsLink reports whether path itself is a symbolic link, a junction or any
// other reparse point, which opening it would follow somewhere else.
func IsLink(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if attrs, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
	}
	return info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0, nil
}

// Transient reports whether err may be a refusal that passes in a moment.
// Windows refuses to open a file whose deletion is pending, until the last
// handle on it closes, with the same "access denied" as a file the user may
// not open, and one that another process holds without sharing it with a
// sharing violation. err does not tell which it is, so a caller tries again
// for a short while and then takes err as it is.
func Transient(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

// EntryExposure is Exposure for a file or folder inside a private folder:
// on Windows an entry can carry access of its own besides what the folder
// passes down.
func EntryExposure(path string) (string, error) {
	return Exposure(path)
}

// Exposure says who, besides the current user, SYSTEM and Administrators,
// the access list of path lets in, naming each trustee an allow entry grants
// anything to, on path or on what it holds. It returns "" when the list
// admits nobody else.
func Exposure(path string) (string, error) {
	others, noList, err := othersAllowed(path, func(windows.ACCESS_MASK, uint8) bool { return true })
	switch {
	case err != nil:
		return "", err
	case noList:
		return "it has no access list, which admits everyone", nil
	case len(others) == 0:
		return "", nil
	}
	return "its access list also admits " + andList(others), nil
}

// fileDeleteChild is FILE_DELETE_CHILD: the right to delete, and so to
// rename, what a folder holds, whatever their own access lists say.
const fileDeleteChild = 0x40

// movable says who besides the user, SYSTEM and Administrators may delete
// or rename folder itself, or "" when nobody may.
func movable(folder string) (string, error) {
	others, err := othersGranted(folder, windows.DELETE)
	if err != nil || len(others) == 0 {
		return "", err
	}
	return folder + " lets " + andList(others) + " delete or rename it", nil
}

// emptiable says who besides the user, SYSTEM and Administrators may delete
// or rename the folders that folder holds, or change its permissions or
// owner, with which they may give themselves the right to delete or rename
// it, or what it holds, or "" when nobody may.
func emptiable(folder string) (string, error) {
	others, err := othersGranted(folder, fileDeleteChild)
	if err != nil {
		return "", err
	}
	if len(others) > 0 {
		return folder + " lets " + andList(others) + " delete or rename the folders in it", nil
	}
	others, err = othersGranted(folder, windows.WRITE_DAC|windows.WRITE_OWNER)
	if err != nil || len(others) == 0 {
		return "", err
	}
	return folder + " lets " + andList(others) + " change its permissions or owner, and with that delete or rename it, or what it holds", nil
}

// ForeignOwner names the owner of path when that is anyone but the user,
// SYSTEM, Administrators or TrustedInstaller, which owns the system's own
// folders, or returns "" when it is one of them. The owner may always
// change who can use path, whatever its access list says.
func ForeignOwner(path string) (string, error) {
	owner, err := ownerOf(path)
	if err != nil {
		return "", err
	}
	trusted, err := trustedSIDs()
	if err != nil {
		return "", err
	}
	trustedInstaller, err := windows.StringToSid(trustedInstallerSID)
	if err != nil {
		return "", fmt.Errorf("identify TrustedInstaller: %w", err)
	}
	if slices.ContainsFunc(append(trusted, trustedInstaller), owner.Equals) {
		return "", nil
	}
	return accountName(owner), nil
}

// othersGranted names the trustees besides the user, SYSTEM and
// Administrators that the access list of path gives any of mask on path
// itself. An entry that only what path holds inherits gives path nothing,
// and generic rights count as what they stand for on a file.
func othersGranted(path string, mask windows.ACCESS_MASK) ([]string, error) {
	others, noList, err := othersAllowed(path, func(rights windows.ACCESS_MASK, flags uint8) bool {
		return flags&windows.INHERIT_ONLY_ACE == 0 && fileRights(rights)&mask != 0
	})
	if noList {
		return []string{"Everyone"}, err
	}
	return others, err
}

// othersAllowed names, once each, the trustees besides the user, SYSTEM and
// Administrators that an allow entry of the access list of path admits, when
// counts says the entry's rights and flags count. noList reports that path
// has no access list at all, which lets everyone do anything.
func othersAllowed(path string, counts func(rights windows.ACCESS_MASK, flags uint8) bool) (others []string, noList bool, err error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, false, fmt.Errorf("read the access list of %s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	switch {
	case errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND), err == nil && dacl == nil:
		return nil, true, nil
	case err != nil:
		return nil, false, fmt.Errorf("read the access list of %s: %w", path, err)
	}
	trusted, err := trustedSIDs()
	if err != nil {
		return nil, false, err
	}

	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, false, fmt.Errorf("read the access list of %s: %w", path, err)
		}
		if ace.Mask == 0 || !counts(ace.Mask, ace.Header.AceFlags) {
			continue
		}
		for _, sid := range allowed(ace) {
			// CREATOR OWNER on a folder only the user may write to lets in
			// the user, who alone creates anything below it.
			if sid != nil && (slices.ContainsFunc(trusted, sid.Equals) || sid.IsWellKnown(windows.WinCreatorOwnerSid)) {
				continue
			}
			if name := accountName(sid); !slices.Contains(others, name) {
				others = append(others, name)
			}
		}
	}
	runtime.KeepAlive(sd)
	return others, false, nil
}

// The kinds of allow entry besides ACCESS_ALLOWED_ACE_TYPE, each of which
// lets its trustee in as a plain one does.
const (
	accessAllowedCompoundACEType       = 4
	accessAllowedObjectACEType         = 5
	accessAllowedCallbackACEType       = 9
	accessAllowedCallbackObjectACEType = 11
)

// objectGUIDSize is the size of each GUID an object entry may hold before
// its SID.
const objectGUIDSize = 16

// allowed returns the trustees an allow entry of any kind lets in, read
// from where its kind keeps them, or none for an entry that only takes
// access away. A compound entry lets its client in through its server, and
// both are returned.
//
// CREATOR OWNER and CREATOR GROUP are among what it returns: each matches
// nobody on the folder that holds the entry, but stands for the creator's
// account, or its primary group, on every file created below.
func allowed(ace *windows.ACCESS_ALLOWED_ACE) []*windows.SID {
	switch ace.Header.AceType {
	case windows.ACCESS_ALLOWED_ACE_TYPE, accessAllowedCallbackACEType:
		return sidsAt(ace, unsafe.Offsetof(ace.SidStart), 1)
	case accessAllowedCompoundACEType:
		// The mask, then a word for the compound kind and a reserved one.
		return sidsAt(ace, unsafe.Offsetof(ace.SidStart)+4, 2)
	case accessAllowedObjectACEType, accessAllowedCallbackObjectACEType:
		// The mask, then flags that say which of the two GUIDs follow.
		flagsAt := unsafe.Offsetof(ace.SidStart)
		if uintptr(ace.Header.AceSize) < flagsAt+4 {
			return []*windows.SID{nil}
		}
		flags := *(*uint32)(unsafe.Add(unsafe.Pointer(ace), flagsAt)) //nolint:gosec // G103: bounded by AceSize above
		offset := flagsAt + 4
		if flags&windows.ACE_OBJECT_TYPE_PRESENT != 0 {
			offset += objectGUIDSize
		}
		if flags&windows.ACE_INHERITED_OBJECT_TYPE_PRESENT != 0 {
			offset += objectGUIDSize
		}
		return sidsAt(ace, offset, 1)
	default:
		return nil
	}
}

// sidsAt returns the n SIDs that follow each other in ace from offset. The
// first that does not fit within the entry ends them as nil, which
// accountName names as an entry it cannot read: Windows stores no such
// entry, and none is passed over.
func sidsAt(ace *windows.ACCESS_ALLOWED_ACE, offset uintptr, n int) []*windows.SID {
	size := uintptr(ace.Header.AceSize)
	sids := make([]*windows.SID, 0, n)
	for range n {
		// A SID is 8 bytes, the last 6 its authority, then 4 for each of
		// its sub-authorities, whose count is its second byte.
		if offset+8 > size {
			return append(sids, nil)
		}
		start := unsafe.Add(unsafe.Pointer(ace), offset) //nolint:gosec // G103: bounded by AceSize above
		length := 8 + 4*uintptr(*(*byte)(unsafe.Add(start, 1)))
		if offset+length > size {
			return append(sids, nil)
		}
		sids = append(sids, (*windows.SID)(start))
		offset += length
	}
	return sids
}

// fileRights turns the generic rights in mask into the file rights they
// stand for.
func fileRights(mask windows.ACCESS_MASK) windows.ACCESS_MASK {
	specific := mask &^ (windows.GENERIC_ALL | windows.GENERIC_READ | windows.GENERIC_WRITE | windows.GENERIC_EXECUTE)
	if mask&windows.GENERIC_ALL != 0 {
		specific |= windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	}
	if mask&windows.GENERIC_READ != 0 {
		specific |= windows.FILE_GENERIC_READ
	}
	if mask&windows.GENERIC_WRITE != 0 {
		specific |= windows.FILE_GENERIC_WRITE
	}
	if mask&windows.GENERIC_EXECUTE != 0 {
		specific |= windows.FILE_GENERIC_EXECUTE
	}
	return specific
}

// samePath reports whether a and b name the same folder, as Windows compares
// names: without regard to case.
func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// finalPathFlags are FILE_NAME_NORMALIZED and VOLUME_NAME_DOS, both zero:
// the path with every link and subst drive resolved, on its drive letter.
const finalPathFlags = 0

// finalPath returns the path the system opens for path, each junction,
// symbolic link and subst drive in it resolved. Tests replace it: no file
// system at hand fails to say.
var finalPath = func(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	// No access is asked for: the handle only names the folder.
	handle, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], uint32(len(buf)), finalPathFlags)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", path, err)
		}
		if int(n) < len(buf) {
			return dosPath(windows.UTF16ToString(buf[:n])), nil
		}
		// Too small: n is the size it needs, its terminating null included.
		buf = make([]uint16, n)
	}
}

// dosPath takes the \\?\ prefix, which GetFinalPathNameByHandle puts on what
// it returns, off p, so that the folders it names compare and read as the
// written ones do.
func dosPath(p string) string {
	if share, ok := strings.CutPrefix(p, `\\?\UNC\`); ok {
		return `\\` + share
	}
	return strings.TrimPrefix(p, `\\?\`)
}

// trustedInstallerSID is NT SERVICE\TrustedInstaller, which owns the system's
// own folders, the volume root among them.
const trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

// ownerOf returns the owner of path. Tests replace it: an account that is
// not elevated cannot give a folder another owner.
var ownerOf = func(path string) (*windows.SID, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, fmt.Errorf("read the owner of %s: %w", path, err)
	}
	defer runtime.KeepAlive(sd)
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return nil, fmt.Errorf("read the owner of %s: %w", path, errors.Join(err, windows.ERROR_INVALID_OWNER))
	}
	return owner.Copy()
}

// trustedSIDs are the principals a private directory admits: the user, and
// the two that can take any file on the machine anyway.
func trustedSIDs() ([]*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("identify the current user: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("identify SYSTEM: %w", err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, fmt.Errorf("identify Administrators: %w", err)
	}
	return []*windows.SID{user.User.Sid, system, admins}, nil
}

// accountName is DOMAIN\name for sid, or the SID itself when no account
// answers to it, as for one deleted since or one of another machine. A nil
// sid is one sidsAt could not read.
func accountName(sid *windows.SID) string {
	if sid == nil {
		return "the trustee of an entry it cannot read"
	}
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	return domain + `\` + account
}
