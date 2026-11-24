package datadir

// A data directory in a folder that a cloud service keeps in sync, such as
// OneDrive's: the service would copy the token, and the binary every hook
// runs, to the cloud, and to every other computer it syncs.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// testProvider names the cloud service of the sync roots the tests register.
const testProvider = "Example Drive"

// syncedFolder returns a private folder registered as a sync root of
// testProvider.
func syncedFolder(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Cloud")
	if err := fsperm.MkdirAll(root); err != nil {
		t.Fatal(err)
	}
	fspermtest.RegisterSyncRoot(t, root, testProvider)
	return root
}

// assertSynced fails the test unless msg, what step reported, refuses dir
// as in root, which testProvider keeps in sync, naming root as the system
// resolves it, and says what to do.
func assertSynced(t *testing.T, step, msg, dir, root string) {
	t.Helper()
	const rest = ", which " + testProvider + " keeps in sync with the cloud, so the token and the binary every hook runs " +
		`would be copied there: set LOADLINE_HOME to a folder outside it, such as the default %LOCALAPPDATA%\loadline`
	named, ok := strings.CutPrefix(msg, dir+" is in ")
	if ok {
		named, ok = strings.CutSuffix(named, rest)
	}
	if !ok || !sameFolder(t, named, root) {
		t.Errorf("%s = %q; want %q, then a name of %s, then %q", step, msg, dir+" is in ", root, rest)
	}
}

// sameFolder reports whether got names the folder want names, however each
// is written.
func sameFolder(t *testing.T, got, want string) bool {
	t.Helper()
	wantInfo, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	gotInfo, err := os.Stat(got)
	return err == nil && os.SameFile(gotInfo, wantInfo)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// A folder made in a sync root is synced whether or not the service has
// turned it into a placeholder yet. Install and the server refuse it as the
// data directory, and change nothing in it, and doctor reports it.
func TestSecure_RefusesADataDirectoryInsideASyncRoot(t *testing.T) {
	root := syncedFolder(t)
	dir := filepath.Join(root, "loadline")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	before := fspermtest.AccessList(t, dir)

	err := Secure(dir, root)

	assertSynced(t, "Secure", errorText(err), dir, root)
	if after := fspermtest.AccessList(t, dir); !after.Equal(before) {
		t.Errorf("the refused Secure changed the access list of %s from %s to %s", dir, before, after)
	}
	got, err := Check(dir, root)
	if err != nil {
		t.Errorf("Check = %v", err)
	}
	assertSynced(t, "Check", got, dir, root)
}

// The token is created through Make, on a hook's path, which refuses a data
// directory in a sync root before it makes anything there.
func TestMake_RefusesADataDirectoryInsideASyncRoot(t *testing.T) {
	root := syncedFolder(t)
	dir := filepath.Join(root, "loadline")

	err := Make(dir)

	assertSynced(t, "Make", errorText(err), dir, root)
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the refused Make made %s (%v)", dir, err)
	}
}

// A data directory not made yet would be made in the folders above it that
// exist, and is refused when those are in a sync root. Doctor says install
// would refuse to make it, and why.
func TestVerify_RefusesADataDirectoryNotMadeYetInsideASyncRoot(t *testing.T) {
	root := syncedFolder(t)
	dir := filepath.Join(root, "apps", "loadline")

	err := Verify(dir, root)

	assertSynced(t, "Verify", errorText(err), dir, root)
	got, err := Check(dir, root)
	notMade, ok := strings.CutPrefix(got, dir+" does not exist yet, and loadline install would refuse to make it: ")
	if err != nil || !ok {
		t.Errorf("Check = %q, %v; want it to say install would refuse to make %s", got, err, dir)
	}
	assertSynced(t, "Check", notMade, dir, root)
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Errorf("%s holds %d entries (%v), want nothing made", root, len(entries), err)
	}
}

// A data directory that is a junction into a sync root is where its files
// would go: in the sync root. Its refusal says so, rather than that it is a
// link, whose advice, to name the folder it leads to itself, would only be
// refused next.
func TestMake_RefusesADataDirectoryThatLeadsIntoASyncRoot(t *testing.T) {
	root := syncedFolder(t)
	inside := filepath.Join(root, "loadline")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(root), "loadline")
	junctionAt(t, dir, inside)

	for step, err := range map[string]error{"Make": Make(dir), "Verify": Verify(dir, filepath.Dir(root))} {
		assertSynced(t, step, errorText(err), dir, root)
	}
}

// replaceSyncRoot makes the checks ask answer whether a path is in a sync
// root, for the rest of the test.
func replaceSyncRoot(t *testing.T, answer func(string) (string, string, bool, error)) {
	t.Helper()
	previous := syncRoot
	syncRoot = answer
	t.Cleanup(func() { syncRoot = previous })
}

// Only a positive answer refuses. Where Windows cannot answer, as on a file
// system the cloud filter does not serve, such as exFAT or a network share,
// or without the Cloud Files API, or where it answers no, the data directory
// is taken, as it was before Loadline asked.
func TestSecure_AcceptsADataDirectoryWhenTheSyncRootQuestionFails(t *testing.T) {
	top := t.TempDir()
	dir := filepath.Join(top, "loadline")
	var asked []string
	answering := func(under bool, err error) func(string) (string, string, bool, error) {
		return func(path string) (string, string, bool, error) {
			asked = append(asked, path)
			if under {
				return top, testProvider, true, nil
			}
			return "", "", false, err
		}
	}
	replaceSyncRoot(t, answering(true, nil))
	if err := Secure(dir, top); err == nil || !slices.Contains(asked, dir) {
		t.Fatalf("Secure = %v, having asked about %q; want it refused for a positive answer about %s, or the test proves nothing", err, asked, dir)
	}

	for _, tt := range []struct {
		name   string
		answer func(string) (string, string, bool, error)
	}{
		{"the question fails", answering(false, errors.New("ask whether it is in a sync root: HRESULT 0x80070032"))},
		{"the answer is no", answering(false, nil)},
	} {
		replaceSyncRoot(t, tt.answer)
		if err := Secure(dir, top); err != nil {
			t.Errorf("%s: Secure = %v; want the data directory taken", tt.name, err)
		}
	}
}
