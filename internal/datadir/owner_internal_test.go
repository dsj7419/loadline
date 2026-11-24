package datadir

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// ownedBy makes path, for the rest of the test, read as owned by owner: an
// account that is not elevated, or not root, cannot give a file another
// owner.
func ownedBy(t *testing.T, path string, owner fsperm.Owner) {
	t.Helper()
	previous := foreignOwner
	foreignOwner = func(p string) (fsperm.Owner, error) {
		if p == path {
			return owner, nil
		}
		return previous(p)
	}
	t.Cleanup(func() { foreignOwner = previous })
}

// ownedByStranger makes path, for the rest of the test, read as owned by
// someone else.
func ownedByStranger(t *testing.T, path string) {
	t.Helper()
	ownedBy(t, path, fsperm.Owner{Name: "a stranger"})
}

// home returns a data directory that holds the token, a spooled event, the
// installed binary and, being written, a token's temporary file.
func home(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "loadline")
	for _, name := range []string{"token", filepath.Join("spool", "1-0000000a.json"), filepath.Join("bin", "loadline"), ".token-1234"} {
		path := filepath.Join(dir, name)
		if err := fsperm.MkdirAll(filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Whoever owns one of Loadline's files or folders may always let anyone at
// it, whatever Loadline makes of its access list. Install, the server and
// the creation of the token refuse it, and doctor reports it.
func TestMake_RefusesAFileOfLoadlinesThatBelongsToSomeoneElse(t *testing.T) {
	for _, name := range []string{"token", filepath.Join("spool", "1-0000000a.json"), "bin"} {
		t.Run(name, func(t *testing.T) {
			dir := home(t)
			path := filepath.Join(dir, name)
			ownedByStranger(t, path)
			want := path + " belongs to a stranger"

			for step, err := range map[string]error{"Make": Make(dir), "Secure": Secure(dir, filepath.Dir(dir))} {
				if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.HasSuffix(err.Error(), "run loadline install") {
					t.Errorf("%s = %v; want it refused, starting %q", step, err, want)
				}
			}
			if got, err := Check(dir, filepath.Dir(dir)); err != nil || !strings.HasPrefix(got, want) {
				t.Errorf("Check = %q, %v; want it to report %q", got, err, want)
			}
		})
	}
}

// A file another process is writing is its own, whoever owns it.
func TestMake_DoesNotJudgeTheOwnerOfAFileBeingWritten(t *testing.T) {
	dir := home(t)
	ownedByStranger(t, filepath.Join(dir, ".token-1234"))

	if err := Make(dir); err != nil {
		t.Errorf("Make = %v, want nil", err)
	}
}

// A file the system leaves, in the data directory, is judged by its owner
// as Loadline's own are.
func TestMake_RefusesASystemFileThatBelongsToSomeoneElse(t *testing.T) {
	dir := home(t)
	ini := filepath.Join(dir, "desktop.ini")
	if err := os.WriteFile(ini, []byte("[.ShellClassInfo]\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ownedByStranger(t, ini)
	want := ini + " belongs to a stranger"

	if err := Make(dir); err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Make = %v; want it refused, starting %q", err, want)
	}
	if got, err := Check(dir, filepath.Dir(dir)); err != nil || !strings.HasPrefix(got, want) {
		t.Errorf("Check = %q, %v; want it to report %q", got, err, want)
	}
}

// goneSID is an invented SID of an account this computer no longer has, and
// gone that account as fsperm.ForeignOwner tells it.
const goneSID = "S-1-5-21-111-222-333-4242"

var gone = fsperm.Owner{Name: goneSID, Gone: true}

// takeBackLead starts the offer to take a folder back, which ends either in
// the command or in the tool's name.
const takeBackLead = "; it belongs to an account this computer no longer has, as after a profile was moved or restored: if the folder is yours, "

// profileHolds makes the profile hold folders alone, for the rest of the
// test: the tests' own folders are wherever their temporary folder is.
func profileHolds(t *testing.T, folders ...string) {
	t.Helper()
	inProfileAnswers(t, func(path string) (bool, error) { return slices.Contains(folders, path), nil })
}

// inProfileAnswers makes inProfile answer with answer, for the rest of the
// test.
func inProfileAnswers(t *testing.T, answer func(string) (bool, error)) {
	t.Helper()
	previous := inProfile
	inProfile = answer
	t.Cleanup(func() { inProfile = previous })
}

// movableAs makes the check of the folders above every data directory find
// found, for the rest of the test.
func movableAs(t *testing.T, found fsperm.Movable) {
	t.Helper()
	previous := movableAncestor
	movableAncestor = func(string, string) (fsperm.Movable, error) { return found, nil }
	t.Cleanup(func() { movableAncestor = previous })
}

// ownedFolder is what fsperm.MovableAncestor finds of folder when owner owns
// it, in its words.
func ownedFolder(folder string, owner fsperm.Owner) fsperm.Movable {
	return fsperm.Movable{
		Folder:  folder,
		Problem: folder + " belongs to " + owner.Name + ", who can give themselves the right to delete or rename it, or what it holds",
		Owner:   owner,
	}
}

// ownFileReport is the report of path, one of Loadline's files, that owner
// owns, as it was before any folder was offered to be taken back.
func ownFileReport(path string, owner fsperm.Owner) string {
	return path + " belongs to " + owner.Name + ", who can always change who may read or replace it, " +
		"which Loadline's own files must not allow: remove it, then run loadline install"
}

// folderReport is the report of what the check of the folders above the
// data directory found, as it was before any folder was offered to be taken
// back.
func folderReport(found fsperm.Movable) string {
	return found.Problem + ", and with it replace the binary every hook runs; " +
		"set LOADLINE_HOME to a folder in your user profile, or take that right away"
}

// A profile that was moved or restored can leave Loadline's files owned by
// the account it was moved from, which this computer no longer has. The data
// directory is in the profile, so the report offers the command that takes
// it back, with everything in it.
func TestOwnerProblem_OffersToTakeBackAFolderOfAGoneAccountInTheProfile(t *testing.T) {
	for _, name := range []string{"token", filepath.Join("spool", "1-0000000a.json"), "bin"} {
		t.Run(name, func(t *testing.T) {
			dir := home(t)
			path := filepath.Join(dir, name)
			ownedBy(t, path, gone)
			profileHolds(t, dir)
			want := ownFileReport(path, gone) + takeBackLead + `run takeown /f "` + dir + `" /r and then loadline install`

			for step, err := range map[string]error{"Make": Make(dir), "Secure": Secure(dir, filepath.Dir(dir))} {
				if err == nil || err.Error() != want {
					t.Errorf("%s = %v;\nwant %s", step, err, want)
				}
			}
			if got, err := Check(dir, filepath.Dir(dir)); err != nil || got != want {
				t.Errorf("Check = %q, %v;\nwant %q", got, err, want)
			}
		})
	}
}

// The dashboard's browser profile is one of Loadline's folders in the data
// directory, and the browser's launch judges its owner as Make does, in the
// same words, the offer to take the data directory back included.
func TestPrepareBrowserProfile_OffersToTakeBackAProfileOfAGoneAccount(t *testing.T) {
	dir := home(t)
	profile := filepath.Join(dir, browserProfile)
	if err := fsperm.MkdirAll(profile); err != nil {
		t.Fatal(err)
	}
	ownedBy(t, profile, gone)
	profileHolds(t, dir)
	want := ownFileReport(profile, gone) + takeBackLead + `run takeown /f "` + dir + `" /r and then loadline install`

	for step, err := range map[string]error{"PrepareBrowserProfile": PrepareBrowserProfile(profile), "Make": Make(dir)} {
		if err == nil || err.Error() != want {
			t.Errorf("%s = %v;\nwant %s", step, err, want)
		}
	}
}

// A folder above the data directory, or the data directory itself, that an
// account this computer no longer has owns, in the profile: the report
// offers to take it back. Only the data directory is taken back with what it
// holds, which is Loadline's: what a folder above holds is the user's own and
// others', which is no business of Loadline's.
func TestMovable_OffersToTakeBackAnAncestorOfAGoneAccount(t *testing.T) {
	dir := home(t)
	above := filepath.Dir(dir)
	// A data directory that LOADLINE_HOME names as Loadline names one of its
	// own folders, alone in the folder above it, which then holds nothing
	// that is not Loadline's by its name.
	spool := filepath.Join(t.TempDir(), "above", "spool")
	if err := fsperm.MkdirAll(spool); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, dir, folder, command string }{
		{"a folder above it", dir, above, `takeown /f "` + above + `"`},
		{"a folder above it, holding only what Loadline names its own", spool, filepath.Dir(spool), `takeown /f "` + filepath.Dir(spool) + `"`},
		{"the data directory itself", dir, dir, `takeown /f "` + dir + `" /r`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			found := ownedFolder(tt.folder, gone)
			movableAs(t, found)
			profileHolds(t, tt.folder)
			top := filepath.Dir(filepath.Dir(tt.dir))
			want := folderReport(found) + takeBackLead + "run " + tt.command + " and then loadline install"

			if err := Verify(tt.dir, top); err == nil || err.Error() != want {
				t.Errorf("Verify = %v;\nwant %s", err, want)
			}
			if got, err := Check(tt.dir, top); err != nil || got != want {
				t.Errorf("Check = %q, %v;\nwant %q", got, err, want)
			}
		})
	}
}

// takeown /r takes everything below the folder it is given, and through a
// file's other name, the file wherever else it is. So the data directory is
// taken back with what it holds only while that is Loadline's alone, and no
// file of it has another name.
func TestMovable_TakesBackOnlyTheDataDirectoryWhenItHoldsMore(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, dir string){
		"a file that is not Loadline's": func(t *testing.T, dir string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a file of Loadline's with another name": func(t *testing.T, dir string) {
			t.Helper()
			if err := os.Link(filepath.Join(dir, "token"), filepath.Join(t.TempDir(), "elsewhere")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := home(t)
			plant(t, dir)
			found := ownedFolder(dir, gone)
			movableAs(t, found)
			profileHolds(t, dir)
			want := folderReport(found) + takeBackLead + `run takeown /f "` + dir + `" and then loadline install`

			if err := Verify(dir, filepath.Dir(dir)); err == nil || err.Error() != want {
				t.Errorf("Verify = %v;\nwant %s", err, want)
			}
		})
	}
}

