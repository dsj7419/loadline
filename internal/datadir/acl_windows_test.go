package datadir_test

// What Secure, and doctor's Check, make of the entries someone who could
// once change the access list of one of Loadline's files may have left on
// it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
	"github.com/dsj7419/loadline/internal/protocol"
)

func hookEvent(t *testing.T) protocol.Envelope {
	t.Helper()
	id, err := protocol.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Envelope{
		V: protocol.SchemaVersion, ID: id, Kind: protocol.KindHook, At: time.Unix(1_700_000_000, 0),
		ConfigDir: `C:\work\.claude`, Payload: json.RawMessage(`{}`),
	}
}

// An object entry, which lets its trustee into a file as a plain one does,
// is taken off the token and the binary every hook runs.
func TestSecure_TakesOffAnObjectEntryThatAdmitsOthers(t *testing.T) {
	for _, name := range []string{"token", filepath.Join("bin", "loadline.exe")} {
		t.Run(name, func(t *testing.T) {
			home, top := loadlineHome(t)
			path := filepath.Join(home, name)
			fspermtest.SetEntry(t, path, fspermtest.Entry{Kind: fspermtest.Object, Mask: fspermtest.FullControl})

			if err := datadir.Secure(home, top); err != nil {
				t.Fatalf("Secure: %v", err)
			}

			if got := fspermtest.AccessList(t, path); strings.Contains(got, ";AU)") {
				t.Errorf("after Secure, %s still admits Authenticated Users: %s", path, got)
			}
		})
	}
}

// Doctor reports such an entry, so that the user is sent to remove it.
func TestCheck_ReportsAnObjectEntryThatAdmitsOthers(t *testing.T) {
	home, top := loadlineHome(t)
	token := filepath.Join(home, "token")
	fspermtest.SetEntry(t, token, fspermtest.Entry{Kind: fspermtest.Object, Mask: fspermtest.FullControl})

	got, err := datadir.Check(home, top)

	if err != nil || !strings.HasPrefix(got, token+" is not private: its access list also admits "+fspermtest.AuthenticatedUsers(t)) {
		t.Errorf("Check = %q, %v; want it to report %s, whose access list is %s", got, err, token, fspermtest.AccessList(t, token))
	}
}

// asMemberOfUsers runs f with BUILTIN\Users as the primary group of this
// process, as a local account has None and a domain account Domain Users,
// groups other accounts are in too, where a Microsoft account has itself.
func asMemberOfUsers(t *testing.T, f func()) {
	t.Helper()
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_DEFAULT|windows.TOKEN_QUERY, &token); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = token.Close() }()
	previous, err := token.GetTokenPrimaryGroup()
	if err != nil {
		t.Fatal(err)
	}
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	set := func(sid *windows.SID) error {
		group := windows.Tokenprimarygroup{PrimaryGroup: sid}
		//nolint:gosec // G103: TokenPrimaryGroup takes a TOKEN_PRIMARY_GROUP by pointer
		return windows.SetTokenInformation(token, windows.TokenPrimaryGroup, (*byte)(unsafe.Pointer(&group)), uint32(unsafe.Sizeof(group)))
	}
	if err := set(users); err != nil {
		t.Skipf("cannot make BUILTIN\\Users the primary group here: %v", err)
	}
	defer func() {
		if err := set(previous.PrimaryGroup); err != nil {
			t.Errorf("restore the primary group: %v", err)
		}
	}()
	f()
}

// An inheritable entry for CREATOR GROUP on the spool admits nobody on the
// spool itself, but every event a hook spools afterwards is created with an
// entry for the primary group of the user's account. Secure takes it off.
func TestSecure_TakesOffACreatorGroupEntryOnTheSpool(t *testing.T) {
	home, top := loadlineHome(t)
	spool := filepath.Join(home, "spool")
	user := fspermtest.UserSID(t)
	fspermtest.SetAccessList(t, spool, "D:P(A;OICIIO;FA;;;CG)(A;OICI;FA;;;"+user+")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")

	if err := datadir.Secure(home, top); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	var event string
	asMemberOfUsers(t, func() {
		if err := (forward.Spool{Dir: spool}).Put(hookEvent(t)); err != nil {
			t.Fatalf("Put: %v", err)
		}
		events, err := filepath.Glob(filepath.Join(spool, "1700000000*.json"))
		if err != nil || len(events) != 1 {
			t.Fatalf("spooled events: %v, %v", events, err)
		}
		event = events[0]
	})
	if others := fspermtest.Others(t, event, fspermtest.Any); len(others) != 0 {
		t.Errorf("the event a hook spooled after Secure admits %v: %s", others, fspermtest.AccessList(t, event))
	}
}

// entriesOf returns the entries of an access list in the security
// descriptor string format, without the owner, group and flags Windows
// writes before them.
func entriesOf(list string) string {
	_, dacl, _ := strings.Cut(list, "D:")
	if i := strings.IndexByte(dacl, '('); i >= 0 {
		return dacl[i:]
	}
	return ""
}

