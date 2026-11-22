package cli

import (
	"github.com/dsj7419/loadline/internal/datadir"
)

// trustFrom is "" in Loadline itself, so that the check that nobody else can
// move the data directory aside goes up to the volume root. Tests set it to
// the temporary folder they work in.
var trustFrom string

// secureDataDir prepares the data directory as datadir.Secure does: it
// holds the ingest token, the binary every hook runs, the spool and the
// history, and LOADLINE_HOME can put it anywhere.
func secureDataDir(dir string) error {
	return datadir.Secure(dir, trustFrom)
}
