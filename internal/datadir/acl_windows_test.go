package datadir_test

// What Secure, and doctor's Check, make of the entries someone who could
// once change the access list of one of Loadline's files may have left on
// it.

import (
	"encoding/json"
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
