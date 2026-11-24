//go:build !windows

package fsperm

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ownedByUID makes path, for the rest of the test, read as owned by uid: an
// account that is not root cannot give a folder another owner.
func ownedByUID(t *testing.T, path string, uid uint32) {
	t.Helper()
	previous := ownerOf
	ownerOf = func(p string) (uint32, error) {
		if samePath(p, path) {
			return uid, nil
		}
		return previous(p)
	}
	t.Cleanup(func() { ownerOf = previous })
}

// The owner of a folder may always change its mode, and so let themselves
// rename it, or what it holds. A data directory, or a folder above it, owned
// by another account than the user's or root's is refused.
func TestMovableAncestor_RefusesAFolderThatBelongsToAnotherAccount(t *testing.T) {
	const stranger = 4242
	for _, where := range []string{"the data directory", "the folder that holds it", "a folder further up"} {
		t.Run(where, func(t *testing.T) {
			top := t.TempDir()
			dir := filepath.Join(top, "tools", "inner", "loadline")
			if err := MkdirAll(dir); err != nil {
				t.Fatal(err)
			}
			owned := map[string]string{
				"the data directory":       dir,
				"the folder that holds it": filepath.Dir(dir),
				"a folder further up":      filepath.Dir(filepath.Dir(dir)),
			}[where]
			ownedByUID(t, owned, stranger)

			got, err := MovableAncestor(dir, top)

			if want := owned + " belongs to "; err != nil || !strings.HasPrefix(got.Problem, want) {
				t.Errorf("MovableAncestor = %+v, %v; want it to start %q", got, err, want)
			}
		})
	}
}

func TestMovableAncestor_TakesFoldersRootOwns(t *testing.T) {
	top := t.TempDir()
	dir := filepath.Join(top, "tools", "loadline")
	if err := MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	ownedByUID(t, filepath.Dir(dir), 0)

	if got, err := MovableAncestor(dir, top); err != nil || got != (Movable{}) {
		t.Errorf("MovableAncestor = %+v, %v; want none (uid %d runs the test)", got, err, os.Getuid())
	}
}

// No account reads as gone here: a user ID that the user database Loadline
// reads, /etc/passwd, does not name may be an account of LDAP, SSSD or
// systemd-homed that signs in all the same.
func TestForeignOwner_NeverSaysAnAccountIsGone(t *testing.T) {
	const unnamed = 4242424
	dir := t.TempDir()
	ownedByUID(t, dir, unnamed)

	got, err := ForeignOwner(dir)

	if want := (Owner{Name: "the account with user ID 4242424"}); err != nil || got != want {
		t.Errorf("ForeignOwner of a folder user ID %d owns = %+v, %v; want %+v", unnamed, got, err, want)
	}
}

// Only a folder's owner, or root, may change its mode.
func TestCanChangeList_TellsTheOwnerAndRootFromOthers(t *testing.T) {
	dir := t.TempDir()
	if got, err := CanChangeList(dir); err != nil || !got {
		t.Errorf("CanChangeList of a folder of the user's = %t, %v; want true", got, err)
	}
	ownedByUID(t, dir, 4242)
	if got, err := CanChangeList(dir); err != nil || got != (os.Geteuid() == 0) {
		t.Errorf("CanChangeList of a folder of another account's = %t, %v; want %t", got, err, os.Geteuid() == 0)
	}
}

// OwnerName names root too, which ForeignOwner leaves out.
func TestOwnerName_NamesRootToo(t *testing.T) {
	dir := t.TempDir()
	ownedByUID(t, dir, 0)

	if got, err := OwnerName(dir); err != nil || got != accountName(0) || got == "" {
		t.Errorf("OwnerName = %q, %v; want %q", got, err, accountName(0))
	}
	if got, err := ForeignOwner(dir); err != nil || got != (Owner{}) {
		t.Errorf("ForeignOwner = %+v, %v; want none", got, err)
	}
}

// lockedOutFolder makes a folder of mode 0000 in a temporary folder, which
// the test end puts back to 0700, so that the folder can be removed.
func lockedOutFolder(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root may list any folder, whatever its mode")
	}
	dir := filepath.Join(t.TempDir(), "loadline")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir
}

// checkAfterAnswers makes the check RegainList makes once it has changed the
// mode answer listable and err, for the rest of the test. The check before it
// is the real one.
func checkAfterAnswers(t *testing.T, listable bool, err error) {
	t.Helper()
	previous := canList
	asked := 0
	canList = func(dir string) (bool, error) {
		asked++
		if asked == 1 {
			return previous(dir)
		}
		return listable, err
	}
	t.Cleanup(func() { canList = previous })
}

// modeOf returns the permission bits of path.
func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// When the check after the new mode fails, RegainList cannot tell whether the
// mode helps, and puts the old one back.
func TestRegainList_PutsTheModeBackWhenTheCheckAfterFails(t *testing.T) {
	dir := lockedOutFolder(t)
	failed := errors.New("the check failed")
	checkAfterAnswers(t, false, failed)

	changed, err := RegainList(dir)

	if changed || !errors.Is(err, failed) {
		t.Errorf("RegainList = %t, %v; want false and %v", changed, err, failed)
	}
	if got := modeOf(t, dir); got != 0 {
		t.Errorf("after RegainList, the mode is %#o; want 0 as it was", got)
	}
}

// When the mode cannot be put back, RegainList says so in one sentence that
// names the folder and keeps every cause, and reports the mode changed: the
// owner keeps every right on it.
func TestRegainList_SaysInOneSentenceWhenItCannotPutTheModeBack(t *testing.T) {
	for name, checkErr := range map[string]error{"still not listable": nil, "the check failed": errors.New("the check failed")} {
		t.Run(name, func(t *testing.T) {
			dir := lockedOutFolder(t)
			checkAfterAnswers(t, false, checkErr)
			refused := errors.New("the file system refused")
			previous := chmod
			changes := 0
			chmod = func(path string, mode fs.FileMode) error {
				changes++
				if changes == 2 {
					return refused
				}
				return previous(path, mode)
			}
			t.Cleanup(func() { chmod = previous })

			changed, err := RegainList(dir)

			if !changed || !errors.Is(err, refused) || !strings.HasPrefix(err.Error(), "put back the mode of "+dir) ||
				strings.Contains(err.Error(), "\n") {
				t.Errorf("RegainList = %t, %q; want true and one sentence that starts with putting back the mode of %s, for %v",
					changed, err, dir, refused)
			}
			if checkErr != nil && !errors.Is(err, checkErr) {
				t.Errorf("RegainList = %v, want it to keep %v too", err, checkErr)
			}
			if got := modeOf(t, dir); got != 0o700 {
				t.Errorf("after RegainList, the mode is %#o; want 0700, which it could not take back", got)
			}
		})
	}
}
