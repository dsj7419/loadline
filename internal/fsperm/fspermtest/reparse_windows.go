package fspermtest

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"testing"

	"golang.org/x/sys/windows"
)

// Reparse tags the tests give paths (winnt.h), each of a kind Loadline tells
// apart.
const (
	// TagLXSymlink is IO_REPARSE_TAG_LX_SYMLINK, a symbolic link of the
	// Windows Subsystem for Linux: a name surrogate.
	TagLXSymlink uint32 = 0xA000001D
	// TagOneDrive is IO_REPARSE_TAG_ONEDRIVE, the tag OneDrive gave its
	// files before Files On-Demand: a cloud file.
	TagOneDrive uint32 = 0x80000021
	// TagDedup is IO_REPARSE_TAG_DEDUP, which data deduplication gives the
	// files whose data it shares with others on the volume.
	TagDedup uint32 = 0x80000013
	// TagAppExecLink is IO_REPARSE_TAG_APPEXECLINK, an app execution alias,
	// which leads to the app it starts without saying it stands for a name.
	TagAppExecLink uint32 = 0x8000001B
	// TagThirdParty is a tag of no Microsoft product, as a third-party file
	// system filter may use.
	TagThirdParty uint32 = 0x0000ABCD
	// TagThirdPartySurrogate is such a tag that says it stands for another
	// name.
	TagThirdPartySurrogate uint32 = 0x2000ABCD
)

const (
	// microsoftTag is the bit of a tag that Microsoft owns, whose reparse
	// point needs no GUID to name its owner.
	microsoftTag = 0x80000000
	// cloudTag is IO_REPARSE_TAG_CLOUD; CLOUD_1 to CLOUD_F add a number in
	// the bits of cloudTagNumber.
	cloudTag       = 0x9000001A
	cloudTagNumber = 0xF000
	// reparseData is what each reparse point planted here holds.
	reparseData = "loadline-test"
)

// testReparseGUID names the owner of a planted reparse point whose tag is not
// Microsoft's.
var testReparseGUID = [16]byte{0x3d, 0x1c, 0x2e, 0x5a, 0xb4, 0x51, 0x6e, 0x4c, 0x9d, 0x1a, 0x10, 0xad, 0x11, 0x0e, 0x5a, 0x02}

// PlantReparsePoint makes path, a file or folder the test created, a reparse
// point with tag, as a file system filter does, and takes it off again when
// the test ends. No filter on the volume handles such a point: Windows reads
// its tag, and opens the path itself, but opens nothing through it, so that
// reading the file, or listing the folder, fails.
//
// It refuses a cloud tag (IO_REPARSE_TAG_CLOUD, or CLOUD_1 to CLOUD_F). While
// the cloud filter is attached to the volume, it refuses every open of a path
// that holds one it did not set, deleting it included, until Windows
// restarts. TagOneDrive stands in for a cloud file instead.
func PlantReparsePoint(tb testing.TB, path string, tag uint32) {
	tb.Helper()
	if tag&^cloudTagNumber == cloudTag {
		tb.Fatalf("plant tag 0x%08X on %s: a cloud tag that no sync engine set leaves the path impossible to open "+
			"or delete until Windows restarts; use TagOneDrive", tag, path)
	}
	if err := controlReparsePoint(path, windows.FSCTL_SET_REPARSE_POINT, reparseBuffer(tag, reparseData)); err != nil {
		tb.Fatalf("plant tag 0x%08X on %s: %v", tag, path, err)
	}
	tb.Cleanup(func() {
		err := controlReparsePoint(path, windows.FSCTL_DELETE_REPARSE_POINT, reparseBuffer(tag, ""))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			tb.Errorf("take tag 0x%08X off %s: %v", tag, path, err)
		}
	})
}

// reparseBuffer is what FSCTL_SET_REPARSE_POINT takes for a reparse point of
// tag that holds data, and with no data, what FSCTL_DELETE_REPARSE_POINT
// takes to remove one: REPARSE_DATA_BUFFER for a tag of Microsoft's, and for
// any other REPARSE_GUID_DATA_BUFFER, whose GUID after the header names the
// point's owner.
func reparseBuffer(tag uint32, data string) []byte {
	header := 8
	if tag&microsoftTag == 0 {
		header += len(testReparseGUID)
	}
	buf := make([]byte, header+len(data))
	binary.LittleEndian.PutUint32(buf[0:], tag)
	binary.LittleEndian.PutUint16(buf[4:], uint16(len(data))) //nolint:gosec // G115: data is reparseData or empty
	copy(buf[8:header], testReparseGUID[:])
	copy(buf[header:], data)
	return buf
}

// controlReparsePoint sends the reparse point of path itself, a link not
// followed, the control code with buf.
func controlReparsePoint(path string, code uint32, buf []byte) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var returned uint32
	return windows.DeviceIoControl(handle, code, &buf[0], uint32(len(buf)), nil, 0, &returned, nil) //nolint:gosec // G115: a reparse buffer is a few dozen bytes
}
