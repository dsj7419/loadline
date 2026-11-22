package fspermtest

import (
	"runtime"
	"sync"
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
	// FullControl is FILE_ALL_ACCESS, the Full control permission of the
	// Security tab.
	FullControl = windows.ACCESS_MASK(0x1f01ff)

	// modify is the Modify permission of the Security tab.
	modify = windows.ACCESS_MASK(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE | windows.DELETE)
)

// Permissive adds an entry to the access list of dir, a directory the test
// created, that lets every signed-in user modify it and everything created
// below it, as the root of a second drive does by default.
func Permissive(tb testing.TB, dir string) {
	tb.Helper()
	Grant(tb, dir, modify, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
}

// Grant adds an entry to the access list of path, a file or directory the
// test created, that gives every signed-in user mask, with inheritance as
// EXPLICIT_ACCESS takes it: NO_INHERITANCE for path alone, or
// SUB_CONTAINERS_AND_OBJECTS_INHERIT, alone or with INHERIT_ONLY.
func Grant(tb testing.TB, path string, mask windows.ACCESS_MASK, inheritance uint32) {
	tb.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		tb.Fatalf("read the access list of %s: %v", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		tb.Fatalf("read the access list of %s: %v", path, err)
	}
	users, err := windows.CreateWellKnownSid(windows.WinAuthenticatedUserSid)
	if err != nil {
		tb.Fatal(err)
	}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(users)
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: mask,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(users),
		},
	}}, dacl)
	if err != nil {
		tb.Fatalf("build the access list of %s: %v", path, err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		tb.Fatalf("give every signed-in user %#x on %s: %v", mask, path, err)
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

// AccessList returns the access list of path in the security descriptor
// string format, to tell whether anything changed it.
func AccessList(tb testing.TB, path string) string {
	tb.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		tb.Fatalf("read the access list of %s: %v", path, err)
	}
	return sd.String()
}

// DeletePending marks path, a file the test created, for deletion while
// holding it open, as a process that deletes a file does for a moment and
// as long as another holds it: until the returned release closes it, which
// deletes it, Windows refuses to open it, with "access denied". The end of
// the test releases it if the test does not.
func DeletePending(tb testing.TB, path string) (release func()) {
	tb.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		tb.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		tb.Fatalf("open %s to delete it: %v", path, err)
	}
	// FILE_DISPOSITION_INFO is one BOOLEAN. Without the POSIX semantics of
	// FileDispositionInfoEx, the name stays in its folder until the handle
	// closes.
	deleteFile := byte(1)
	if err := windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &deleteFile, 1); err != nil {
		_ = windows.CloseHandle(handle)
		tb.Fatalf("mark %s for deletion: %v", path, err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			if err := windows.CloseHandle(handle); err != nil {
				tb.Errorf("close %s: %v", path, err)
			}
		})
	}
	tb.Cleanup(release)
	return release
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
