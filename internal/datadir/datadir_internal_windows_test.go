package datadir

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestClearOthers_WaitsOutADeletionAndLeavesOutWhatHasGone(t *testing.T) {
	path := going(t)

	if err := clearOthers(path); err != nil {
		t.Errorf("clearOthers(%s), deleted meanwhile: %v; want nil", path, err)
	}
}

func TestEntryProblem_WaitsOutADeletionAndLeavesOutWhatHasGone(t *testing.T) {
	path := going(t)

	if got, err := entryProblem(path); err != nil || got != "" {
		t.Errorf("entryProblem(%s), deleted meanwhile = %q, %v; want \"\", nil", path, got, err)
	}
}
