package config_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// Two callers, step by step, as the concurrent test only meets them now and
// then: the first has written its token to its temporary file and not yet
// linked it into place when the second prepares the directory and
// publishes a token of its own. The second leaves the first's file as it
// is, private as the directory made it, which its list shows: the private
// one with its entries in another order, which a rewrite would change. The
// first then takes the token the second published.
func TestEnsureToken_LeavesTheFileAnotherCallerIsWritingAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "token")
	var second []byte
	var before, after fspermtest.List
	calls := 0
	config.SetBeforePublish(t, func(temp string) {
		calls++
		if calls > 1 {
			return
		}
		before = fspermtest.PrivateInAnotherOrder(t, temp)
		second = ensureToken(t, path)
		after = fspermtest.AccessList(t, temp)
	})

	first := ensureToken(t, path)

	if !after.Equal(before) {
		t.Errorf("the second caller changed the access list of the first's file from %s to %s", before, after)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("the first caller got %q, the second %q; want one token", first, second)
	}
}
