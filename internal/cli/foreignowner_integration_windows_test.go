package cli_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// otherAccountVariable names a second local account, and so opts in to the
// tests here. They give a data directory to an account other than the
// user's, which takes SeRestorePrivilege, a right only an elevated process
// holds, and give it back at their end.
const otherAccountVariable = "LOADLINE_IT_OTHER_ACCOUNT"

// Whoever owns a folder can always change its access list, so a data
// directory that another account owns is never the user's alone, however
// private Loadline made it. Install, the server and doctor refuse it, naming
// the account, and offer no command to take it back: the account still
// exists, and the folder may be its own.
func TestIntegration_InstallServeAndDoctorRefuseADataDirectoryAnotherAccountOwns(t *testing.T) {
	account, name := otherAccount(t)
	home := homeOwnedBy(t, account)

	doctorLine := assertRefusedByAll(t, home+" belongs to "+name+", who can give themselves the right to delete or rename it")

	if strings.Contains(doctorLine, "takeown") {
		t.Errorf("doctor's line %q offers takeown, want no offer to take a folder from an account that exists", doctorLine)
	}
}

// After a profile was moved or restored, its folders can keep as their owner
// an account this computer no longer has, a SID of its own account domain
// that names no account. Doctor says how to take such a data directory back.
func TestIntegration_DoctorOffersToTakeBackAFolderAnOldAccountOwns(t *testing.T) {
	optIn(t)
	gone := goneAccount(t)
	home := homeOwnedBy(t, gone)

	got := run(t, "doctor", "--config-dir", t.TempDir())

	line := dataDirectoryLine(got.stdout)
	refusal := "✖ data directory: " + home + " belongs to " + gone.String() + ", who can give themselves the right to delete or rename it"
	if got.code != 1 || !strings.HasPrefix(line, refusal) {
		t.Fatalf("doctor = %d, stdout:\n%s\nwant 1 and a line that starts %q", got.code, got.stdout, refusal)
	}
	if !strings.Contains(line, "takeown /f ") {
		t.Errorf("doctor's line %q offers no takeown /f, which it offers only for a folder in your user profile, "+
			"where the temporary folder is by default", line)
	}
}

// optIn skips the test, saying what is missing, unless otherAccountVariable
// is set, and returns the account it names.
func optIn(t *testing.T) string {
	t.Helper()
	if account := os.Getenv(otherAccountVariable); account != "" {
		return account
	}
	missing := otherAccountVariable + " is unset"
	if !windows.GetCurrentProcessToken().IsElevated() {
		missing += ", and this shell is not elevated"
	}
	t.Skipf("%s; set it to a second local account, in an elevated shell, to run this test (see CONTRIBUTING.md)", missing)
	return ""
}

