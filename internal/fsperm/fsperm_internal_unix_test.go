//go:build !windows

package fsperm

import (
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