// assertNeverOffered checks that no report offers to take a folder back
// that owner owns, while the profile is as inProfileAnswer says: each is the
// report it was before any was offered, byte for byte. It tries one of
// Loadline's files, the data directory itself, and a folder above it.
func assertNeverOffered(t *testing.T, owner fsperm.Owner, inProfileAnswer func(string) (bool, error)) {
	t.Helper()
	t.Run("one of Loadline's files", func(t *testing.T) {
		dir := home(t)
		token := filepath.Join(dir, "token")
		ownedBy(t, token, owner)
		inProfileAnswers(t, inProfileAnswer)
		want := ownFileReport(token, owner)

		for step, err := range map[string]error{"Make": Make(dir), "Secure": Secure(dir, filepath.Dir(dir))} {
			if err == nil || err.Error() != want {
				t.Errorf("%s = %v;\nwant %s", step, err, want)
			}
		}
		if got, err := Check(dir, filepath.Dir(dir)); err != nil || got != want {
			t.Errorf("Check = %q, %v;\nwant %q", got, err, want)
		}
	})
	for _, where := range []string{"the data directory", "a folder above it"} {
		t.Run(where, func(t *testing.T) {
			dir := home(t)
			folder := map[string]string{"the data directory": dir, "a folder above it": filepath.Dir(dir)}[where]
			found := ownedFolder(folder, owner)
			movableAs(t, found)
			inProfileAnswers(t, inProfileAnswer)
			want := folderReport(found)

			if err := Verify(dir, filepath.Dir(folder)); err == nil || err.Error() != want {
				t.Errorf("Verify = %v;\nwant %s", err, want)
			}
			if got, err := Check(dir, filepath.Dir(folder)); err != nil || got != want {
				t.Errorf("Check = %q, %v;\nwant %q", got, err, want)
			}
		})
	}
}

