package cli

import (
	"testing"

	"github.com/dsj7419/loadline/internal/datadir"
)

// What the server and the status line write in the data directory must be
// what the data directory's check takes for Loadline's own.
func TestDataDir_OwnsWhatTheServerAndTheTapWrite(t *testing.T) {
	for _, name := range []string{logName, logName + ".1", logName + ".2", ensureStampName, openStampName} {
		if !datadir.Owns(name) {
			t.Errorf("datadir.Owns(%q) = false, want true", name)
		}
	}
}