// Whatever was given to one of Loadline's files or folders explicitly, a
// denial included, and any list of its own but Loadline's private one, is
// taken off, so that only what the data directory passes down applies.
func TestSecure_LeavesOnlyWhatTheDataDirectoryPassesDown(t *testing.T) {
	user := fspermtest.UserSID(t)
	for name, tt := range map[string]struct{ path, list string }{
		"an entry for the user alone":                {"token", "D:(A;;FA;;;" + user + ")"},
		"a denial for everyone":                      {"token", "D:(D;;WD;;;WD)"},
		"a denial in a list of its own":              {"token", "D:P(D;;WD;;;WD)(A;;FA;;;" + user + ")(A;;FA;;;SY)(A;;FA;;;BA)"},
		"a list of its own without Administrators":   {"spool", "D:P(A;OICI;FA;;;" + user + ")(A;OICI;FA;;;SY)"},
		"the private list, but passing nothing down": {"bin", "D:P(A;;FA;;;" + user + ")(A;;FA;;;SY)(A;;FA;;;BA)"},
	} {
		t.Run(name, func(t *testing.T) {
			home, top := loadlineHome(t)
			path := filepath.Join(home, tt.path)
			// What the data directory passes down to a file or a folder of
			// the same kind, made now.
			fresh := filepath.Join(home, "loadline.log")
			if tt.path == "token" {
				writeFile(t, fresh)
			} else {
				fresh = filepath.Join(home, "spool", "bad")
				if err := os.Mkdir(fresh, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			want, wantProtected := entriesOf(fspermtest.AccessList(t, fresh)), fspermtest.Protected(t, fresh)
			fspermtest.SetAccessList(t, path, tt.list)

			if err := datadir.Secure(home, top); err != nil {
				t.Fatalf("Secure: %v", err)
			}

			if got := entriesOf(fspermtest.AccessList(t, path)); got != want || fspermtest.Protected(t, path) != wantProtected {
				t.Errorf("after Secure, %s, given %s, has %s (protected: %t); want %s (protected: %t), as the data directory passes down",
					path, tt.list, got, fspermtest.Protected(t, path), want, wantProtected)
			}
		})
	}
}

// A deny entry admits nobody, yet one on the spool keeps every hook from
// spooling an event, and install and the server take it off.
func TestSecure_TakesOffADenyEntryThatLocksTheUserOut(t *testing.T) {
	home, top := loadlineHome(t)
	spool := filepath.Join(home, "spool")
	user := fspermtest.UserSID(t)
	// The spool's own private list, and before it: nobody may add a file.
	fspermtest.SetAccessList(t, spool, "D:P(D;;DC;;;WD)(A;OICI;FA;;;"+user+")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	t.Cleanup(func() {
		fspermtest.SetAccessList(t, spool, "D:P(A;OICI;FA;;;"+user+")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	})
	if err := (forward.Spool{Dir: spool}).Put(hookEvent(t)); err == nil {
		t.Fatal("the deny entry did not keep a put out; the test proves nothing")
	}

	if err := datadir.Secure(home, top); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	if err := (forward.Spool{Dir: spool}).Put(hookEvent(t)); err != nil {
		t.Errorf("after Secure, a hook still cannot spool an event: %v\n%s is %s", err, spool, fspermtest.AccessList(t, spool))
	}
}

// One on the binary keeps every hook from running it.
func TestSecure_TakesOffADenyEntryOnTheBinary(t *testing.T) {
	home, top := loadlineHome(t)
	exe := filepath.Join(home, "bin", "loadline.exe")
	fspermtest.SetAccessList(t, exe, "D:(D;;WP;;;WD)")

	if err := datadir.Secure(home, top); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	if got := fspermtest.AccessList(t, exe); strings.Contains(got, "(D;") {
		t.Errorf("after Secure, nobody may run %s: %s", exe, got)
	}
}

// unreadable gives path a list that lets its owner, through an OWNER RIGHTS
// entry, read its data and nothing else: not read or change the list, which
// an owner otherwise always may. Its folder still lets the user see it.
func unreadable(t *testing.T, path string) {
	t.Helper()
	writeFile(t, path)
	fspermtest.SetAccessList(t, path, "D:P(A;;CC;;;OW)")
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("%s cannot even be seen: %v", path, err)
	}
}

// A file of Loadline's whose list the user may not read gets past the walk,
// and is refused where its list is read: Loadline cannot tell what it lets
// in, or take it off.
func TestSecure_RefusesAFileWhoseAccessListItCannotRead(t *testing.T) {
	home, top := loadlineHome(t)
	log := filepath.Join(home, "loadline.log")
	unreadable(t, log)

	if err := datadir.Secure(home, top); err == nil || !strings.Contains(err.Error(), log) {
		t.Errorf("Secure = %v, want it to fail, naming %s", err, log)
	}
}

func TestCheck_RefusesAFileWhoseAccessListItCannotRead(t *testing.T) {
	home, top := loadlineHome(t)
	log := filepath.Join(home, "loadline.log")
	unreadable(t, log)

	if got, err := datadir.Check(home, top); err == nil || !strings.Contains(err.Error(), log) {
		t.Errorf("Check = %q, %v; want it to fail, naming %s", got, err, log)
	}
}
