package datadir

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// going writes one of Loadline's files and has it deleted soon after, as a
// drain deletes a spooled event between the walk that lists it and the look
// at its access list: Windows refuses to open it meanwhile, with "access
// denied".
func going(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "1-0000000a.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(20*time.Millisecond, fspermtest.DeletePending(t, path))
	return path
}

func TestClearOwnEntries_WaitsOutADeletionAndLeavesOutWhatHasGone(t *testing.T) {
	path := going(t)

	if err := clearOwnEntries(path); err != nil {
		t.Errorf("clearOwnEntries(%s), deleted meanwhile: %v; want nil", path, err)
	}
}

func TestEntryProblem_WaitsOutADeletionAndLeavesOutWhatHasGone(t *testing.T) {
	path := going(t)

	if got, err := entryProblem(path); err != nil || got != "" {
		t.Errorf("entryProblem(%s), deleted meanwhile = %q, %v; want \"\", nil", path, got, err)
	}
}

func TestOwnerProblem_WaitsOutADeletionAndLeavesOutWhatHasGone(t *testing.T) {
	path := going(t)

	if got, err := ownerProblem(filepath.Dir(path), path); err != nil || got != "" {
		t.Errorf("ownerProblem(%s), deleted meanwhile = %q, %v; want \"\", nil", path, got, err)
	}
}

// takeown /r follows a junction out of the folder it is given, and takes
// what the junction leads to, and goes into folders whose contents the user
// cannot see. So the data directory is taken back with what it holds only
// while nothing in it is a link and every folder of it can be listed.
func TestMovable_TakesBackOnlyTheDataDirectoryWhenTakeownCouldLeaveIt(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, dir string){
		"a junction among Loadline's files": func(t *testing.T, dir string) {
			t.Helper()
			junctionAt(t, filepath.Join(dir, "loadline.log"), t.TempDir())
		},
		"a folder of Loadline's that keeps the user out": func(t *testing.T, dir string) {
			t.Helper()
			spool := filepath.Join(dir, "spool")
			fspermtest.SetAccessListAsIs(t, spool, "D:AI(A;OICIID;FA;;;SY)")
			t.Cleanup(func() { _ = fsperm.ClearExplicit(spool) })
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := home(t)
			plant(t, dir)
			found := ownedFolder(dir, gone)
			movableAs(t, found)
			profileHolds(t, dir)
			want := folderReport(found) + takeBackLead + `run takeown /f "` + dir + `" and then loadline install`

			if err := Verify(dir, filepath.Dir(dir)); err == nil || err.Error() != want {
				t.Errorf("Verify = %v;\nwant %s", err, want)
			}
		})
	}
}
