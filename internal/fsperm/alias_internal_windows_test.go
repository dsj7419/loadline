package fsperm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// runAs makes the judgements below take sid for the user Loadline runs as,
// for the rest of the test.
func runAs(t *testing.T, sid *windows.SID) {
	t.Helper()
	previous := currentUser
	currentUser = func() (*windows.SID, error) { return sid.Copy() }
	t.Cleanup(func() { currentUser = previous })
}

// A CI runner may run as the built-in Administrator, this machine's account
// of RID 500, whose SID the security descriptor string format writes by an
// alias, LA, as it writes SYSTEM as SY and Administrators as BA. Every
// judgement of a list compares SIDs, not their text, so each holds for such
// a user as for any other: the list Private gives is exactly private, a
// file's inherited copy of it too, it admits nobody else, and the user owns
// what they own.
func TestJudgements_HoldForAUserWhoseSIDTheFormatWritesByAnAlias(t *testing.T) {
	admin, err := windows.StringToSid("LA")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(admin.String(), "S-1-5-21-") || !strings.HasSuffix(admin.String(), "-500") {
		t.Fatalf("LA is %s, not this machine's account of RID 500", admin)
	}
	dir := t.TempDir()
	folder, file, made := filepath.Join(dir, "spool"), filepath.Join(dir, "token"), filepath.Join(dir, "bin")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Put back for the user the test runs as, once the judgements are done,
	// so that the temporary folder can be removed.
	t.Cleanup(func() {
		_ = ClearExplicit(file)
		_ = PassDown(folder)
	})
	runAs(t, admin)
	// Written by the S-1-… form, as privateDescriptor writes the user.
	fspermtest.SetAccessListAsIs(t, folder, "D:P(A;OICI;FA;;;"+admin.String()+")(A;OICI;FA;;;S-1-5-18)(A;OICI;FA;;;S-1-5-32-544)")
	fspermtest.SetAccessListAsIs(t, file, "D:AI(A;ID;FA;;;"+admin.String()+")(A;ID;FA;;;S-1-5-18)(A;ID;FA;;;S-1-5-32-544)")
	if err := MkdirAll(made); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	stored := fspermtest.AccessList(t, file)
	if text := stored.String(); !strings.Contains(text, ";;;LA)") || strings.Contains(text, admin.String()) {
		t.Fatalf("the format wrote the list as %s, without the alias, so the test proves nothing", text)
	}

	want := fspermtest.ParseList(t, "D:(A;ID;FA;;;"+admin.String()+")(A;ID;FA;;;SY)(A;ID;FA;;;BA)")
	if !stored.SameEntries(want) {
		t.Errorf("the file's list, read back, is %s; want the entries stored, %s", stored, want)
	}
	for _, path := range []string{folder, made} {
		if private, err := DirPrivate(path); err != nil || !private {
			t.Errorf("DirPrivate(%s), whose list is %s, = %t, %v; want true", path, fspermtest.AccessList(t, path), private, err)
		}
	}
	for _, path := range []string{folder, file, made} {
		if private, err := EntryPrivate(path); err != nil || !private {
			t.Errorf("EntryPrivate(%s) = %t, %v; want true", path, private, err)
		}
		if others, err := Exposure(path); err != nil || others != "" {
			t.Errorf("Exposure(%s) = %q, %v; want nobody else", path, others, err)
		}
	}
	ownedBySID(t, dir, admin)
	if owner, err := ForeignOwner(dir); err != nil || owner != "" {
		t.Errorf("ForeignOwner of a folder the user owns = %q, %v; want none", owner, err)
	}
}
