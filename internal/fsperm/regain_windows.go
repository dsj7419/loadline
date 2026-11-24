package fsperm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"
)

// listRights are what the entry RegainList adds gives the user on the folder
// itself: what a listing opens a folder with (FILE_GENERIC_READ, which
// os.ReadDir asks for as GENERIC_READ), and the right to pass through it.
const listRights windows.ACCESS_MASK = windows.FILE_LIST_DIRECTORY | windows.FILE_READ_EA | windows.FILE_TRAVERSE |
	windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE | windows.READ_CONTROL

// canList is Listable, which RegainList asks before and after it adds its
// entry. Tests replace it: no folder at hand fails that second question
// with an error.
var canList = Listable

// setSecurity is SetKernelObjectSecurity, through which RegainList stores a
// list. Tests replace it: no folder at hand refuses to take back, through a
// handle that may change its list, the list it had a moment before.
var setSecurity = windows.SetKernelObjectSecurity

// CanChangeList reports whether this process may change the access list of
// dir itself, whatever the list says: whether it can open dir for that, as
// its owner always may unless an OWNER RIGHTS entry says what the owner may
// do instead. An elevated process may as Administrators, which own what an
// elevated process creates.
func CanChangeList(dir string) (bool, error) {
	handle, err := openItself(dir, windows.READ_CONTROL|windows.WRITE_DAC)
	switch {
	case err == nil:
		_ = windows.CloseHandle(handle)
		return true, nil
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return false, nil
	}
	return false, fmt.Errorf("open %s to change its access list: %w", dir, err)
}

// RegainList lets this process list dir, a folder whose own access list keeps
// it out, when that widens nobody else's access, and reports whether it
// changed the list. A folder it can list already is left as it is.
//
// It adds one entry, for the user alone, on dir itself and passed on to
// nothing, that gives listRights. The entry goes after every entry dir was
// given explicitly, and so after each denial among them, which it cannot
// override then, and before what dir inherits: as any entry given
// explicitly does, it overrides an inherited denial, for the user alone.
// Whether the list is protected, and whether Windows worked out what it
// inherits, stays as it was. When dir still cannot be listed then, as when a
// denial keeps the user out, RegainList puts the list back exactly as it was
// and returns ErrStillLocked, as it does for a folder whose list this process
// may not change (CanChangeList): taking a denial off would let in whoever
// else it keeps out.
//
// Two processes that run it on dir at once may each add the entry, and each
// put back the list it read, which can leave the entry there once more. That
// one too lets in the user alone, after every explicit denial.
func RegainList(dir string) (bool, error) {
	listable, err := canList(dir)
	if err != nil || listable {
		return false, err
	}
	handle, err := openItself(dir, windows.READ_CONTROL|windows.WRITE_DAC)
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return false, ErrStillLocked
	case err != nil:
		return false, fmt.Errorf("let the user list %s: %w", dir, err)
	}
	// The handle keeps the right to change the list whatever the new list
	// says, so the old one can always be put back through it.
	defer func() { _ = windows.CloseHandle(handle) }()
	original, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("read the access list of %s: %w", dir, err)
	}
	defer runtime.KeepAlive(original)
	control, _, err := original.Control()
	if err != nil {
		return false, fmt.Errorf("read the access list of %s: %w", dir, err)
	}
	dacl, err := presentList(original)
	if err != nil {
		return false, fmt.Errorf("read the access list of %s: %w", dir, err)
	}
	widened, err := withListEntry(dacl)
	if err == nil {
		err = storeList(handle, widened, control)
	}
	if err != nil {
		return false, fmt.Errorf("let the user list %s: %w", dir, err)
	}

	listable, err = canList(dir)
	if err == nil && listable {
		return true, nil
	}
	if restoreErr := storeList(handle, dacl, control); restoreErr != nil {
		const kept = "put back the access list of %s, which keeps the entry added to let you list it, "
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

// passable reports nothing here: Windows lets every account pass through any
// folder (bypass traverse checking), and a folder's listing lets its lister
// look at what it holds.
func passable(string) error { return nil }

// ListableOnceRegained reports, changing nothing, whether dir, a folder this
// process cannot list, could be listed once RegainList gave the user its
// entry: Windows judges the list it would make for this process as it judges
// a listing, so a denial before the entry that applies to the user keeps it
// from that. Whether this process may change the list at all is
// CanChangeList's question; one whose list it may not read cannot be judged,
// and reads as false.
func ListableOnceRegained(dir string) (bool, error) {
	handle, err := openItself(dir, windows.READ_CONTROL)
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read the access list of %s: %w", dir, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("read the access list of %s: %w", dir, err)
	}
	defer runtime.KeepAlive(sd)
	dacl, err := presentList(sd)
	if errors.Is(err, ErrStillLocked) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the access list of %s: %w", dir, err)
	}
	widened, err := withListEntry(dacl)
	if err != nil {
		return false, fmt.Errorf("judge the access list of %s: %w", dir, err)
	}
	granted, err := grants(sd, widened, windows.FILE_GENERIC_READ)
	if err != nil {
		return false, fmt.Errorf("judge the access list of %s: %w", dir, err)
	}
	return granted, nil
}

