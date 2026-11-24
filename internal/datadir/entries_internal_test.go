package datadir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// inspectAs makes the walk take each path in seen for the entry it maps to,
// for the rest of the test, and look at no other path. A change to seen
// shows from the next look on.
func inspectAs(t *testing.T, seen map[string]fsperm.Entry) {
	t.Helper()
	previous := inspect
	inspect = func(path string) (fsperm.Entry, error) {
		entry, ok := seen[path]
		if !ok {
			t.Errorf("looked at %s, which the test does not expect", path)
			return fsperm.Entry{}, fmt.Errorf("read %s: %w", path, fs.ErrNotExist)
		}
		return entry, nil
	}
	t.Cleanup(func() { inspect = previous })
}

// On ReFS two files in one folder can have IDs that differ only above their
// low 64 bits. Another writer's temporary file beside the token is then no
// name of the token's, whatever their IDs share: it is not taken for the
// token's leftover, so it is not removed, and it does not excuse a name the
// token has elsewhere.
func TestLeftovers_DoNotMatchAnotherFilesTemporaryNameThatSharesTheLow64Bits(t *testing.T) {
	dir := t.TempDir()
	token, writing := filepath.Join(dir, "token"), filepath.Join(dir, ".token-9")
	for _, path := range []string{token, writing} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tokens := fsperm.FullFileID(7, [16]byte{0: 0x2a, 8: 1})
	anothers := fsperm.FullFileID(7, [16]byte{0: 0x2a, 8: 2})
	// The token has a second name, which is not writing.
	seen := map[string]fsperm.Entry{token: {Names: 2, ID: tokens}, writing: {Names: 1, ID: anothers}}
	inspectAs(t, seen)

	if own, err := leftovers(token, tokens); err != nil || len(own) != 0 {
		t.Errorf("leftovers = %q, %v; want none: %s is another file", own, err, writing)
	}
	for _, tidy := range []bool{false, true} {
		if _, err := judgeNames(token, tidy); !errors.Is(err, errMoreNames) {
			t.Errorf("judgeNames(tidy: %t) = %v; want the token's second name refused", tidy, err)
		}
	}
	if _, err := os.Lstat(writing); err != nil {
		t.Errorf("%s, another writer's file, is gone (%v)", writing, err)
	}

	// The same name with the token's own ID is its leftover, and is the
	// second name: the whole ID decides.
	seen[writing] = fsperm.Entry{Names: 2, ID: tokens}
	if own, err := leftovers(token, tokens); err != nil || !slices.Equal(own, []string{writing}) {
		t.Errorf("leftovers = %q, %v; want %s, the token's own", own, err, writing)
	}
	if _, err := judgeNames(token, false); err != nil {
		t.Errorf("judgeNames = %v; want the token, beside its own leftover, taken", err)
	}
}
