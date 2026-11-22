package cli

import (
	"errors"
	"os"
	"slices"
	"sync"

	"github.com/dsj7419/loadline/internal/domain"
)

// errConfigDirMissing is what a probe of a config dir that is not on disk
// reports.
var errConfigDirMissing = errors.New("the config dir does not exist")

// existingDirs keeps claude from running for a config dir that is not on disk.
// claude creates the directory it is pointed at, and Loadline only observes:
// a mistyped account in the config, or a dir an event named that the user
// has since deleted, must stay as it is. A missing dir lists no sessions, as
// claude would find none there. Once none of its sessions is live it is
// dropped, and neither polled nor probed again until an event names it.
type existingDirs struct {
	// hasLive reports whether any session of a dir is live.
	hasLive func(domain.ConfigDir) bool
	// dropped is called with each dir as it is dropped.
	dropped  func(domain.ConfigDir)
	failures *failureLog

	mu   sync.Mutex
	gone map[domain.ConfigDir]bool
}

func newExistingDirs(failures *failureLog) *existingDirs {
	return &existingDirs{failures: failures, gone: make(map[domain.ConfigDir]bool)}
}

// present reports whether dir is a directory on disk, so that claude may run
// for it. One that is not is logged, at most once a minute, and dropped
// unless a session of it is live.
func (c *existingDirs) present(dir domain.ConfigDir) bool {
	info, err := os.Stat(string(dir))
	if err == nil && info.IsDir() {
		return true
	}
	if err == nil {
		err = errors.New("not a directory")
	}
	c.failures.note("config dir not found; claude is not run for it", string(dir), err)
	if !c.hasLive(dir) {
		c.drop(dir)
	}
	return false
}

func (c *existingDirs) drop(dir domain.ConfigDir) {
	c.mu.Lock()
	first := !c.gone[dir]
	c.gone[dir] = true
	c.mu.Unlock()
	if first {
		c.dropped(dir)
	}
}

// named notes that an event named dir, which brings it back if dropped.
func (c *existingDirs) named(dir domain.ConfigDir) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.gone, dir)
}

// known returns dirs without the dropped ones, in a slice of its own.
func (c *existingDirs) known(dirs []domain.ConfigDir) []domain.ConfigDir {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(dirs), func(dir domain.ConfigDir) bool { return c.gone[dir] })
}