// presentList returns the access list sd holds. A folder without one, which
// admits everyone, is not kept from anyone by its list, and RegainList has
// nothing to add to it: ErrStillLocked.
func presentList(sd *windows.SECURITY_DESCRIPTOR) (*windows.ACL, error) {
	dacl, _, err := sd.DACL()
	switch {
	case errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND), err == nil && dacl == nil:
		return nil, ErrStillLocked
	case err != nil:
		return nil, err
	}
	return dacl, nil
}

// withListEntry returns dacl with the entry RegainList adds after the last
// entry given explicitly, laid out as an access list: in a list in the order
// Windows keeps, after the denials and allows given explicitly and before
// every inherited entry. A list out of that order keeps each of its explicit
// denials ahead of the entry all the same. Every other entry is copied as it
// is, byte for byte.
func withListEntry(dacl *windows.ACL) (*windows.ACL, error) {
	user, err := currentUser()
	if err != nil {
		return nil, err
	}
	entries := make([][]byte, 0, int(dacl.AceCount)+1)
	at := 0
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, err
		}
		//nolint:gosec // G103: an entry is AceSize bytes long
		entries = append(entries, unsafe.Slice((*byte)(unsafe.Pointer(ace)), ace.Header.AceSize))
		if ace.Header.AceFlags&windows.INHERITED_ACE == 0 {
			at = len(entries)
		}
	}
	entries = slices.Insert(entries, at, allowEntry(user, listRights))

	// The list's revision, its first byte, stays: the entry is of the one
	// kind every revision holds.
	revision := *(*byte)(unsafe.Pointer(dacl)) //nolint:gosec // G103: an access list starts with its revision
	list := []byte{revision, 0, 0, 0, 0, 0, 0, 0}
	for _, entry := range entries {
		list = append(list, entry...)
	}
	if len(list) > math.MaxUint16 {
		return nil, fmt.Errorf("an access list of %d bytes, more than one may hold: %w", len(list), windows.ERROR_INVALID_ACL)
	}
	binary.LittleEndian.PutUint16(list[2:], uint16(len(list)))    //nolint:gosec // G115: bounded above
	binary.LittleEndian.PutUint16(list[4:], uint16(len(entries))) //nolint:gosec // G115: fewer entries than bytes
	return (*windows.ACL)(unsafe.Pointer(&list[0])), nil          //nolint:gosec // G103: list is laid out as an access list
}

// allowEntry is a plain allow entry that gives mask to sid on the object that
// holds it alone, passed on to nothing.
func allowEntry(sid *windows.SID, mask windows.ACCESS_MASK) []byte {
	//nolint:gosec // G103: a SID is GetLengthSid bytes long
	trustee := unsafe.Slice((*byte)(unsafe.Pointer(sid)), windows.GetLengthSid(sid))
	entry := make([]byte, 8, 8+len(trustee))
	entry[0] = windows.ACCESS_ALLOWED_ACE_TYPE
	binary.LittleEndian.PutUint16(entry[2:], uint16(8+len(trustee))) //nolint:gosec // G115: a SID is at most 68 bytes
	binary.LittleEndian.PutUint32(entry[4:], uint32(mask))
	return append(entry, trustee...)
}

