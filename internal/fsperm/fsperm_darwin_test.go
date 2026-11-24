package fsperm_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// macOS keeps an access control list beside a folder's mode, and a denial in
// it keeps even the owner of a folder of mode 0700 from listing it. Giving
// the owner every right cannot undo that: RegainList leaves the folder as it
// was, and doctor's answer beforehand agrees.
func TestRegainList_LeavesAFolderAnAccessControlListKeepsTheOwnerOutOf(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may list any folder, whatever its access control list")
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "loadline")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G204: the account is the test's own, the folder its own temporary one
	if out, err := exec.CommandContext(t.Context(), "/bin/chmod", "+a", account.Username+" deny list", dir).CombinedOutput(); err != nil {
		t.Fatalf("deny %s the right to list %s: %v %s", account.Username, dir, err, out)
	}
	t.Cleanup(func() {
		// The test's own context has ended by now. Taking the list off lets
		// the temporary folder be removed.
		//nolint:gosec // G204: the folder is the test's own temporary one
		_ = exec.CommandContext(context.Background(), "/bin/chmod", "-N", dir).Run()
	})
	if listable, err := fsperm.Listable(dir); err != nil || listable {
		t.Fatalf("Listable = %t, %v; want false once the list denies listing, or the test proves nothing", listable, err)
	}

	if got, err := fsperm.ListableOnceRegained(dir); err != nil || got {
		t.Errorf("ListableOnceRegained = %t, %v; want false", got, err)
	}
	if changed, err := fsperm.RegainList(dir); changed || !errors.Is(err, fsperm.ErrStillLocked) {
		t.Errorf("RegainList = %t, %v; want false and %v", changed, err, fsperm.ErrStillLocked)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("after RegainList, the folder is %v, %v; want mode 0700 as it was", info, err)
	}
}
