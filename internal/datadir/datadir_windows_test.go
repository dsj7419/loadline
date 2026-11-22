package datadir_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// The tests take the temporary folder they build in, with every folder above
// it, for a drive root. It is made private first, so that what the machine's
// own temporary folder passes down, which may let other accounts in, is no
// test's subject, and then, like the root of a second drive, lets every
// signed-in user modify what is created below it.
func driveRoot(t *testing.T) string {
	t.Helper()
	top := privateTop(t)
	fspermtest.Permissive(t, top)
	return top
}

// privateTop returns a temporary folder made private, which the tests take,
// with every folder above it, for one nobody else can move aside, so that
// only what they build below it is judged.
func privateTop(t *testing.T) string {
	t.Helper()
	top := t.TempDir()
	if err := fsperm.Private(top); err != nil {
		t.Fatal(err)
	}
	return top
}

func TestSecure_RefusesADataDirectoryOthersCanMoveAside(t *testing.T) {
	top := driveRoot(t)
	tools := filepath.Join(top, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tools, "loadline")

	err := datadir.Secure(home, top)

	if err == nil {
		t.Fatalf("Secure(%s) succeeded, want it refused: %s lets others move it aside", home, tools)
	}
	for _, want := range []string{
		tools + " lets ", fspermtest.AuthenticatedUsers(t), "delete or rename it",
		"replace the binary every hook runs", "LOADLINE_HOME", "user profile",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Secure = %v, want it to say %q", err, want)
		}
	}
	if _, statErr := os.Stat(home); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("Secure created %s although it refused it", home)
	}
}

func TestSecure_CreatesTheDataDirectoryPrivate(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")

	if err := datadir.Secure(home, top); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	if !fspermtest.Protected(t, home) || len(fspermtest.Others(t, home, fspermtest.Any)) > 0 {
		t.Errorf("%s admits %v, want only the user, SYSTEM and Administrators", home, fspermtest.Others(t, home, fspermtest.Any))
	}
}

func TestSecure_MakesAnExistingDataDirectoryPrivate(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := datadir.Secure(home, top); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	if others := fspermtest.Others(t, home, fspermtest.Any); len(others) > 0 {
		t.Errorf("%s admits %v, want only the user, SYSTEM and Administrators", home, others)
	}
}

// Doctor says what install and the server would refuse, in the same words.
func TestCheck_ReportsWhatSecureRefuses(t *testing.T) {
	top := driveRoot(t)
	tools := filepath.Join(top, "tools")
	home := filepath.Join(tools, "loadline")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	refused := datadir.Secure(home, top)
	if refused == nil {
		t.Fatal("Secure succeeded, want it refused")
	}

	got, err := datadir.Check(home, top)

	if err != nil || got != refused.Error() {
		t.Errorf("Check = %q, %v; want %q", got, err, refused.Error())
	}
}

func TestCheck_ReportsADataDirectoryOthersCanGetInto(t *testing.T) {
	home := filepath.Join(driveRoot(t), "loadline")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := datadir.Check(home, filepath.Dir(home))

	if err != nil || !strings.HasPrefix(got, home+" is not private: its access list also admits ") ||
		!strings.HasSuffix(got, "; run loadline install") {
		t.Errorf("Check = %q, %v; want it to say %s is not private and that install fixes it", got, err, home)
	}
}

func TestCheck_APrivateDataDirectoryPasses(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")
	if err := datadir.Secure(home, top); err != nil {
		t.Fatal(err)
	}

	if got, err := datadir.Check(home, top); err != nil || got != "" {
		t.Errorf("Check = %q, %v; want \"\"", got, err)
	}
}

// A missing data directory that install would make, below folders nobody
// else can move aside, is an error.
func TestCheck_AMissingDataDirectoryIsAnError(t *testing.T) {
	top := privateTop(t)

	got, err := datadir.Check(filepath.Join(top, "tools", "loadline"), top)

	if got != "" || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Check = %q, %v; want an error that matches fs.ErrNotExist", got, err)
	}
}