// storeList gives the object handle is open on the access list dacl, as it
// is: protected, and marked as one Windows worked out what it inherits for,
// when control says so of the list it replaces. Windows works out nothing
// from the folder above.
func storeList(handle windows.Handle, dacl *windows.ACL, control windows.SECURITY_DESCRIPTOR_CONTROL) error {
	sd, err := windows.NewSecurityDescriptor()
	if err != nil {
		return err
	}
	if err := sd.SetDACL(dacl, true, false); err != nil {
		return err
	}
	bits := control & (windows.SE_DACL_PROTECTED | windows.SE_DACL_AUTO_INHERITED)
	// Windows keeps the mark that it worked out what a list inherits only
	// when asked to work it out, which leaves a list given whole as it is.
	if bits&windows.SE_DACL_AUTO_INHERITED != 0 {
		bits |= windows.SE_DACL_AUTO_INHERIT_REQ
	}
	if err := sd.SetControl(windows.SE_DACL_PROTECTED|windows.SE_DACL_AUTO_INHERITED|windows.SE_DACL_AUTO_INHERIT_REQ, bits); err != nil {
		return err
	}
	return setSecurity(handle, windows.DACL_SECURITY_INFORMATION, sd)
}

// procAccessCheck is AccessCheck, which judges a security descriptor for a
// token as Windows judges an object's own.
var procAccessCheck = windows.NewLazySystemDLL("advapi32.dll").NewProc("AccessCheck")

// genericMapping is GENERIC_MAPPING: what each generic right stands for on a
// file.
type genericMapping struct {
	read, write, execute, all windows.ACCESS_MASK
}

// privilegeSet is PRIVILEGE_SET with room for one privilege, which
// AccessCheck fills with any it used. It uses none for the rights a listing
// asks for.
type privilegeSet struct {
	count, control uint32
	privilege      windows.LUIDAndAttributes
}

// grants reports whether a folder whose owner and group are those of sd, and
// whose access list is dacl, would give this process every one of rights.
func grants(sd *windows.SECURITY_DESCRIPTOR, dacl *windows.ACL, rights windows.ACCESS_MASK) (bool, error) {
	owner, _, err := sd.Owner()
	if err != nil {
		return false, err
	}
	group, _, err := sd.Group()
	if err != nil {
		return false, err
	}
	judged, err := windows.NewSecurityDescriptor()
	if err == nil {
		err = judged.SetOwner(owner, false)
	}
	if err == nil {
		err = judged.SetGroup(group, false)
	}
	if err == nil {
		err = judged.SetDACL(dacl, true, false)
	}
	if err != nil {
		return false, err
	}

	// AccessCheck judges for an impersonation token: this process's own,
	// copied as one.
	var process, token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &process); err != nil {
		return false, err
	}
	defer func() { _ = process.Close() }()
	if err := windows.DuplicateTokenEx(process, windows.TOKEN_QUERY, nil, windows.SecurityIdentification, windows.TokenImpersonation, &token); err != nil {
		return false, err
	}
	defer func() { _ = token.Close() }()
	mapping := genericMapping{windows.FILE_GENERIC_READ, windows.FILE_GENERIC_WRITE, windows.FILE_GENERIC_EXECUTE, fileAllAccess}
	var privileges privilegeSet
	size := uint32(unsafe.Sizeof(privileges))
	var granted windows.ACCESS_MASK
	var status int32
	//nolint:gosec // G103: AccessCheck reads judged and mapping and writes at most size bytes to privileges, then granted and status
	ok, _, err := procAccessCheck.Call(uintptr(unsafe.Pointer(judged)), uintptr(token), uintptr(rights),
		uintptr(unsafe.Pointer(&mapping)), uintptr(unsafe.Pointer(&privileges)), uintptr(unsafe.Pointer(&size)),
		uintptr(unsafe.Pointer(&granted)), uintptr(unsafe.Pointer(&status)))
	if ok == 0 {
		return false, fmt.Errorf("AccessCheck: %w", err)
	}
	return status != 0, nil
}

// ResetAdvice says what an administrator can do to dir, a folder whose
// permissions Loadline cannot put right without widening who else may use
// it: reset them, with icacls, to what the folder above passes down, which in
// the user's profile lets them in. It gives the command to paste, the path in
// double quotes, unless the path holds what a shell reads as something else
// even there (PasteUnsafe): it names the tool and what to reset then, since
// that command could reset another folder.
func ResetAdvice(dir string) string {
	if PasteUnsafe(dir) {
		return "reset the permissions of " + dir + " with icacls"
	}
	return `reset it with icacls "` + dir + `" /reset`
}
