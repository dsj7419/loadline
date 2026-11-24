package fsperm

import (
	"encoding/binary"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// When the folders that really hold the data directory cannot be found, as
// on a file system that cannot say where a path leads, install and the
// server refuse it, and say what to do.
func TestMovableAncestor_SaysWhatToDoWhenThePathCannotBeResolved(t *testing.T) {
	previous := finalPath
	finalPath = func(string) (string, error) { return "", errors.New("the file system cannot say") }
	t.Cleanup(func() { finalPath = previous })
	top := t.TempDir()

	_, err := MovableAncestor(filepath.Join(top, "loadline"), top)

	if err == nil || !strings.Contains(err.Error(), "the file system cannot say") ||
		!strings.HasSuffix(err.Error(), "set LOADLINE_HOME to a folder in your user profile") {
		t.Errorf("MovableAncestor = %v; want the reason and the advice", err)
	}
}

// An ID read in full and one read as a 64-bit index can hold the same
// numbers and still be two different files: a volume serial number is 32 bits
// beside an index and 64 bits beside a full ID. The two forms never compare
// equal, nor does either equal the zero FileID, which is no file's.
func TestFileID_FormsNeverCompareEqual(t *testing.T) {
	const volume, index = 0x5eed, 0x0003_0000_0000_002a
	var low [16]byte
	binary.LittleEndian.PutUint64(low[:8], index)
	full, byIndex := FullFileID(volume, low), indexFileID(volume, index)
	if full.volume != byIndex.volume || full.id != byIndex.id {
		t.Fatalf("FullFileID = %+v and indexFileID = %+v hold different numbers, so the test proves nothing", full, byIndex)
	}

	if full == byIndex {
		t.Errorf("a full ID equals an index ID of the same numbers: %+v", full)
	}
	if full != FullFileID(volume, low) || byIndex != indexFileID(volume, index) {
		t.Errorf("an ID made again from the same numbers differs from itself: %+v, %+v", full, byIndex)
	}
	for _, id := range []FileID{full, byIndex, FullFileID(0, [16]byte{}), indexFileID(0, 0)} {
		if id == (FileID{}) {
			t.Errorf("%+v equals the zero FileID", id)
		}
	}
}
