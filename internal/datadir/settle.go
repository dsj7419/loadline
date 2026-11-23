package datadir

import (
	"errors"
	"time"

	"github.com/dsj7419/loadline/internal/fsperm"
)

const (
	// settleBudget bounds how long Loadline waits on one of its files that
	// Windows refuses to open while another Loadline process deletes it. A
	// deletion takes far less; the budget covers a scanner's brief hold.
	settleBudget = 500 * time.Millisecond
	// leftoverBudget bounds how long Make tries to remove a temporary name
	// left beside one of Loadline's files while something holds it. What it
	// cannot remove in that time it leaves for a later run.
	leftoverBudget = 100 * time.Millisecond
	// maxSettleDelay caps the pause between attempts.
	maxSettleDelay = 50 * time.Millisecond
)

// errMoreNames is how a look at one of Loadline's files says that it has a
// name besides its own and its own temporary ones. The token has another
// for a moment: another caller has linked it into place from its temporary
// file, and the look came between the link and that name's removal.
var errMoreNames = errors.New("the file has more than one name")

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
// not open, so op's error is returned as it is. A second name that outlasts
// it is no token being put in place (errMoreNames).
func settle(op func() error) error {
	return settleWithin(settleBudget, op)
}

// settleWithin is settle with budget in place of settleBudget.
func settleWithin(budget time.Duration, op func() error) error {
	deadline := time.Now().Add(budget)
	delay := time.Millisecond
	for {
		err := op()
		mayPass := fsperm.Transient(err) || errors.Is(err, errMoreNames)
		if !mayPass || !time.Now().Add(delay).Before(deadline) {
			return err
		}
		time.Sleep(delay)
		delay = min(2*delay, maxSettleDelay)
	}
}
