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
	return homeAt(t, filepath.Join(t.TempDir(), "loadline"))
}

// homeAt makes dir a data directory as home does, and returns it.
func homeAt(t *testing.T, dir string) string {
	t.Helper()
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
// it, whatever Loadline makes of its access list, a file another process is
// writing among them. Install, its first check, the server and the creation
// of the token refuse it, and doctor reports it.
func TestMake_RefusesAFileOfLoadlinesThatBelongsToSomeoneElse(t *testing.T) {
	for _, name := range []string{"token", filepath.Join("spool", "1-0000000a.json"), "bin", ".token-1234"} {
		t.Run(name, func(t *testing.T) {
			dir := home(t)
			path := filepath.Join(dir, name)
			ownedByStranger(t, path)
			want := path + " belongs to a stranger"

			for step, err := range map[string]error{
				"Verify": Verify(dir, filepath.Dir(dir)), "Make": Make(dir), "Secure": Secure(dir, filepath.Dir(dir)),
			} {
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
// the command or in the tool's name, and fileLead the offer to take a file
// back.
const (
	takeBackLead = "; it belongs to an account this computer no longer has, as after a profile was moved or restored: if the folder is yours, "
	fileLead     = "; it belongs to an account this computer no longer has, as after a profile was moved or restored: if the file is yours, "
)

// offered is the offer of the command that takes path, a folder, back, with
// all it holds when recursive is set.
func offered(path string, recursive bool) string {
	command := `takeown /f "` + path + `"`
	if recursive {
		command += " /r"
	}
	return takeBackLead + "run " + command + " and then loadline install"
}

// offeredFile is the offer of the command that takes path, a file, back.
func offeredFile(path string) string {
	return fileLead + `run takeown /f "` + path + `" and then loadline install`
}

// onDisk is path as the system resolves it, every link followed and each
// short name in full, which the command that takes a folder back names, and
// which the profile is asked about. A test's temporary folder can hold a
// short name, as it does on CI.
func onDisk(t *testing.T, path string) string {
	t.Helper()
	held, err := fsperm.Resolved(path)
	if err != nil {
		t.Fatal(err)
	}
	return held
}

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

// writeAt writes path, holding a byte, and the folders above it that are
// missing.
func writeAt(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// linkOutOf gives the file at elsewhere, outside the data directory, the
// name path in it too, a hard link, as anyone who could write to the data
// directory could.
func linkOutOf(t *testing.T, path string) {
	t.Helper()
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	writeAt(t, elsewhere)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(elsewhere, path); err != nil {
		t.Fatal(err)
	}
}

// A profile that was moved or restored can leave Loadline's files owned by
// the account it was moved from, which this computer no longer has, a file
// another process is writing among them. The data directory is in the
// profile, so the report offers the command that takes it back, with
// everything in it.
func TestOwnerProblem_OffersToTakeBackAFolderOfAGoneAccountInTheProfile(t *testing.T) {
	for _, name := range []string{"token", filepath.Join("spool", "1-0000000a.json"), "bin", ".token-1234"} {
		t.Run(name, func(t *testing.T) {
			dir := home(t)
			path := filepath.Join(dir, name)
			ownedBy(t, path, gone)
			profileHolds(t, onDisk(t, dir))
			want := ownFileReport(path, gone) + offered(onDisk(t, dir), true)

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
	profileHolds(t, onDisk(t, dir))
	want := ownFileReport(profile, gone) + offered(onDisk(t, dir), true)

	for step, err := range map[string]error{"PrepareBrowserProfile": PrepareBrowserProfile(profile), "Make": Make(dir)} {
		if err == nil || err.Error() != want {
			t.Errorf("%s = %v;\nwant %s", step, err, want)
		}
	}
}

// The walk of Loadline's own files judges each temporary name by its names
// before any owner is judged. One for a file elsewhere is refused as the
// hard link it is, and nothing is offered: there is nothing to take back
// until it is gone. The token beside its own leftover temporary name is in
// place, and every name of it is in the data directory, so the data
// directory is offered with all it holds.
func TestOwnerProblem_AgreesWithTheJudgementOfTemporaryNames(t *testing.T) {
	for _, tt := range []struct {
		name  string
		plant func(t *testing.T, dir string) (want string)
	}{
		{
			name: "a temporary name for a file elsewhere",
			plant: func(t *testing.T, dir string) string {
				t.Helper()
				put := filepath.Join(dir, "spool", ".put-1.tmp")
				linkOutOf(t, put)
				return moreNamesProblem(put)
			},
		},
		{
			name: "the token beside its own leftover temporary name",
			plant: func(t *testing.T, dir string) string {
				t.Helper()
				token := filepath.Join(dir, "token")
				if err := os.Link(token, filepath.Join(dir, ".token-5678")); err != nil {
					t.Fatal(err)
				}
				return ownFileReport(token, gone) + offered(onDisk(t, dir), true)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := home(t)
			ownedBy(t, filepath.Join(dir, "token"), gone)
			profileHolds(t, onDisk(t, dir))
			want := tt.plant(t, dir)

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

// When takeown /r could reach beyond the data directory, the data directory
// is not offered, and what the report names is offered alone: that is what
// a gone account owns, and taking the data directory without what it holds
// would change nothing.
func TestOwnerProblem_OffersWhatItReportsAloneWhenTheDataDirectoryHoldsMore(t *testing.T) {
	t.Run("one of Loadline's files, beside a file in the browser profile with a name elsewhere", func(t *testing.T) {
		dir := home(t)
		token := filepath.Join(dir, "token")
		ownedBy(t, token, gone)
		linkOutOf(t, filepath.Join(dir, browserProfile, "Default", "Cookies"))
		profileHolds(t, onDisk(t, token))
		want := ownFileReport(token, gone) + offeredFile(onDisk(t, token))

		for step, err := range map[string]error{"Make": Make(dir), "Secure": Secure(dir, filepath.Dir(dir))} {
			if err == nil || err.Error() != want {
				t.Errorf("%s = %v;\nwant %s", step, err, want)
			}
		}
		if got, err := Check(dir, filepath.Dir(dir)); err != nil || got != want {
			t.Errorf("Check = %q, %v;\nwant %q", got, err, want)
		}
	})
	t.Run("the browser profile, beside what is not Loadline's", func(t *testing.T) {
		dir := home(t)
		writeAt(t, filepath.Join(dir, "notes.txt"))
		profile := filepath.Join(dir, browserProfile)
		if err := fsperm.MkdirAll(profile); err != nil {
			t.Fatal(err)
		}
		ownedBy(t, profile, gone)
		profileHolds(t, onDisk(t, profile))
		want := ownFileReport(profile, gone) + offered(onDisk(t, profile), false)

		if err := PrepareBrowserProfile(profile); err == nil || err.Error() != want {
			t.Errorf("PrepareBrowserProfile = %v;\nwant %s", err, want)
		}
	})
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
	for _, tt := range []struct {
		name, dir, folder string
		recursive         bool
	}{
		{name: "a folder above it", dir: dir, folder: above},
		{name: "a folder above it, holding only what Loadline names its own", dir: spool, folder: filepath.Dir(spool)},
		{name: "the data directory itself", dir: dir, folder: dir, recursive: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			found := ownedFolder(tt.folder, gone)
			movableAs(t, found)
			profileHolds(t, onDisk(t, tt.folder))
			top := filepath.Dir(filepath.Dir(tt.dir))
			want := folderReport(found) + offered(onDisk(t, tt.folder), tt.recursive)

			if err := Verify(tt.dir, top); err == nil || err.Error() != want {
				t.Errorf("Verify = %v;\nwant %s", err, want)
			}
			if got, err := Check(tt.dir, top); err != nil || got != want {
				t.Errorf("Check = %q, %v;\nwant %q", got, err, want)
			}
		})
	}
}

// takeown /r takes everything below the folder it is given, the browser
// profile's files and the files Loadline is still writing among them, and
// through a file's other name, the file wherever else it is. So the data
// directory is taken back with what it holds only while it holds nothing at
// its top that is not Loadline's, and no file anywhere in it has a name
// outside it. Otherwise it is taken back alone.
func TestMovable_TakesBackOnlyTheDataDirectoryWhenItHoldsMore(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, dir string){
		"a file that is not Loadline's": func(t *testing.T, dir string) {
			t.Helper()
			writeAt(t, filepath.Join(dir, "notes.txt"))
		},
		"a file of Loadline's with a name elsewhere": func(t *testing.T, dir string) {
			t.Helper()
			if err := os.Link(filepath.Join(dir, "token"), filepath.Join(t.TempDir(), "elsewhere")); err != nil {
				t.Fatal(err)
			}
		},
		"a spooled event's temporary name for a file elsewhere": func(t *testing.T, dir string) {
			t.Helper()
			linkOutOf(t, filepath.Join(dir, "spool", ".put-1.tmp"))
		},
		"a token's temporary name for a file elsewhere": func(t *testing.T, dir string) {
			t.Helper()
			linkOutOf(t, filepath.Join(dir, ".token-9999"))
		},
		"an installed binary's temporary name for a file elsewhere": func(t *testing.T, dir string) {
			t.Helper()
			linkOutOf(t, filepath.Join(dir, "bin", ".loadline-1.tmp"))
		},
		"a file in the browser profile with a name elsewhere": func(t *testing.T, dir string) {
			t.Helper()
			linkOutOf(t, filepath.Join(dir, browserProfile, "Default", "Cookies"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := home(t)
			plant(t, dir)
			found := ownedFolder(dir, gone)
			movableAs(t, found)
			profileHolds(t, onDisk(t, dir))
			want := folderReport(found) + offered(onDisk(t, dir), false)

			if err := Verify(dir, filepath.Dir(dir)); err == nil || err.Error() != want {
				t.Errorf("Verify = %v;\nwant %s", err, want)
			}
		})
	}
}

// What takeown /r reaches only through the data directory's own names, the
// token's leftover temporary name for it and whatever the browser keeps in
// its profile, takes nothing from anywhere else.
func TestMovable_TakesBackWithAllItHoldsADataDirectoryOfItsOwn(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, dir string){
		"the token's leftover temporary name": func(t *testing.T, dir string) {
			t.Helper()
			if err := os.Link(filepath.Join(dir, "token"), filepath.Join(dir, ".token-5678")); err != nil {
				t.Fatal(err)
			}
		},
		"what the browser keeps in its profile": func(t *testing.T, dir string) {
			t.Helper()
			writeAt(t, filepath.Join(dir, browserProfile, "Default", "Preferences"))
			writeAt(t, filepath.Join(dir, browserProfile, "Default", "Cache", "data_0"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := home(t)
			plant(t, dir)
			found := ownedFolder(dir, gone)
			movableAs(t, found)
			profileHolds(t, onDisk(t, dir))
			want := folderReport(found) + offered(onDisk(t, dir), true)

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
// but for a few characters, and a control or format character can hide in a
// path, or reorder it, as a terminal shows it. A path that holds one of
// those is given no command, but the tool's name. Each offer calls what it
// takes back a folder or a file, as it is.
func TestTakeBackAdvice_QuotesThePathOrNamesTheTool(t *testing.T) {
	quoted := func(path string, what taken) string {
		if what == aFile {
			return offeredFile(path)
		}
		return offered(path, what == aFolderAndAllItHolds)
	}
	named := func(path string, what taken) string {
		lead, which := takeBackLead, path
		switch what {
		case aFolder:
		case aFile:
			lead = fileLead
		case aFolderAndAllItHolds:
			which += " and all it holds"
		}
		return lead + "take ownership of " + which + " with takeown and then run loadline install"
	}
	for _, tt := range []struct {
		name, path string
		want       func(path string, what taken) string
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
		{"a tab", "C:\\Users\\a\tb\\AppData\\Local\\loadline", named},
		{"a delete", "C:\\Users\\a\x7fb\\AppData\\Local\\loadline", named},
		{"a next line", "C:\\Users\\a\u0085b\\AppData\\Local\\loadline", named},
		{"a right-to-left override", "C:\\Users\\a\u202eb\\AppData\\Local\\loadline", named},
		{"a zero-width space", "C:\\Users\\a\u200bb\\AppData\\Local\\loadline", named},
		{"a left-to-right isolate", "C:\\Users\\a\u2066b\u2069\\AppData\\Local\\loadline", named},
	} {
		for _, what := range []taken{aFolder, aFolderAndAllItHolds, aFile} {
			if got, want := takeBackAdvice(tt.path, what), tt.want(tt.path, what); got != want {
				t.Errorf("takeBackAdvice(%s, %d), with %s = %q;\nwant %q", tt.path, what, tt.name, got, want)
			}
		}
	}
}

// The data directory is placed before it is walked: outside the profile
// nothing is offered for it, or for anything in it, so the walk that tells
// whether takeown /r would reach nothing else is not made at all.
func TestTakeBackDataDir_DoesNotWalkADataDirectoryOutsideTheProfile(t *testing.T) {
	dir := home(t)
	inProfileAnswers(t, func(string) (bool, error) { return false, nil })
	previous := inspect
	looked := 0
	inspect = func(path string) (fsperm.Entry, error) {
		looked++
		return previous(path)
	}
	t.Cleanup(func() { inspect = previous })

	if got := takeBackDataDir(gone, dir, filepath.Join(dir, "token")); got != "" {
		t.Errorf("takeBackDataDir = %q, want nothing offered outside the profile", got)
	}
	if looked != 0 {
		t.Errorf("takeBackDataDir looked at %d paths in %s, which is outside the profile; want none", looked, dir)
	}
}
