package fsperm

import (
	"encoding/binary"
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
	user, err := currentUser()
	if err != nil {
		return nil, err
	}
	// The SID in its S-1-… form, which the format reads back as the SID it
	// is, whatever alias it may write it by.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return nil, fmt.Errorf("build a private access list: %w", err)
	}
	return sd, nil
}

// Private gives dir the access list of privateDescriptor and passes it down
// (PassDown), unless it has that list already (DirPrivate). Left as it is, a
// dir that is private already passes nothing again to the files that other
// Loadline processes may be writing in it.
func Private(dir string) error {
	private, err := DirPrivate(dir)
	if err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	if private {
		return nil
	}
	return PassDown(dir)
}

// DirPrivate reports whether dir has the access list Private gives it:
// protected, so that nothing the folder above passes down applies, and
// holding the entries of privateDescriptor and nothing else.
func DirPrivate(dir string) (bool, error) {
	sd, err := privateDescriptor()
	if err != nil {
		return false, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return false, err
	}
	private, err := hasList(dir, dacl)
	runtime.KeepAlive(sd)
	if err != nil {
		return false, fmt.Errorf("read the access list of %s: %w", dir, err)
	}
	return private, nil
}

// PassDown gives dir the access list of privateDescriptor, whatever it has,
// and Windows applies it to everything dir holds, replacing what each of
// those inherited; access a file was given explicitly stays.
func PassDown(dir string) error {
	sd, err := privateDescriptor()
	if err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	dacl, _, err := sd.DACL()
	if err == nil {
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
	// Each of the three once: Windows keeps two entries for one trustee when
	// one has unused bytes after its SID, and three entries can then lack
	// one of the three.
	seen := make([]bool, len(trusted))
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, err
		}
		if ace.Header.AceFlags != flags || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != fileAllAccess {
			return false, nil
		}
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

// IsLink reports whether path itself is a link (Entry.Link): a symbolic
// link, a junction or any other reparse point but a deduplicated file.
func IsLink(path string) (bool, error) {
	entry, err := Inspect(path)
	return entry.Link, err
}

// IsNameSurrogate reports whether path itself is a link that stands for
// another path, such as a symbolic link or a junction, which can be followed
// to say where it leads. A cloud file, a deduplicated file and a reparse
// point of a kind Loadline does not know are none.
func IsNameSurrogate(path string) (bool, error) {
	entry, err := Inspect(path)
	return entry.Reparse == NameSurrogate, err
}

// The reparse tags, and parts of them, that Loadline tells apart (winnt.h).
const (
	// reparseTagNameSurrogate is set in every tag whose reparse point
	// stands for another name (IsReparseTagNameSurrogate), whoever's tag
	// it is.
	reparseTagNameSurrogate = 0x20000000
	// reparseTagCloud is IO_REPARSE_TAG_CLOUD. IO_REPARSE_TAG_CLOUD_1 to
	// _F are the same tag with a number in the bits of
	// reparseTagCloudNumber.
	reparseTagCloud       = 0x9000001A
	reparseTagCloudNumber = 0xF000
	// reparseTagOneDrive is IO_REPARSE_TAG_ONEDRIVE, which OneDrive gave its
	// files before it kept them through the cloud filter.
	reparseTagOneDrive = 0x80000021
	// reparseTagDedup is IO_REPARSE_TAG_DEDUP.
	reparseTagDedup = 0x80000013
)

// reparseKind tells the kind of a reparse point from its tag.
func reparseKind(tag uint32) ReparseKind {
	switch {
	case tag&reparseTagNameSurrogate != 0:
		return NameSurrogate
	case tag&^reparseTagCloudNumber == reparseTagCloud, tag == reparseTagOneDrive:
		return CloudFile
	case tag == reparseTagDedup:
		return Deduplicated
	}
	return OtherReparse
}

// Inspect tells what path itself is, a link not followed. It reads the
// path's attributes alone, which the folder that holds it lets its lister
// read, so that a file or a link whose own list keeps the user out is read
// too.
func Inspect(path string) (Entry, error) {
	handle, err := openItself(path, windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return Entry{}, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return Entry{}, fmt.Errorf("read %s: %w", path, err)
	}
	id, err := readFileID(handle)
	if err != nil {
		return Entry{}, fmt.Errorf("read %s: %w", path, err)
	}
	reparse, tag, err := reparseOf(handle, info.FileAttributes)
	if err != nil {
		return Entry{}, fmt.Errorf("read %s: %w", path, err)
	}
	return Entry{
		Link:    reparse != NotReparse && reparse != Deduplicated,
		Reparse: reparse,
		Tag:     tag,
		Folder:  info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0,
		Names:   uint64(info.NumberOfLinks),
		ID:      id,
	}, nil
}

// fileAttributeTagInfo is FILE_ATTRIBUTE_TAG_INFO, what
// GetFileInformationByHandleEx gives for FileAttributeTagInfo.
type fileAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

// reparseOf tells what kind of reparse point the file handle is open on is,
// and its tag, from attributes, the file's attributes read on that handle.
// The tag is read only from a reparse point: a file system that has none
// need not answer for it. Deduplication makes files alone reparse points,
// so a folder with its tag is of a kind Loadline does not know.
func reparseOf(handle windows.Handle, attributes uint32) (ReparseKind, uint32, error) {
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		return NotReparse, 0, nil
	}
	var info fileAttributeTagInfo
	//nolint:gosec // G103: the call fills exactly the FILE_ATTRIBUTE_TAG_INFO it is given, of the size it is told
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return NotReparse, 0, err
	}
	kind := reparseKind(info.ReparseTag)
	if kind == Deduplicated && attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		kind = OtherReparse
	}
	return kind, info.ReparseTag, nil
}

