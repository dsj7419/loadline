package fsperm

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// fileAllAccess is FILE_ALL_ACCESS, the Full control of the Security tab.
	fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

	// accessAllowedCallbackACEType is ACCESS_ALLOWED_CALLBACK_ACE_TYPE, a
	// conditional allow entry. Up to its SID it is laid out as an
	// ACCESS_ALLOWED_ACE.
	accessAllowedCallbackACEType = 9
)

// Private replaces the access list of dir with one that gives full control
// to the current user, SYSTEM and Administrators, and to nobody else, and
// that every file and directory below dir inherits. The list is protected,
// so that what the folder above dir passes down no longer applies. Windows
// applies the new list to everything dir already holds, replacing what each
// of those inherited; access a file was given explicitly stays.
func Private(dir string) error {
	trusted, err := trustedSIDs()
	if err != nil {
		return err
	}
	// The entries hold the SIDs by address while Windows builds the list.
	var pinner runtime.Pinner
	defer pinner.Unpin()
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(trusted))
	for _, sid := range trusted {
		pinner.Pin(sid)
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: fileAllAccess,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("make %s private: build its access list: %w", dir, err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	return nil
}

// Exposure says who, besides the current user, SYSTEM and Administrators,
// the access list of path lets in, naming each trustee an allow entry grants
// anything to. It returns "" when the list admits nobody else.
func Exposure(path string) (string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", fmt.Errorf("read the access list of %s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	switch {
	case errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND), err == nil && dacl == nil:
		return "it has no access list, which admits everyone", nil
	case err != nil:
		return "", fmt.Errorf("read the access list of %s: %w", path, err)
	}
	trusted, err := trustedSIDs()
	if err != nil {
		return "", err
	}

	var others []string
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return "", fmt.Errorf("read the access list of %s: %w", path, err)
		}
		if !grants(ace) {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) //nolint:gosec // G103: an allow entry's SID starts at SidStart
		if slices.ContainsFunc(trusted, sid.Equals) {
			continue
		}
		if name := accountName(sid); !slices.Contains(others, name) {
			others = append(others, name)
		}
	}
	if len(others) == 0 {
		return "", nil
	}
	return "its access list also admits " + andList(others), nil
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
