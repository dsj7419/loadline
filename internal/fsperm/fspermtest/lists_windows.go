package fspermtest

import (
	"encoding/binary"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The kinds of allow entry an access list can hold besides the plain one,
// which the security descriptor string format cannot always write.
const (
	Compound       = 4  // ACCESS_ALLOWED_COMPOUND_ACE_TYPE
	Object         = 5  // ACCESS_ALLOWED_OBJECT_ACE_TYPE
	Callback       = 9  // ACCESS_ALLOWED_CALLBACK_ACE_TYPE
	CallbackObject = 11 // ACCESS_ALLOWED_CALLBACK_OBJECT_ACE_TYPE
)

// aclRevisionDS is ACL_REVISION_DS, the revision of a list that may hold
// entries of every kind.
const aclRevisionDS = 4

// UserSID returns the SID of the user the test runs as, in string form.
func UserSID(tb testing.TB) string {
	tb.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		tb.Fatal(err)
	}
	return user.User.Sid.String()
}

// SetAccessList gives path, a file or directory the test created, the
// access list sddl describes, protected from what its folder passes down
// when sddl says so ("D:P…") and taking it otherwise.
func SetAccessList(tb testing.TB, path, sddl string) {
	tb.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		tb.Fatalf("parse %s: %v", sddl, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		tb.Fatalf("the access list of %s: %v", sddl, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		tb.Fatal(err)
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if control&windows.SE_DACL_PROTECTED != 0 {
		info = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, nil, nil, dacl, nil); err != nil {
		tb.Fatalf("give %s the access list %s: %v", path, sddl, err)
	}
}

// SetAccessListAsIs stores the access list sddl describes on path, a file or
// directory the test created, exactly as written, each entry's inherited
// flag included, as SetFileSecurity and backup tools do: Windows works out
// nothing from the folder above.
func SetAccessListAsIs(tb testing.TB, path, sddl string) {
	tb.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		tb.Fatalf("parse %s: %v", sddl, err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		tb.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.WRITE_DAC|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		tb.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	if err := windows.SetKernelObjectSecurity(handle, windows.DACL_SECURITY_INFORMATION, sd); err != nil {
		tb.Fatalf("store %s on %s: %v", sddl, path, err)
	}
}

// Entry is one allow entry for every signed-in user, of any kind.
type Entry struct {
	// Kind is the entry's type: 0 for a plain one, or one of the kinds
	// above. A compound entry lets them in through SYSTEM.
	Kind byte
	// Flags are the entry's inheritance flags.
	Flags byte
	Mask  windows.ACCESS_MASK
	// ObjectType and InheritedObjectType put a GUID of each in an object
	// entry.
	ObjectType, InheritedObjectType bool
	// Protected keeps what path's folder passes down from applying beside
	// the entry.
	Protected bool
}

// SetEntry gives path, a file or directory the test created, an access list
// that holds entry alone, beside what its folder passes down unless entry
// is Protected.
func SetEntry(tb testing.TB, path string, entry Entry) {
	tb.Helper()
	users, err := windows.CreateWellKnownSid(windows.WinAuthenticatedUserSid)
	if err != nil {
		tb.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		tb.Fatal(err)
	}

	var body []byte
	switch entry.Kind {
	case Compound:
		// CompoundAceType COMPOUND_ACE_IMPERSONATION, a reserved word, and
		// the server's SID; the client's follows.
		body = append([]byte{1, 0, 0, 0}, sidBytes(system)...)
	case Object, CallbackObject:
		var flags uint32
		var guids []byte
		guid := make([]byte, 16)
		guid[0] = 0x42
		if entry.ObjectType {
			flags |= windows.ACE_OBJECT_TYPE_PRESENT
			guids = append(guids, guid...)
		}
		if entry.InheritedObjectType {
			flags |= windows.ACE_INHERITED_OBJECT_TYPE_PRESENT
			guids = append(guids, guid...)
		}
		body = binary.LittleEndian.AppendUint32(nil, flags)
		body = append(body, guids...)
	}
	ace := []byte{entry.Kind, entry.Flags, 0, 0}
	ace = binary.LittleEndian.AppendUint32(ace, uint32(entry.Mask))
	ace = append(ace, body...)
	ace = append(ace, sidBytes(users)...)
	// Two SIDs and two GUIDs at most: far below the 64 KiB a size holds.
	binary.LittleEndian.PutUint16(ace[2:], uint16(len(ace))) //nolint:gosec // G115: see above

	acl := []byte{aclRevisionDS, 0, 0, 0, 1, 0, 0, 0}
	acl = append(acl, ace...)
	binary.LittleEndian.PutUint16(acl[2:], uint16(len(acl))) //nolint:gosec // G115: one entry, as above

	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if entry.Protected {
		info = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	//nolint:gosec // G103: acl is laid out as an ACL, which the call takes by pointer
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, nil, nil, (*windows.ACL)(unsafe.Pointer(&acl[0])), nil); err != nil {
		tb.Fatalf("give %s an entry of kind %d: %v", path, entry.Kind, err)
	}
}

// sidBytes returns sid as the bytes an entry holds it in.
func sidBytes(sid *windows.SID) []byte {
	//nolint:gosec // G103: a SID is GetLengthSid bytes long
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(sid)), windows.GetLengthSid(sid))...)
}