// everywhere puts every folder in the profile.
func everywhere(string) (bool, error) { return true, nil }

// An account this computer cannot name right now, as one of a domain whose
// controller does not answer, may still sign in, and own the folder
// rightly: it is never offered to be taken from it.
func TestOwnerProblem_NeverOffersItForAnAccountItCannotNameRightNow(t *testing.T) {
	assertNeverOffered(t, fsperm.Owner{Name: "S-1-5-21-1-2-3-4242 (an account this computer cannot name right now)"}, everywhere)
}

// A group, or any account this computer can name, exists, and may own the
// folder rightly.
func TestOwnerProblem_NeverOffersItForALiveAccount(t *testing.T) {
	assertNeverOffered(t, fsperm.Owner{Name: `BUILTIN\Guests`}, everywhere)
}

// A folder outside the profile may be someone else's, whoever owned it, as
// may one that cannot be placed.
func TestOwnerProblem_NeverOffersItOutsideTheProfile(t *testing.T) {
	t.Run("outside", func(t *testing.T) {
		assertNeverOffered(t, gone, func(string) (bool, error) { return false, nil })
	})
	t.Run("cannot be placed", func(t *testing.T) {
		assertNeverOffered(t, gone, func(string) (bool, error) { return true, errors.New("the profile cannot be found") })
	})
}