// procCfGetSyncRootInfoByPath is the call of the Cloud Files API that tells
// of the sync root that holds a path. Tests replace it: no Windows at hand
// lacks it.
var procCfGetSyncRootInfoByPath = windows.NewLazySystemDLL("cldapi.dll").NewProc("CfGetSyncRootInfoByPath")

// The classes of what CfGetSyncRootInfoByPath tells of a sync root
// (CF_SYNC_ROOT_INFO_CLASS).
const (
	// syncRootInfoBasic is CF_SYNC_ROOT_INFO_BASIC: the root's 64-bit file
	// ID.
	syncRootInfoBasic = 0
	// syncRootInfoProvider is CF_SYNC_ROOT_INFO_PROVIDER: the service that
	// keeps the root in sync (syncRootProviderInfo).
	syncRootInfoProvider = 2
)

// notUnderSyncRoot is the HRESULT of ERROR_CLOUD_FILE_NOT_UNDER_SYNC_ROOT:
// CfGetSyncRootInfoByPath's answer for a path in no sync root, on a volume
// the cloud filter serves.
const notUnderSyncRoot = 0x80070000 | uint32(windows.ERROR_CLOUD_FILE_NOT_UNDER_SYNC_ROOT)

// syncRootProviderInfo is CF_SYNC_ROOT_PROVIDER_INFO (cfapi.h). It holds no
// pointer, so it is laid out the same on every Windows architecture.
type syncRootProviderInfo struct {
	ProviderStatus  uint32
	ProviderName    [256]uint16
	ProviderVersion [256]uint16
}

// SyncRoot asks Windows whether path is in a folder that a cloud service,
// such as OneDrive, keeps in sync, a sync root, and if so returns the name of
// the service and that folder, or "" for the folder when it cannot be found
// among those that hold path. It asks about path, or when path does
// not exist yet, about the nearest folder above it that does, in which it
// would be made, and finds that folder without following a link. Windows
// answers for where that folder really is, each link on the way followed,
// which is where whatever is made there goes.
//
// under is true only when Windows says so. When it cannot answer, err says
// why: a file system that the cloud filter does not serve, such as exFAT or
// a network share, answers with an error, as does a Windows without the
// Cloud Files API. A path in no sync root is not under one, with no error.
func SyncRoot(path string) (root, provider string, under bool, err error) {
	existing, err := nearestExisting(path)
	if err == nil {
		err = procCfGetSyncRootInfoByPath.Find()
	}
	var name *uint16
	if err == nil {
		name, err = windows.UTF16PtrFromString(existing)
	}
	if err != nil {
		return "", "", false, fmt.Errorf("ask whether %s is kept in sync with the cloud: %w", path, err)
	}
	var info syncRootProviderInfo
	switch hr := syncRootInfo(name, syncRootInfoProvider, &info); hr {
	case 0:
		return syncRootFolder(existing, name), windows.UTF16ToString(info.ProviderName[:]), true, nil
	case notUnderSyncRoot:
		return "", "", false, nil
	default:
		return "", "", false, fmt.Errorf("ask whether %s is kept in sync with the cloud: HRESULT 0x%08X", existing, hr)
	}
}

