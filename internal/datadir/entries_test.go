package datadir_test

import (
	"testing"

	"github.com/dsj7419/loadline/internal/datadir"
)

func TestOwns_KnowsLoadlinesEntriesOnly(t *testing.T) {
	for _, name := range []string{
		"token", ".token-123456", "bin", "spool", "loadline.log", "loadline.log.1", "loadline.log.12",
		"loadline.db", "loadline.db-wal", "loadline.db-shm", "loadline.db-journal",
		"ensure.stamp", "opened.stamp", "config.json",
	} {
		if !datadir.Owns(name) {
			t.Errorf("Owns(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"shared", "notes.txt", "tokens", "token.bak", "loadline.log.x", "loadline.log.", "loadline.log.-1",
		"loadline.db.bak", "Bin", ".git",
	} {
		if datadir.Owns(name) {
			t.Errorf("Owns(%q) = true, want false", name)
		}
	}
}
