package datadir

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// Windows refuses to open a folder whose deletion is pending, with "access
// denied", until the last handle on it closes. A profile being deleted is
// waited for, and once gone, left out.
func TestMake_TakesADataDirectoryWhoseBrowserProfileIsBeingDeleted(t *testing.T) {
	profileGoingAway(t, func(t *testing.T, profile string) {
		t.Helper()
		time.AfterFunc(20*time.Millisecond, fspermtest.DeletePending(t, profile))
	})
}

// The browser profile is made in the data directory, and making it makes the
// data directory too when it is missing. In a folder a cloud service keeps
// in sync, the service would copy the dashboard's cookie, and then the token,
// to the cloud: PrepareBrowserProfile refuses it as the data directory's
// checks do, and makes nothing there.
func TestPrepareBrowserProfile_RefusesADataDirectoryInASyncRoot(t *testing.T) {
	root := syncedFolder(t)
	dir := filepath.Join(root, "loadline")

	err := PrepareBrowserProfile(filepath.Join(dir, browserProfile))

	assertSynced(t, "PrepareBrowserProfile", errorText(err), dir, root)
	if _, statErr := os.Lstat(dir); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("PrepareBrowserProfile made %s (%v), in a sync root, although it refused it", dir, statErr)
	}
}