// syncRootInfo asks CfGetSyncRootInfoByPath what class tells of the sync
// root that holds the path name, into info, the structure class calls for,
// and returns the HRESULT it answers with.
func syncRootInfo[T syncRootProviderInfo | uint64](name *uint16, class uintptr, info *T) uint32 {
	var written uint32
	//nolint:gosec // G103: the call reads the path and writes at most the size of *info there, and how much it wrote at written
	hr, _, _ := procCfGetSyncRootInfoByPath.Call(uintptr(unsafe.Pointer(name)), class, uintptr(unsafe.Pointer(info)),
		unsafe.Sizeof(*info), uintptr(unsafe.Pointer(&written)))
	return uint32(hr) //nolint:gosec // G115: an HRESULT is 32 bits
}

// syncRootFolder names the sync root that holds existing, a folder that
// CfGetSyncRootInfoByPath, asked about the path name, says is in one: of
// the folders that really hold existing, the one whose file ID is the
// root's. NTFS, the only file system the cloud filter serves, keeps that
// 64-bit ID in the low 64 bits of a file's full ID. Each folder is looked at
// itself, a link not followed. It returns "" when the root cannot be found.
func syncRootFolder(existing string, name *uint16) string {
	// CF_SYNC_ROOT_BASIC_INFO, which holds the ID alone.
	var rootID uint64
	if syncRootInfo(name, syncRootInfoBasic, &rootID) != 0 {
		return ""
	}
	held, err := finalPath(existing)
	if err != nil {
		return ""
	}
	for folder := held; ; folder = filepath.Dir(folder) {
		if entry, err := Inspect(folder); err == nil && binary.LittleEndian.Uint64(entry.ID.id[:8]) == rootID {
			return folder
		}
		if filepath.Dir(folder) == folder {
			return ""
		}
	}
}

// nearestExisting returns path, made absolute, or when it does not exist
// yet, the nearest folder above it that does. Each is looked at itself, a
// link not followed, so a link that leads nowhere exists.
func nearestExisting(path string) (string, error) {
	existing, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for {
		_, err := os.Lstat(existing)
		parent := filepath.Dir(existing)
		switch {
		case err == nil:
			return existing, nil
		case !errors.Is(err, fs.ErrNotExist) || parent == existing:
			return "", err
		}
		existing = parent
	}
}

// fileIDInfo is FILE_ID_INFO, what GetFileInformationByHandleEx gives for
// FileIdInfo.
type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

// readFileID reads the ID of the file handle is open on (fileIDByHandle).
// Tests replace it: no volume at hand gives two files IDs that differ only
// above their low 64 bits, as ReFS can.
var readFileID = func(handle windows.Handle) (FileID, error) {
	return fileIDByHandle(handle, windows.GetFileInformationByHandleEx)
}

// fileIDByHandle reads the ID of the file handle is open on in full, as
// FILE_ID_INFO, which infoEx, GetFileInformationByHandleEx, gives. A volume
// that cannot give it refuses with an error that depends on its file system
// and driver, ERROR_INVALID_PARAMETER, ERROR_NOT_SUPPORTED and
// ERROR_INVALID_FUNCTION among them. So any refusal falls back to the file
// index, from GetFileInformationByHandle, the call Inspect has already made
// on the same handle for the rest of what it tells.
func fileIDByHandle(handle windows.Handle, infoEx func(windows.Handle, uint32, *byte, uint32) error) (FileID, error) {
	var full fileIDInfo
	//nolint:gosec // G103: infoEx fills exactly the FILE_ID_INFO it is given, of the size it is told
	if err := infoEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&full)), uint32(unsafe.Sizeof(full))); err == nil {
		return FullFileID(full.VolumeSerialNumber, full.FileID), nil
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return FileID{}, err
	}
	return indexFileID(uint64(info.VolumeSerialNumber), uint64(info.FileIndexHigh)<<32|uint64(info.FileIndexLow)), nil
}

// PrivateHere gives dir the access list of privateDescriptor, as Private
// does, but passes it down to nothing dir holds: each of those keeps what it
// has until it is judged on its own. A folder whose list keeps the user out
// can be listed then, and what it holds looked at before anything is done
// to it.
func PrivateHere(dir string) error {
	sd, err := privateDescriptor()
	if err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	// What its owner may always do, whatever its list says.
	handle, err := openItself(dir, windows.READ_CONTROL|windows.WRITE_DAC)
	if err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	if err := windows.SetKernelObjectSecurity(handle, windows.DACL_SECURITY_INFORMATION, sd); err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	return nil
}

