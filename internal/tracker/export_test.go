package tracker

import "github.com/dsj7419/loadline/internal/domain"

// CallsHeld exposes how many model calls the tracker holds for the credit
// estimates of dir's account, so the external tests can pin the bound.
func CallsHeld(t *Tracker, dir domain.ConfigDir) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if entry, ok := t.accounts[dir]; ok {
		return len(entry.credits.calls)
	}
	return 0
}
