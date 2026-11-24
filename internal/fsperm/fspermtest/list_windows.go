package fspermtest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/bits"
	"runtime"
	"slices"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// List is an access list as Windows stores it, to compare by what it holds.
// Its text in the security descriptor string format is for messages alone:
// that format writes some SIDs by an alias, the built-in Administrator's as
// LA, for one, so the text of two lists that are the same can differ.
type List struct {
	// Missing is a file without an access list, which admits everyone.
	Missing bool
	// Protected keeps what the folder above passes down from applying;
	// AutoInherited marks a list Windows has worked out inheritance for.
	Protected, AutoInherited bool
	Entries                  []ACE
	text                     string
}

// ACE is one entry of an access list.
type ACE struct {
	Type, Flags byte
	Mask        windows.ACCESS_MASK
	// Object is what an object entry holds between its mask and its SID:
	// which GUIDs follow, and the GUIDs.
	Object []byte
	// SID is the trustee, nil for a kind of entry whose layout is not read
	// here, which Rest then holds whole after the mask.
	SID *windows.SID
	// Rest is what follows the SID in a callback entry, its condition.
	Rest []byte
}

// AccessList returns the access list of path.
func AccessList(tb testing.TB, path string) List {
	tb.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		tb.Fatalf("read the access list of %s: %v", path, err)
	}
	return listOf(tb, sd)
}

// Descriptor returns the access list of path, with the control bits that say
// whether it is protected and whether Windows worked out what it inherits,
// as the bytes Windows keeps them in. Two lists that hold the same entries
// can differ in those bytes, as in unused space after an entry's SID.
func Descriptor(tb testing.TB, path string) []byte {
	tb.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		tb.Fatalf("read the access list of %s: %v", path, err)
	}
	defer runtime.KeepAlive(sd)
	//nolint:gosec // G103: a self-relative security descriptor is Length bytes long
	return bytes.Clone(unsafe.Slice((*byte)(unsafe.Pointer(sd)), sd.Length()))
}

// ParseList returns the access list sddl, a security descriptor string such
// as "D:P(A;OICI;FA;;;SY)", describes.
func ParseList(tb testing.TB, sddl string) List {
	tb.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		tb.Fatalf("parse %s: %v", sddl, err)
	}
	return listOf(tb, sd)
}

// Equal reports whether l and other are the same list: the same protection
// and inheritance marks, and the same entries in the same order.
func (l List) Equal(other List) bool {
	return l.Missing == other.Missing && l.Protected == other.Protected && l.AutoInherited == other.AutoInherited &&
		l.SameEntries(other)
}

// SameEntries reports whether l and other hold the same entries in the same
// order, whatever their marks.
func (l List) SameEntries(other List) bool {
	return slices.EqualFunc(l.Entries, other.Entries, ACE.Equal)
}

// String returns the list in the security descriptor string format, to show.
func (l List) String() string { return l.text }

// Denies reports whether l holds an entry that denies anything.
func (l List) Denies() bool {
	return slices.ContainsFunc(l.Entries, func(ace ACE) bool { return ace.Type == windows.ACCESS_DENIED_ACE_TYPE })
}

// Names reports whether l holds an entry for sid.
func (l List) Names(sid *windows.SID) bool {
	return slices.ContainsFunc(l.Entries, func(ace ACE) bool { return ace.SID != nil && ace.SID.Equals(sid) })
}

// Equal reports whether a and other are the same entry, their trustees
// compared as SIDs (EqualSid), not as text.
func (a ACE) Equal(other ACE) bool {
	sameSID := a.SID == other.SID || (a.SID != nil && other.SID != nil && a.SID.Equals(other.SID))
	return a.Type == other.Type && a.Flags == other.Flags && a.Mask == other.Mask && sameSID &&
		bytes.Equal(a.Object, other.Object) && bytes.Equal(a.Rest, other.Rest)
}

func listOf(tb testing.TB, sd *windows.SECURITY_DESCRIPTOR) List {
	tb.Helper()
	defer runtime.KeepAlive(sd)
	control, _, err := sd.Control()
	if err != nil {
		tb.Fatal(err)
	}
	list := List{
		Protected:     control&windows.SE_DACL_PROTECTED != 0,
		AutoInherited: control&windows.SE_DACL_AUTO_INHERITED != 0,
		text:          sd.String(),
	}
	dacl, _, err := sd.DACL()
	switch {
	case errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND), err == nil && dacl == nil:
		list.Missing = true
		return list
	case err != nil:
		tb.Fatal(err)
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			tb.Fatalf("read entry %d of %s: %v", i, list.text, err)
		}
		//nolint:gosec // G103: an entry is AceSize bytes long
		raw := unsafe.Slice((*byte)(unsafe.Pointer(ace)), ace.Header.AceSize)
		list.Entries = append(list.Entries, parseACE(tb, raw))
	}
	return list
}

// The kinds of entry whose layout parseACE reads: those with a SID right
// after the mask, those with a condition after the SID, and the object
// kinds, which hold GUIDs before it.
var (
	plainKinds    = []byte{0, 1, 2, 3, 17} // allowed, denied, audit, alarm, mandatory label
	callbackKinds = []byte{9, 10, 13}      // allowed, denied and audit callback
	objectKinds   = []byte{5, 6, 7, 8, 11, 12, 15, 16}
)

func parseACE(tb testing.TB, raw []byte) ACE {
	tb.Helper()
	if len(raw) < 8 {
		tb.Fatalf("an entry of %d bytes", len(raw))
	}
	ace := ACE{Type: raw[0], Flags: raw[1], Mask: windows.ACCESS_MASK(binary.LittleEndian.Uint32(raw[4:8]))}
	at := 8
	switch {
	case slices.Contains(objectKinds, ace.Type):
		if len(raw) < 12 {
			tb.Fatalf("an object entry of %d bytes", len(raw))
		}
		at = 12 + 16*bits.OnesCount32(binary.LittleEndian.Uint32(raw[8:12])&3)
		ace.Object = slices.Clone(raw[8:at])
	case slices.Contains(plainKinds, ace.Type), slices.Contains(callbackKinds, ace.Type):
	default:
		ace.Rest = slices.Clone(raw[8:])
		return ace
	}
	if at >= len(raw) {
		tb.Fatalf("an entry of kind %d with no room for its SID", ace.Type)
	}
	sid := (*windows.SID)(unsafe.Pointer(&raw[at])) //nolint:gosec // G103: the entry's SID starts at the offset its kind gives
	end := at + int(windows.GetLengthSid(sid))
	if end > len(raw) {
		tb.Fatalf("an entry of kind %d whose SID runs past it", ace.Type)
	}
	copied, err := sid.Copy()
	if err != nil {
		tb.Fatal(err)
	}
	ace.SID = copied
	// What follows a plain entry's SID is padding, which holds nothing.
	if !slices.Contains(plainKinds, ace.Type) {
		ace.Rest = slices.Clone(raw[end:])
	}
	return ace
}
