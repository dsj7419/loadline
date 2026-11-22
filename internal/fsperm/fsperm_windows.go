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

// accessAllowedCallbackACEType is ACCESS_ALLOWED_CALLBACK_ACE_TYPE, a
// conditional allow entry. Up to its SID it is laid out as an
// ACCESS_ALLOWED_ACE.
const accessAllowedCallbackACEType = 9

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

// Private gives dir the access list of privateDescriptor. Windows applies it
// to everything dir already holds, replacing what each of those inherited;
// access a file was given explicitly stays.
func Private(dir string) error {
	sd, err := privateDescriptor()
	if err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	runtime.KeepAlive(sd)
	if err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	return nil
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
// or rename the folders that folder holds, or "" when nobody may.
func emptiable(folder string) (string, error) {
	others, err := othersGranted(folder, fileDeleteChild)
	if err != nil || len(others) == 0 {
		return "", err
	}
	return folder + " lets " + andList(others) + " delete or rename the folders in it", nil
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
		if !grants(ace) || !counts(ace.Mask, ace.Header.AceFlags) {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) //nolint:gosec // G103: an allow entry's SID starts at SidStart
		if slices.ContainsFunc(trusted, sid.Equals) || isCreatorPlaceholder(sid) {
			continue
		}
		if name := accountName(sid); !slices.Contains(others, name) {
			others = append(others, name)
		}
	}
	runtime.KeepAlive(sd)
	return others, false, nil
}

// grants reports whether ace allows its trustee anything. Deny entries
// only take access away.
func grants(ace *windows.ACCESS_ALLOWED_ACE) bool {
	switch ace.Header.AceType {
	case windows.ACCESS_ALLOWED_ACE_TYPE, accessAllowedCallbackACEType:
		return ace.Mask != 0
	default:
		return false
	}
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

// isCreatorPlaceholder reports whether sid is CREATOR OWNER or CREATOR
// GROUP, which stand in for whoever creates a file below and match nobody
// on the folder itself.
func isCreatorPlaceholder(sid *windows.SID) bool {
	return sid.IsWellKnown(windows.WinCreatorOwnerSid) || sid.IsWellKnown(windows.WinCreatorGroupSid)
}

// samePath reports whether a and b name the same folder, as Windows compares
// names: without regard to case.
func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
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
// answers to it, as for one deleted since or one of another machine.
func accountName(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	return domain + `\` + account
}
