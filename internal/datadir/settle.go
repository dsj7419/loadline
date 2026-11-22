package datadir

import (
	"time"

	"github.com/dsj7419/loadline/internal/fsperm"
)

const (
	// settleBudget bounds how long Loadline waits on one of its files that
	// Windows refuses to open while another Loadline process deletes it. A
	// deletion takes far less; the budget covers a scanner's brief hold.
	settleBudget = 500 * time.Millisecond
	// maxSettleDelay caps the pause between attempts.
	maxSettleDelay = 50 * time.Millisecond
)

// settle runs op, which reads or changes one of Loadline's files, and runs
// it again after a growing pause while it fails in a way that may pass,
// until settleBudget is spent.
//
// Windows refuses to open a file whose deletion is pending, until the last
// handle on it closes, with "access denied": a spooled event for a moment
// while a drain deletes it, an old log while a rotation replaces it, or the
// history's write-ahead log while the server closes it. Once such a file is
// gone, op fails with fs.ErrNotExist, and the caller leaves the file out. A
// refusal that outlasts the budget cannot be told from a file the user may
// not open, so op's error is returned as it is.
func settle(op func() error) error {
	deadline := time.Now().Add(settleBudget)
	delay := time.Millisecond
	for {
		err := op()
		if !fsperm.Transient(err) || !time.Now().Add(delay).Before(deadline) {
			return err
		}
		time.Sleep(delay)
		delay = min(2*delay, maxSettleDelay)
	}
}
