package fspermtest

import (
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Access masks a test asks about.
const (
	// Read is the right to read a file's contents or list a directory.
	Read = windows.ACCESS_MASK(windows.FILE_READ_DATA)
	// Write is the right to change, extend or delete a file, or to add
	// files to a directory.
	Write = windows.ACCESS_MASK(windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.DELETE)
	// Any is every right a file or directory can grant.
	Any = windows.ACCESS_MASK(0xffffffff)

	// modify is the Modify permission of the Security tab.
	modify = windows.ACCESS_MASK(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE | windows.DELETE)
)

// Permissive adds an entry to the access list of dir, a directory the test
// created, that lets every signed-in user modify it and everything created
// below it, as the root of a second drive does by default.
func Permissive(tb testing.TB, dir string) {
	tb.Helper()
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		tb.Fatalf("read the access list of %s: %v", dir, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		tb.Fatalf("read the access list of %s: %v", dir, err)
	}
	users, err := windows.CreateWellKnownSid(windows.WinAuthenticatedUserSid)
	if err != nil {
		tb.Fatal(err)
	}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(users)
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: modify,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(users),
		},
	}}, dacl)
	if err != nil {
		tb.Fatalf("build the access list of %s: %v", dir, err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		tb.Fatalf("let every signed-in user modify %s: %v", dir, err)
	}
}

// AuthenticatedUsers is the name Windows gives the group that Permissive
// lets in, in the language of this system.
func AuthenticatedUsers(tb testing.TB) string {
	tb.Helper()
	users, err := windows.CreateWellKnownSid(windows.WinAuthenticatedUserSid)
	if err != nil {
		tb.Fatal(err)
	}
	return name(users)
}

// Others lists the trustees, other than the current user, SYSTEM and
// Administrators, whose allow entries in the access list of path grant any
// of mask. A missing access list admits everyone, and so is "Everyone".
func Others(tb testing.TB, path string, mask windows.ACCESS_MASK) []string {
	tb.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		tb.Fatalf("read the access list of %s: %v", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		tb.Fatalf("read the access list of %s: %v", path, err)
	}
	if dacl == nil {
		return []string{"Everyone"}
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		tb.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		tb.Fatal(err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		tb.Fatal(err)
	}
	var others []string
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			tb.Fatalf("read entry %d of the access list of %s: %v", i, path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask&mask == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) //nolint:gosec // G103: an ACE's SID starts at SidStart
		if sid.Equals(user.User.Sid) || sid.Equals(system) || sid.Equals(admins) {
			continue
		}
		others = append(others, name(sid))
	}
	return others
}

// Protected reports whether the access list of path ignores what its parent
// passes down.
func Protected(tb testing.TB, path string) bool {
	tb.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		tb.Fatalf("read the access list of %s: %v", path, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		tb.Fatalf("read the security descriptor of %s: %v", path, err)
	}
	return control&windows.SE_DACL_PROTECTED != 0
}

func name(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	return domain + `\` + account
}
