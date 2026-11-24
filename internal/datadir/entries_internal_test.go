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
	"testing/synctest"
	"time"

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

// The token's own leftover temporary name excuses itself alone: a token with
// a third name, elsewhere, is refused for it. The walk comes to such a
// leftover before the token and refuses it there (judgeTemporary), so only
// judgeNames itself shows that the token is refused too.
func TestJudgeNames_RefusesANameElsewhereBesideTheTokensOwnLeftover(t *testing.T) {
	dir := t.TempDir()
	token, leftover := filepath.Join(dir, "token"), filepath.Join(dir, ".token-9")
	for _, path := range []string{token, leftover} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tokens := fsperm.FullFileID(7, [16]byte{0: 0x2a})
	inspectAs(t, map[string]fsperm.Entry{token: {Names: 3, ID: tokens}, leftover: {Names: 3, ID: tokens}})

	if _, err := judgeNames(token, false); !errors.Is(err, errMoreNames) {
		t.Errorf("judgeNames = %v; want the token's name elsewhere refused beside its own leftover", err)
	}
}

// The token's writer removes a name of the token's, or links one into place,
// whenever it likes: here the token's own name goes between the look at its
// temporary name, which found two names, and the count of them in its
// folder, which finds one. The count is no longer what the look saw, which
// tells of no name elsewhere: the temporary name is judged again, and taken
// as in place, neither refused nor left unjudged.
func TestMake_JudgesATemporaryNameAgainWhenItsNamesChangeMeanwhile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loadline")
	if err := fsperm.MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	token, temporary := filepath.Join(dir, "token"), filepath.Join(dir, ".token-7")
	if err := os.WriteFile(token, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
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
	// twin gives the file at from its second name, to, and arms the seam
	// again: the next look at the temporary name sees two names, of which
	// the count then finds one.
	twin := func(from, to string) {
		t.Helper()
		if err := os.Link(from, to); err != nil {
			t.Skipf("cannot make a hard link here: %v", err)
		}
		removed = false
	}
	twin(token, temporary)

	_, verdict, err := judgeTemporary(temporary)

	if err != nil || verdict != inPlace || !removed {
		t.Errorf("judgeTemporary = %s, %v (seam ran: %t); want %s once %s went", verdictName(verdict), err, removed, verdictName(inPlace), token)
	}

	twin(temporary, token)

	if err := Make(dir); err != nil {
		t.Errorf("Make = %v; want %s judged again once %s went, and taken", err, temporary, token)
	}
	if !removed {
		t.Errorf("nothing came between the look at %s and the count of its names, so the test proves nothing", temporary)
	}
}

// beyondSettle is longer than settle waits for a file: a walk held up that
// long, as on a loaded computer or while a scanner holds the folder, finds
// the budget spent when it comes to its next attempt. The tests that hold a
// walk up run in a synctest bubble, whose clock moves only while they wait.
const beyondSettle = settleBudget + 50*time.Millisecond

// tokenBeingPutInPlace makes a data directory whose token was just linked
// into place from its temporary file, as createToken in config does, and has
// the token's writer remove the temporary name, as it does next, between the
// walk's look at that name and its count of the file's names. The walk then
// does not get to go on until settle's budget is spent (beyondSettle).
// stalled reports that the walk came to that moment.
func tokenBeingPutInPlace(t *testing.T) (dir, temporary string, stalled *bool) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "loadline")
	if err := fsperm.MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	temporary = filepath.Join(dir, ".token-7")
	if err := os.WriteFile(temporary, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(temporary, filepath.Join(dir, "token")); err != nil {
		t.Skipf("cannot make a hard link here: %v", err)
	}
	stalled = new(bool)
	previous := betweenLookAndCount
	betweenLookAndCount = func(path string) {
		if path != temporary || *stalled {
			return
		}
		*stalled = true
		if err := os.Remove(temporary); err != nil {
			t.Errorf("remove %s: %v", temporary, err)
		}
		time.Sleep(beyondSettle)
	}
	t.Cleanup(func() { betweenLookAndCount = previous })
	return dir, temporary, stalled
}

// The token has two names for an instant alone: its writer links its
// temporary file into place, then removes the temporary name. A look at that
// name in between finds two names, and the count of them in the folder finds
// one once the name has gone, which is no verdict yet. However long that
// attempt took, the name is looked at again, and found gone, never taken for
// a hard link elsewhere.
func TestJudgeTemporary_LooksAgainAfterAShortCountWhateverTheTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, temporary, stalled := tokenBeingPutInPlace(t)

		_, verdict, err := judgeTemporary(temporary)

		if !*stalled {
			t.Fatal("nothing came between the look and the count, so the test proves nothing")
		}
		if err != nil || verdict != goneOrGoing {
			t.Errorf("judgeTemporary = %s, %v; want %s: its writer removed the name, and the file has no name elsewhere",
				verdictName(verdict), err, verdictName(goneOrGoing))
		}
	})
}

