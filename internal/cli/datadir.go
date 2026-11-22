package cli

import (
	"fmt"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// secureDataDir creates the data directory if need be and makes it, with
// everything in it, private to the user: it holds the ingest token, the
// binary every hook runs, the spool and the history, and LOADLINE_HOME can
// put it in a folder that lets other users in.
func secureDataDir(dir string) error {
	if err := fsperm.MkdirAll(dir); err != nil {
		return fmt.Errorf("create the data directory: %w", err)
	}
	return fsperm.Private(dir)
}