// The command is pasted into Command Prompt or PowerShell, which take a path
// in double quotes as it is, spaces, &, ' and letters of any script among it,
// but for a few characters. A path that holds one of those is given no
// command, but the tool's name.
func TestTakeBackAdvice_QuotesThePathOrNamesTheTool(t *testing.T) {
	quoted := func(path string, recursive bool) string {
		command := `takeown /f "` + path + `"`
		if recursive {
			command += " /r"
		}
		return takeBackLead + "run " + command + " and then loadline install"
	}
	named := func(path string, recursive bool) string {
		what := path
		if recursive {
			what += " and all it holds"
		}
		return takeBackLead + "take ownership of " + what + " with takeown and then run loadline install"
	}
	for _, tt := range []struct {
		name, path string
		want       func(path string, recursive bool) string
	}{
		{"spaces", `C:\Users\Jo Smith\AppData\Local\loadline`, quoted},
		{"&", `C:\Users\R&D\AppData\Local\loadline`, quoted},
		{"'", `C:\Users\O'Brien\AppData\Local\loadline`, quoted},
		{"non-ASCII", `C:\Users\Zoë Ōta 山田\AppData\Local\loadline`, quoted},
		{"%", `C:\Users\100%\AppData\Local\loadline`, named},
		{"$", `C:\Users\Dan$\AppData\Local\loadline`, named},
		{"!", `C:\Users\Hey!\AppData\Local\loadline`, named},
		{`"`, `C:\Users\a"b\AppData\Local\loadline`, named},
		{"a backtick", "C:\\Users\\a`b\\AppData\\Local\\loadline", named},
		{"a typographic double quote", "C:\\Users\\a\u201cb\u201d\\AppData\\Local\\loadline", named},
		{"a low typographic double quote", "C:\\Users\\a\u201eb\\AppData\\Local\\loadline", named},
	} {
		for _, recursive := range []bool{false, true} {
			if got, want := takeBackAdvice(tt.path, recursive), tt.want(tt.path, recursive); got != want {
				t.Errorf("takeBackAdvice(%s, %t), with %s = %q;\nwant %q", tt.path, recursive, tt.name, got, want)
			}
		}
	}
}
