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

// existingDirs keeps claude from running for a config dir that is not on
// disk. claude creates the directory it is pointed at, and Loadline only
// observes: a mistyped account in the config, or a dir an event named that
// the user has since deleted, must stay as it is. It also holds the dirs
// dropped from polling for being missing, until an event names them again.
type existingDirs struct {
	failures *failureLog

	mu      sync.Mutex
	dropped map[domain.ConfigDir]bool
}

func newExistingDirs(failures *failureLog) *existingDirs {
	return &existingDirs{failures: failures, dropped: make(map[domain.ConfigDir]bool)}
}

// exists reports whether dir is a directory on disk, so that claude may run
// for it. One that is not is logged, at most once a minute.
func (c *existingDirs) exists(dir domain.ConfigDir) bool {
	info, err := os.Stat(string(dir))
	if err == nil && info.IsDir() {
		return true
	}
	if err == nil {
		err = errors.New("not a directory")
	}
	c.failures.note("config dir not found; claude is not run for it", string(dir), err)
	return false
}

// drop leaves dir out of the dirs to poll until an event names it.
func (c *existingDirs) drop(dir domain.ConfigDir) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropped[dir] = true
}

// named notes that an event named dir, which brings it back if dropped.
func (c *existingDirs) named(dir domain.ConfigDir) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.dropped, dir)
}

// known returns dirs without the dropped ones, in a slice of its own.
func (c *existingDirs) known(dirs []domain.ConfigDir) []domain.ConfigDir {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(dirs), func(dir domain.ConfigDir) bool { return c.dropped[dir] })
}