// A server refuses a data directory below a folder others can move aside
// before it makes anything there, so doctor finds none. It says why install
// would refuse to make it, judging the folders above it that exist, in the
// words install would use.
func TestCheck_SaysWhyInstallWouldRefuseADataDirectoryNotMadeYet(t *testing.T) {
	top := driveRoot(t)
	tools := filepath.Join(top, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{filepath.Join(tools, "loadline"), filepath.Join(tools, "apps", "loadline")} {
		refused := datadir.Secure(home, top)
		if refused == nil {
			t.Fatalf("Secure(%s) succeeded, want it refused", home)
		}
		if entries, err := os.ReadDir(tools); err != nil || len(entries) != 0 {
			t.Fatalf("the refused Secure left %d entries in %s (%v), want none", len(entries), tools, err)
		}

		got, err := datadir.Check(home, top)

		if want := home + " does not exist yet, and loadline install would refuse to make it: " + refused.Error(); err != nil || got != want {
			t.Errorf("Check(%s) = %q, %v; want %q", home, got, err, want)
		}
	}
}

// Only a data directory that is missing is judged as one install would make.
// One that cannot be read for another reason, here a name Windows refuses,
// is an error, below a folder others can move aside too.
func TestCheck_ADataDirectoryItCannotReadIsNoneNotMadeYet(t *testing.T) {
	top := driveRoot(t)
	tools := filepath.Join(top, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := datadir.Check(filepath.Join(tools, "load<line"), top)

	if got != "" || err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Check = %q, %v; want an error that is not fs.ErrNotExist", got, err)
	}
}

// junction makes link a directory junction to target, as any user may in a
// folder they can create directories in.
func junction(t *testing.T, link, target string) {
	t.Helper()
	//nolint:gosec // G204: the paths are the test's own temporary folders
	if out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A data directory that was open before, or one whose files someone gave
// access to: making the directory private keeps what a file was given
// explicitly, so Secure takes it off Loadline's files.
func TestSecure_RemovesExplicitEntriesFromLoadlinesFiles(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")
	token, event := filepath.Join(home, "token"), filepath.Join(home, "spool", "event.json")
	writeFile(t, token)
	writeFile(t, event)
	fspermtest.Grant(t, token, fspermtest.Read, windows.NO_INHERITANCE)
	fspermtest.Grant(t, event, fspermtest.Read, windows.NO_INHERITANCE)

	if err := datadir.Secure(home, top); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	for _, path := range []string{token, event, filepath.Dir(event)} {
		if others := fspermtest.Others(t, path, fspermtest.Any); len(others) > 0 {
			t.Errorf("%s admits %s after Secure, want only the user, SYSTEM and Administrators", path, strings.Join(others, ", "))
		}
	}
	if data, err := os.ReadFile(token); err != nil || len(data) != 64 {
		t.Errorf("read the token back: %q, %v", data, err)
	}
}

// Doctor reports a file of Loadline's that someone gave access to, which
// making the directory private does not take away.
func TestCheck_ReportsAFileOfLoadlinesOthersCanGetInto(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")
	if err := datadir.Secure(home, top); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(home, "token")
	writeFile(t, token)
	fspermtest.Grant(t, token, fspermtest.Read, windows.NO_INHERITANCE)

	got, err := datadir.Check(home, top)

	if err != nil || !strings.HasPrefix(got, token+" is not private: its access list also admits ") ||
		!strings.HasSuffix(got, "; run loadline install") {
		t.Errorf("Check = %q, %v; want it to say %s is not private and that install fixes it", got, err, token)
	}
}

// loadlineHome returns a data directory below a driveRoot, top, made as a
// running Loadline has it: private, with the token, a spooled event and
// the installed binary in the folders Loadline makes for them.
func loadlineHome(t *testing.T) (home, top string) {
	t.Helper()
	top = driveRoot(t)
	home = filepath.Join(top, "loadline")
	for _, dir := range []string{"spool", "bin"} {
		if err := fsperm.MkdirAll(filepath.Join(home, dir)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"token", filepath.Join("spool", "1-0000000a.json"), filepath.Join("bin", "loadline.exe")} {
		writeFile(t, filepath.Join(home, name))
	}
	if err := datadir.Secure(home, top); err != nil {
		t.Fatal(err)
	}
	return home, top
}

// inFlight are the names, in the data directory, of the files other
// Loadline processes write before they put them in place: the token's, a
// spooled event's and the installed binary's.
var inFlight = []string{".token-1234", filepath.Join("spool", ".put-1234.tmp"), filepath.Join("bin", ".loadline-1234.tmp")}

// moveIn writes a file in top, which lets every signed-in user modify what
// is created below it, and moves it to path, which keeps what it inherited
// there: its access list is then not what the folder at path passes down,
// which shows whether anything rewrote it since.
func moveIn(t *testing.T, top, path string) {
	t.Helper()
	outside := filepath.Join(top, "outside-"+filepath.Base(path))
	writeFile(t, outside)
	if err := os.Rename(outside, path); err != nil {
		t.Fatal(err)
	}
	if others := fspermtest.Others(t, path, fspermtest.Any); len(others) == 0 {
		t.Fatalf("%s admits nobody else, so the test proves nothing", path)
	}
}

// A file another Loadline process is writing under a temporary name is
// that process's until it puts it in place. It was made after its folder
// was private, so nothing is to be taken off it, and Secure and Make,
// which the creation of the token runs in every caller at once, leave it
// as it is.
func TestSecure_LeavesAFileAnotherProcessIsWritingAlone(t *testing.T) {
	for _, name := range inFlight {
		t.Run(filepath.Base(name), func(t *testing.T) {
			home, top := loadlineHome(t)
			path := filepath.Join(home, name)
			moveIn(t, top, path)
			before := fspermtest.AccessList(t, path)

			if err := datadir.Secure(home, top); err != nil {
				t.Fatalf("Secure: %v", err)
			}
			if err := datadir.Make(home); err != nil {
				t.Fatalf("Make: %v", err)
			}

			if after := fspermtest.AccessList(t, path); after != before {
				t.Errorf("the access list of %s, another process's, went from %s to %s", path, before, after)
			}
		})
	}
}

// One begun before its folder was private took what the folder passed down
// then. Once Secure has judged everything else, the data directory passes
// its new list down to it too.
func TestSecure_PassesTheListDownToAFileBegunBeforeTheDirectoryWasPrivate(t *testing.T) {
	dir, parent := openDataDir(t, privateTop(t))
	path := filepath.Join(dir, ".token-1234")
	writeFile(t, path)
	if others := fspermtest.Others(t, path, fspermtest.Any); len(others) == 0 {
		t.Fatalf("%s admits nobody else, so the test proves nothing", path)
	}

	if err := datadir.Secure(dir, parent); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	if others := fspermtest.Others(t, path, fspermtest.Any); len(others) > 0 {
		t.Errorf("%s admits %v, want the list the data directory passes down", path, others)
	}
}

// Doctor does not report another process's file as one of Loadline's that
// others can get into.
func TestCheck_PassesOverAFileAnotherProcessIsWriting(t *testing.T) {
	for _, name := range inFlight {
		t.Run(filepath.Base(name), func(t *testing.T) {
			home, top := loadlineHome(t)
			moveIn(t, top, filepath.Join(home, name))

			if got, err := datadir.Check(home, top); err != nil || got != "" {
				t.Errorf("Check = %q, %v; want \"\"", got, err)
			}
		})
	}
}

// Windows refuses to open a file whose deletion is pending, as a token's
// temporary file is a moment after another caller linked it into place,
// with "access denied". Nothing that prepares or checks the directory opens
// another process's file, so none of them fails on one.
func TestSecure_TakesADataDirectoryWhileAnotherProcessDeletesItsFile(t *testing.T) {
	for _, name := range inFlight {
		t.Run(filepath.Base(name), func(t *testing.T) {
			home, top := loadlineHome(t)
			path := filepath.Join(home, name)
			writeFile(t, path)
			fspermtest.DeletePending(t, path)

			if err := datadir.Verify(home, top); err != nil {
				t.Errorf("Verify: %v", err)
			}
			if err := datadir.Secure(home, top); err != nil {
				t.Errorf("Secure: %v", err)
			}
			if err := datadir.Make(home); err != nil {
				t.Errorf("Make: %v", err)
			}
			if got, err := datadir.Check(home, top); err != nil || got != "" {
				t.Errorf("Check = %q, %v; want \"\"", got, err)
			}
		})
	}
}

// A file of Loadline's whose deletion is pending, as a spooled event's is
// while a drain deletes it once delivered, is refused for a moment: that
// is waited out, and the file, once gone, left out.
func TestSecure_LeavesOutAFileOfLoadlinesThatIsBeingDeleted(t *testing.T) {
	for name, prepare := range map[string]func(home, top string) error{
		"Secure": datadir.Secure,
		"Check": func(home, top string) error {
			if got, err := datadir.Check(home, top); err != nil || got != "" {
				return fmt.Errorf("Check = %q, %w", got, err)
			}
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			home, top := loadlineHome(t)
			event := filepath.Join(home, "spool", "1-0000000a.json")
			time.AfterFunc(20*time.Millisecond, fspermtest.DeletePending(t, event))

			if err := prepare(home, top); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			if _, err := os.Lstat(event); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s is still there (%v): the test meant it deleted while %s looked", event, err, name)
			}
		})
	}
}

// One refused for longer than any deletion takes cannot be told apart from
// a file the user may not open, so it is no file to pass over: Secure and
// Check take the refusal as it is, naming the file.
func TestSecure_RefusesAFileOfLoadlinesItCannotOpen(t *testing.T) {
	home, top := loadlineHome(t)
	event := filepath.Join(home, "spool", "1-0000000a.json")
	fspermtest.DeletePending(t, event)

	if err := datadir.Secure(home, top); err == nil || !strings.Contains(err.Error(), event) {
		t.Errorf("Secure = %v, want it to fail, naming %s", err, event)
	}
	if got, err := datadir.Check(home, top); err == nil || !strings.Contains(err.Error(), event) {
		t.Errorf("Check = %q, %v; want it to fail, naming %s", got, err, event)
	}
}

// The folders Loadline makes in the data directory are private from the
// start, with a list of their own, and Secure keeps them so: taking that
// list off, only to have the data directory pass the same entries down,
// would rewrite what every file in them inherited.
func TestSecure_LeavesLoadlinesPrivateFoldersAsTheyAre(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")
	folders := []string{filepath.Join(home, "spool"), filepath.Join(home, "bin")}
	before := make([]string, len(folders))
	for i, folder := range folders {
		if err := fsperm.MkdirAll(folder); err != nil {
			t.Fatal(err)
		}
		before[i] = fspermtest.AccessList(t, folder)
	}

	if err := datadir.Secure(home, top); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	for i, folder := range folders {
		if after := fspermtest.AccessList(t, folder); after != before[i] {
			t.Errorf("Secure changed the access list of %s from %s to %s", folder, before[i], after)
		}
	}
}

// A file of Loadline's that still carries what a shared folder passed down
// to it, as one moved in from there does, is made the user's alone.
func TestSecure_TakesOthersOffAFileOfLoadlinesThatInheritedThem(t *testing.T) {
	home, top := loadlineHome(t)
	log := filepath.Join(home, "loadline.log")
	moveIn(t, top, log)

	if err := datadir.Secure(home, top); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	if others := fspermtest.Others(t, log, fspermtest.Any); len(others) > 0 {
		t.Errorf("%s admits %s after Secure, want only the user, SYSTEM and Administrators", log, strings.Join(others, ", "))
	}
}

// A bin that is a junction to a folder outside, as one left in a data
// directory that was open before: making it private would set the list on
// the junction, and the binary every hook runs would go into the folder it
// points to.
func TestSecure_RefusesALinkAmongLoadlinesFiles(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(top, "theirs")
	if err := os.Mkdir(theirs, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	junction(t, bin, theirs)

	err := datadir.Secure(home, top)

	if err == nil || !strings.Contains(err.Error(), bin+" is a link") {
		t.Fatalf("Secure = %v, want it refused, naming %s as a link", err, bin)
	}
	if fspermtest.Protected(t, theirs) {
		t.Errorf("Secure changed the access list of %s, where the link points", theirs)
	}
	if got, checkErr := datadir.Check(home, top); checkErr != nil || got != err.Error() {
		t.Errorf("Check = %q, %v; want what Secure refused with: %q", got, checkErr, err.Error())
	}
}

// A folder named like a file the system leaves, holding someone else's
// files, is not made private with the data directory.
func TestMake_LeavesASystemNamedFolderOfOtherFilesAlone(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")
	notes := filepath.Join(home, "desktop.ini", "notes.txt")
	writeFile(t, notes)
	before := fspermtest.AccessList(t, notes)

	err := datadir.Make(home)

	if err == nil || !strings.Contains(err.Error(), "desktop.ini, which is not Loadline's") {
		t.Errorf("Make = %v, want it refused, naming desktop.ini", err)
	}
	if after := fspermtest.AccessList(t, notes); after != before {
		t.Errorf("Make changed the access list of %s, in a folder named desktop.ini, from %s to %s", notes, before, after)
	}
}

// One that is a junction into another folder leads nothing Secure does
// there.
func TestSecure_DoesNotFollowASystemNamedJunction(t *testing.T) {
	top := privateTop(t)
	home := filepath.Join(top, "loadline")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(top, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.Permissive(t, outside)
	shared := filepath.Join(outside, "shared.txt")
	writeFile(t, shared)
	before := fspermtest.AccessList(t, shared)
	junction(t, filepath.Join(home, "desktop.ini"), outside)

	err := datadir.Secure(home, top)

	if err == nil || !strings.Contains(err.Error(), "desktop.ini, which is not Loadline's") {
		t.Errorf("Secure = %v, want it refused, naming desktop.ini", err)
	}
	if after := fspermtest.AccessList(t, shared); after != before {
		t.Errorf("Secure changed the access list of %s, through a junction named desktop.ini, from %s to %s", shared, before, after)
	}
	if fspermtest.Protected(t, outside) {
		t.Errorf("Secure made %s, where a junction named desktop.ini points, private", outside)
	}
}