// otherAccount returns the SID of the account otherAccountVariable names,
// and its name as Windows gives it: DOMAIN\account, which for a local
// account is this computer's name and the account's.
func otherAccount(t *testing.T) (*windows.SID, string) {
	t.Helper()
	named := optIn(t)
	sid, _, use, err := windows.LookupSID("", named)
	if err != nil {
		t.Fatalf("%s (%s) names no account here: %v", otherAccountVariable, named, err)
	}
	if use != windows.SidTypeUser {
		t.Fatalf("%s (%s) names a principal of kind %d, not a user account", otherAccountVariable, named, use)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if sid.Equals(user.User.Sid) {
		t.Fatalf("%s (%s) names you, not another account", otherAccountVariable, named)
	}
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		t.Fatal(err)
	}
	return sid, domain + `\` + account
}

// goneAccount returns the SID of an account this computer no longer has:
// that of its own account domain, plus a RID none of its accounts has.
func goneAccount(t *testing.T) *windows.SID {
	t.Helper()
	computer, err := windows.ComputerName()
	if err != nil {
		t.Fatal(err)
	}
	domain, _, use, err := windows.LookupSID("", computer)
	if err != nil || use != windows.SidTypeDomain {
		t.Fatalf("look up the account domain of %s: %v, a principal of kind %d; want a domain", computer, err, use)
	}
	sid, err := windows.StringToSid(domain.String() + "-4242")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sid.LookupAccount(""); !errors.Is(err, windows.ERROR_NONE_MAPPED) {
		t.Fatalf("look up %s: %v; want no account of this computer to have its RID", sid, err)
	}
	return sid
}

// homeOwnedBy points LOADLINE_HOME at a data directory that owner owns, and
// returns it. Its config keeps doctor off the real port and the real claude.
func homeOwnedBy(t *testing.T, owner *windows.SID) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	writeAll(t, map[string]string{
		filepath.Join(home, "config.json"): fmt.Sprintf(`{"port":%d,"claudePath":%q}`, freePort(t), filepath.Join(home, "no-claude")),
	})
	giveTo(t, home, owner)
	t.Setenv("LOADLINE_HOME", home)
	return home
}

// giveTo makes owner the owner of path, and gives path back to the owner it
// had before the test's temporary folders are removed, so that none of
// another account's is left behind.
func giveTo(t *testing.T, path string, owner *windows.SID) {
	t.Helper()
	previous := ownerOf(t, path)
	if err := setOwner(path, owner); err != nil {
		t.Fatalf("give %s to %s: %v; that takes SeRestorePrivilege, which only an elevated shell can enable", path, owner, err)
	}
	t.Cleanup(func() {
		if err := setOwner(path, previous); err != nil {
			t.Errorf("give %s back to %s: %v", path, previous, err)
		}
	})
}

// ownerOf returns the owner of path.
func ownerOf(t *testing.T, path string) *windows.SID {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	copied, err := owner.Copy()
	if err != nil {
		t.Fatal(err)
	}
	return copied
}

// setOwner makes owner the owner of path, which for an account other than
// the user's takes SeRestorePrivilege. The privilege is enabled for this
// alone, on a thread of its own that impersonates the process: the code
// under test never runs with it.
func setOwner(path string, owner *windows.SID) error {
	done := make(chan error, 1)
	go func() {
		// Never unlocked: the thread ends with this goroutine, so that even
		// a failed revert leaves no thread with the privilege behind.
		runtime.LockOSThread()
		done <- setOwnerAsRestorer(path, owner)
	}()
	return <-done
}

// setOwnerAsRestorer has the calling thread, which must be locked to its
// goroutine, impersonate the process with SeRestorePrivilege enabled, make
// owner the owner of path, and revert.
func setOwnerAsRestorer(path string, owner *windows.SID) (err error) {
	if err := windows.ImpersonateSelf(windows.SecurityImpersonation); err != nil {
		return fmt.Errorf("impersonate the process: %w", err)
	}
	defer func() { err = errors.Join(err, windows.RevertToSelf()) }()
	var token windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_ADJUST_PRIVILEGES, false, &token); err != nil {
		return fmt.Errorf("open the thread's token: %w", err)
	}
	defer func() { _ = token.Close() }()
	name, err := windows.UTF16PtrFromString("SeRestorePrivilege")
	if err != nil {
		return fmt.Errorf("name SeRestorePrivilege: %w", err)
	}
	var restore windows.LUID
	if err := windows.LookupPrivilegeValue(nil, name, &restore); err != nil {
		return fmt.Errorf("look up SeRestorePrivilege: %w", err)
	}
	// A token that lacks the privilege is no error here: AdjustTokenPrivileges
	// succeeds without enabling it, and setting the owner is then refused.
	enable := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges:     [1]windows.LUIDAndAttributes{{Luid: restore, Attributes: windows.SE_PRIVILEGE_ENABLED}},
	}
	if err := windows.AdjustTokenPrivileges(token, false, &enable, 0, nil, nil); err != nil {
		return fmt.Errorf("enable SeRestorePrivilege: %w", err)
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, owner, nil, nil, nil)
}