// Make, which creates the token on a hook's path, and Verify, install's
// first check, whose walk doctor's Check shares, take a data directory whose
// token is being put in place while their walk is held up.
func TestMake_TakesATokenBeingPutInPlaceWhileTheWalkIsHeldUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir, _, stalled := tokenBeingPutInPlace(t)

		err := Make(dir)

		if !*stalled {
			t.Fatal("nothing came between the look and the count, so the test proves nothing")
		}
		if err != nil {
			t.Errorf("Make = %v; want nil: the data directory holds the token alone, with no name elsewhere", err)
		}
	})
}

func TestVerify_TakesATokenBeingPutInPlaceWhileTheWalkIsHeldUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir, _, stalled := tokenBeingPutInPlace(t)

		err := Verify(dir, filepath.Dir(dir))

		if !*stalled {
			t.Fatal("nothing came between the look and the count, so the test proves nothing")
		}
		if err != nil {
			t.Errorf("Verify = %v; want nil: the data directory holds the token alone, with no name elsewhere", err)
		}
	})
}

// The same holds for the look at the token itself (judgeNames): it finds two
// names, the writer removes the temporary one before the walk looks for it
// beside the token, and the walk is held up until settle's budget is spent.
// The token is looked at again, and found with one name.
func TestJudge_LooksAgainAtTheTokenAfterAShortCountWhateverTheTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "loadline")
		if err := fsperm.MkdirAll(dir); err != nil {
			t.Fatal(err)
		}
		token, temporary := filepath.Join(dir, "token"), filepath.Join(dir, ".token-7")
		if err := os.WriteFile(temporary, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(temporary, token); err != nil {
			t.Skipf("cannot make a hard link here: %v", err)
		}
		stalled := false
		lookAs(t, token, func() (fsperm.Entry, error) {
			entry, err := fsperm.Inspect(token)
			if !stalled {
				stalled = true
				if err := os.Remove(temporary); err != nil {
					t.Errorf("remove %s: %v", temporary, err)
				}
				time.Sleep(beyondSettle)
			}
			return entry, err
		})
		var found tree

		_, judged, err := found.judge(token, false)

		if !stalled {
			t.Fatal("nothing came between the look and the count, so the test proves nothing")
		}
		if err != nil || !judged || found.link != "" {
			t.Errorf("judge = judged %t, %v, stopped at %q (another name: %t); want the token taken, with its one name",
				judged, err, found.link, found.moreNames)
		}
	})
}

// A name elsewhere lasts: the attempt after the one that found it finds it
// too, and the temporary name is refused as a hard link, however long each
// attempt takes, and after no more attempts than that.
func TestJudgeTemporary_RefusesANameElsewhereHoweverLongEachAttemptTakes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "loadline")
		if err := fsperm.MkdirAll(dir); err != nil {
			t.Fatal(err)
		}
		temporary := filepath.Join(dir, ".token-7")
		if err := os.WriteFile(temporary, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		elsewhere := filepath.Join(filepath.Dir(dir), "elsewhere")
		if err := os.Link(temporary, elsewhere); err != nil {
			t.Skipf("cannot make a hard link here: %v", err)
		}
		attempts := 0
		previous := betweenLookAndCount
		betweenLookAndCount = func(string) {
			attempts++
			if attempts == 3 {
				// Ends a walk that would otherwise look again for ever.
				if err := os.Remove(elsewhere); err != nil {
					t.Errorf("remove %s: %v", elsewhere, err)
				}
			}
			time.Sleep(beyondSettle)
		}
		t.Cleanup(func() { betweenLookAndCount = previous })

		_, verdict, err := judgeTemporary(temporary)

		if err != nil || verdict != linkedElsewhere || attempts != 2 {
			t.Errorf("judgeTemporary = %s, %v after %d attempts; want %s after 2, the second finding the name elsewhere again",
				verdictName(verdict), err, attempts, verdictName(linkedElsewhere))
		}
	})
}

// verdictName names verdict as the code does.
func verdictName(verdict temporaryVerdict) string {
	names := map[temporaryVerdict]string{
		inPlace: "inPlace", linkedElsewhere: "linkedElsewhere", goneOrGoing: "goneOrGoing", aLink: "aLink",
	}
	if name, ok := names[verdict]; ok {
		return name
	}
	return fmt.Sprintf("no verdict (%d)", verdict)
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
					t.Errorf("judgeTemporary = %s, %v; want the look's error", verdictName(verdict), err)
				}
				return
			}
			if err != nil || verdict != tt.want {
				t.Errorf("judgeTemporary = %s, %v; want %s", verdictName(verdict), err, verdictName(tt.want))
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