// openItself opens path itself, a link not followed, asking for access and
// nothing else. CreateFile asks for SYNCHRONIZE besides, which only a
// file's own list can grant.
func openItself(path string, access uint32) (windows.Handle, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	name, err := windows.NewNTUnicodeString(ntPath(abs))
	if err != nil {
		return 0, err
	}
	attrs := windows.OBJECT_ATTRIBUTES{ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	var handle windows.Handle
	var io windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access, &attrs, &io, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT, 0, 0)
	var status windows.NTStatus
	if errors.As(err, &status) {
		// As the Win32 error it stands for, which says "access denied"
		// for a file being deleted and "not found" for one gone.
		return 0, status.Errno()
	}
	return handle, err
}

// ntPath is the name the native API takes for abs, an absolute path.
func ntPath(abs string) string {
	switch {
	case strings.HasPrefix(abs, `\\?\`):
		return `\??\` + abs[len(`\\?\`):]
	case strings.HasPrefix(abs, `\\`):
		return `\??\UNC\` + abs[len(`\\`):]
	}
	return `\??\` + abs
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

// The forms GetFinalPathNameByHandle can name a path in (winbase.h).
const (
	// volumeNameDOS is FILE_NAME_NORMALIZED and VOLUME_NAME_DOS, both zero:
	// the path with every link and subst drive resolved, on its drive letter.
	volumeNameDOS = 0
	// volumeNameGUID is VOLUME_NAME_GUID: the same path on the volume's
	// \\?\Volume{…} name, which every volume has.
	volumeNameGUID = 0x1
)

// finalPath returns the path the system opens for path, each junction,
// symbolic link and subst drive in it resolved. On a volume that has no
// drive letter, and so no such path, it fails with ErrNoDriveLetter. Tests
// replace it: no file system at hand fails to say.
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
	final, dosErr := finalPathName(handle, volumeNameDOS)
	if dosErr == nil {
		return dosPath(final), nil
	}
	// The path on the GUID name only tells why there is none on a drive
	// letter: no folder is judged by it. The DOS query's error is left out,
	// since ERROR_PATH_NOT_FOUND, its answer then, reads as a path that does
	// not exist.
	if _, guidErr := finalPathName(handle, volumeNameGUID); noDriveLetter(dosErr, guidErr) {
		return "", fmt.Errorf("resolve %s: %w", path, ErrNoDriveLetter)
	}
	return "", fmt.Errorf("resolve %s: %w", path, dosErr)
}

// finalPathName asks GetFinalPathNameByHandle for the path of the file handle
// is open on, in the form flags names.
func finalPathName(handle windows.Handle, flags uint32) (string, error) {
	size := uint32(windows.MAX_PATH)
	for {
		buf := make([]uint16, size)
		n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], size, flags)
		if err != nil {
			return "", err
		}
		if n < size {
			return windows.UTF16ToString(buf[:n]), nil
		}
		// Too small: n is the size it needs, its terminating null included.
		size = n
	}
}

// noDriveLetter reports whether the volume a path is on has no drive letter,
// from what GetFinalPathNameByHandle answered when asked for the path on the
// volume's DOS name (dosErr) and on its GUID name (guidErr). Every volume has
// a GUID name, but only one with a drive letter, or a folder it is mounted
// in, has a DOS name. When neither could be had, the file system cannot say
// where the path leads at all.
func noDriveLetter(dosErr, guidErr error) bool {
	return dosErr != nil && guidErr == nil
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

// currentUser returns the SID of the user Loadline runs as. Tests replace
// it, to judge lists for a user whose SID has an alias in the security
// descriptor string format, as the built-in Administrator's does.
var currentUser = func() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("identify the current user: %w", err)
	}
	return user.User.Sid.Copy()
}

// trustedSIDs are the principals a private directory admits: the user, and
// the two that can take any file on the machine anyway.
func trustedSIDs() ([]*windows.SID, error) {
	user, err := currentUser()
	if err != nil {
		return nil, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("identify SYSTEM: %w", err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, fmt.Errorf("identify Administrators: %w", err)
	}
	return []*windows.SID{user, system, admins}, nil
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
