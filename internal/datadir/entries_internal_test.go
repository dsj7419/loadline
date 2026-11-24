package datadir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// The token's writer removes a name of the token's, or links one into place,
// whenever it likes: here the token's own name goes between the look at its
// temporary name, which found two names, and the count of them in its
// folder, which finds one. The count is no longer what the look saw, which
// tells of no name elsewhere: the temporary name is judged again, and taken.
func TestMake_JudgesATemporaryNameAgainWhenItsNamesChangeMeanwhile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loadline")
	if err := fsperm.MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	token, temporary := filepath.Join(dir, "token"), filepath.Join(dir, ".token-7")
	if err := os.WriteFile(token, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(token, temporary); err != nil {
		t.Skipf("cannot make a hard link here: %v", err)
	}
	removed := false
	previous := betweenLookAndCount
	betweenLookAndCount = func(path string) {
		if path != temporary || removed {
			return
		}
		removed = true
		if err := os.Remove(token); err != nil {
			t.Errorf("remove %s: %v", token, err)
		}
	}
	t.Cleanup(func() { betweenLookAndCount = previous })

	if err := Make(dir); err != nil {
		t.Errorf("Make = %v; want %s judged again once %s went, and taken", err, temporary, token)
	}
	if !removed {
		t.Errorf("nothing came between the look at %s and the count of its names, so the test proves nothing", temporary)
	}
}

// lookAs makes the walk take path for what look returns, for the rest of the
// test, and every other path for what it is.
func lookAs(t *testing.T, path string, look func() (fsperm.Entry, error)) {
	t.Helper()
	previous := inspect
	inspect = func(p string) (fsperm.Entry, error) {
		if p == path {
			return look()
		}
		return previous(p)
	}
	t.Cleanup(func() { inspect = previous })
}

// Its folder's listing gives a temporary name as a regular file, but what
// the walk then finds under the name may be something else: a link or a
// folder put in its place meanwhile, which no writer makes, or nothing. A
// link is refused as a link. A folder, or nothing, goes unjudged, and is
// left for the next run. A look that fails for any other reason fails.
func TestJudgeTemporary_TellsWhatItFindsUnderTheName(t *testing.T) {
	for _, tt := range []struct {
		name    string
		look    fsperm.Entry
		lookErr error
		// fails is set when judgeTemporary is to return the look's error.
		fails bool
		want  temporaryVerdict
	}{
		{name: "a file with that name alone", look: fsperm.Entry{Names: 1}, want: inPlace},
		{name: "a link", look: fsperm.Entry{Link: true, Reparse: fsperm.NameSurrogate, Names: 1}, want: aLink},
		{name: "a folder", look: fsperm.Entry{Folder: true, Names: 1}, want: goneOrGoing},
		{name: "nothing", lookErr: fs.ErrNotExist, want: goneOrGoing},
		{name: "a look that fails", lookErr: errors.New("the device is not ready"), fails: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".token-1")
			lookAs(t, path, func() (fsperm.Entry, error) {
				if tt.lookErr != nil {
					return fsperm.Entry{}, fmt.Errorf("read %s: %w", path, tt.lookErr)
				}
				return tt.look, nil
			})

			_, verdict, err := judgeTemporary(path)

			if tt.fails {
				if !errors.Is(err, tt.lookErr) {
					t.Errorf("judgeTemporary = %d, %v; want the look's error", verdict, err)
				}
				return
			}
			if err != nil || verdict != tt.want {
				t.Errorf("judgeTemporary = %d, %v; want %d", verdict, err, tt.want)
			}
		})
	}
}

// A link found under a temporary name is refused as any link among
// Loadline's paths is.
func TestMake_RefusesALinkFoundUnderATemporaryName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loadline")
	path := filepath.Join(dir, ".token-1")
	if err := fsperm.MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookAs(t, path, func() (fsperm.Entry, error) {
		return fsperm.Entry{Link: true, Reparse: fsperm.NameSurrogate, Names: 1}, nil
	})

	if err := Make(dir); err == nil || !strings.HasPrefix(err.Error(), path+" is a link") {
		t.Errorf("Make = %v, want it refused, starting %q", err, path+" is a link")
	}
}
