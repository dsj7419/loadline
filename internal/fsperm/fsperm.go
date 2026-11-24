// Package fsperm keeps the directories Loadline writes its secrets to
// private to the user who runs it, and tells when one is not. The data
// directory holds the ingest token and the binary every hook runs, and
// LOADLINE_HOME can put it in a folder that lets other users in.
package fsperm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoDriveLetter is why the folders that really hold a path cannot be found
// on a volume that Windows gives no drive letter to: it names them on such a
// volume only by the volume's GUID name, which Loadline judges no folder by.
var ErrNoDriveLetter = errors.New("the volume has no drive letter")

// Entry is what Inspect tells of a path itself.
type Entry struct {
	// Link reports a symbolic link, or on Windows a junction or any other
	// reparse point but a deduplicated file, which opening the path would
	// follow somewhere else, or may: Loadline cannot tell where a reparse
	// point of a kind it does not know leads.
	Link bool
	// Reparse is the kind of reparse point the path is, which says what
	// kind of link it is: on systems other than Windows, a symbolic link is
	// a NameSurrogate.
	Reparse ReparseKind
	// Tag is the reparse point's tag, which names its kind to Windows, or 0.
	Tag uint32
	// Folder reports a folder.
	Folder bool
	// Names is how many names the file has: more than one when it was hard
	// linked, and it is then also wherever its other names are.
	Names uint64
	// ID tells the file from every other on the machine: two names with
	// the same ID are names of one file.
	ID FileID
}

// ReparseKind is the kind of reparse point a path is, as its tag says.
type ReparseKind uint8

const (
	// NotReparse is a path that is no reparse point.
	NotReparse ReparseKind = iota
	// NameSurrogate is a link that stands for another path, such as a
	// symbolic link or a junction.
	NameSurrogate
	// CloudFile is a file or folder a cloud service keeps in sync, such as
	// a OneDrive placeholder.
	CloudFile
	// Deduplicated is a file whose data deduplication keeps in a store it
	// shares with other files on the volume. It leads nowhere else: the
	// system reads it as the file it was.
	Deduplicated
	// OtherReparse is a reparse point of a kind Loadline does not know.
	OtherReparse
)

// FileID is a file's identity: the volume it is on and its number there. On
// Windows that number is the file's 128-bit ID, or on a volume that cannot
// give it, the 64-bit file index, which ReFS does not keep unique. form says
// which: a volume serial number has 64 bits beside the full ID and 32 beside
// the index, so numbers of the two forms can match for two different files.
// The zero FileID is of neither form, and no file's.
type FileID struct {
	form   idForm
	volume uint64
	id     [16]byte
}

// idForm is the form of a FileID's numbers.
type idForm uint8

const (
	// fullID is Windows's 128-bit file ID beside its volume's 64-bit
	// serial number.
	fullID idForm = iota + 1
	// indexID is a 64-bit number in the low 8 bytes beside its volume's:
	// Windows's file index and 32-bit volume serial number, or the inode
	// and device numbers of Unix.
	indexID
)

// FullFileID is the ID of a file as Windows gives it in FILE_ID_INFO: the
// 64-bit serial number of its volume and its 128-bit ID there. Inspect makes
// each full ID with it, and a test that stands in for Inspect can too.
func FullFileID(volume uint64, id [16]byte) FileID {
	return FileID{form: fullID, volume: volume, id: id}
}

// indexFileID is the ID of the file numbered index on volume, with index in
// the low 8 bytes, where NTFS's full ID holds it.
func indexFileID(volume, index uint64) FileID {
	id := FileID{form: indexID, volume: volume}
	binary.LittleEndian.PutUint64(id.id[:8], index)
	return id
}

// andList joins names as a sentence lists them: "a", "a and b", "a, b and c".
func andList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	last := len(names) - 1
	return strings.Join(names[:last], ", ") + " and " + names[last]
}

// Owner is the owner of a path that Loadline does not trust with it, as
// ForeignOwner tells. The zero Owner is one it trusts.
type Owner struct {
	// Name names the owner in the words a report gives.
	Name string
	// Gone reports an account this computer no longer has, as after a
	// profile was moved or restored, which can never sign in again. Only
	// Windows tells: elsewhere it is always false.
	Gone bool
}

// Movable is what MovableAncestor found: the folder someone else could move
// aside, and why.
type Movable struct {
	// Folder is the nearest folder that someone other than the user, SYSTEM
	// or Administrators could delete or rename, or give themselves the right
	// to.
	Folder string
	// Problem says so in a sentence, which names Folder.
	Problem string
	// Owner is Folder's owner when that is why, or zero when its access list
	// is.
	Owner Owner
}

// MovableAncestor looks at dir and the folders above it that already exist,
// up to the volume root, or when top is set, up to top, which it trusts with
// every folder above it. It returns the nearest one that is not safe, which
// lets someone other than the user, SYSTEM or Administrators delete or
// rename it, or give themselves the right to, and so put a folder of their
// own in its place, or the zero Movable when none can. Folders on the way
// that do not exist yet are left out: Loadline creates them private.
//
// A folder is not safe when it belongs to anyone else, who may always
// change its permissions, or when it lets anyone else delete or rename
// itself, or what it holds, or change its permissions or owner. dir itself
// is judged by its owner alone, since making it private replaces the rest.
//
// A link on the way, a junction or a symbolic link, or on Windows a drive
// letter that subst made, puts dir among the folders that hold the link's
// target, which dir's own names do not show. So the folders that really
// hold dir are judged too, as the system resolves them, against top
// resolved the same way.
func MovableAncestor(dir, top string) (Movable, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Movable{}, fmt.Errorf("check the folders above %s: %w", dir, err)
	}
	if found, err := movableChain(dir, top); err != nil || found.Problem != "" {
		return found, err
	}
	held, err := resolved(dir)
	if err != nil {
		return Movable{}, unresolved(dir, err)
	}
	if samePath(held, dir) {
		return Movable{}, nil
	}
	heldTop := top
	if top != "" {
		if heldTop, err = resolved(top); err != nil {
			return Movable{}, unresolved(top, err)
		}
	}
	found, err := movableChain(held, heldTop)
	if err != nil || found.Problem == "" {
		return Movable{}, err
	}
	// dir stays as the user gave it, so that they know it, as does any
	// folder above it found by dir's own names. held, and the folder found
	// among those that really hold dir, are as the system resolved them,
	// which is the truth on disk: every link followed and, on Windows, each
	// name in its long form, whatever short 8.3 name dir gave.
	found.Problem = dir + " is really " + held + ", and " + found.Problem
	return found, nil
}

// unresolved says why the folders that really hold path cannot be found,
// err, and what to do. They go unjudged then, so such a path is refused; one
// in the user's profile resolves.
func unresolved(path string, err error) error {
	if errors.Is(err, ErrNoDriveLetter) {
		return &sentenceError{
			sentence: path + " is on a volume Windows gives no drive letter to, as some RAM disks, container mounts and network redirectors are, " +
				"so Loadline cannot check the folders that hold it: set LOADLINE_HOME to a folder on a disk with a drive letter, such as one in your user profile",
			cause: err,
		}
	}
	return fmt.Errorf("find the folders that hold %s: %w; set LOADLINE_HOME to a folder in your user profile", path, err)
}

// sentenceError says in a sentence of its own what went wrong and what to do,
// in place of its cause's message. errors.Is and errors.As still find the
// cause.
type sentenceError struct {
	sentence string
	cause    error
}

func (e *sentenceError) Error() string { return e.sentence }

func (e *sentenceError) Unwrap() error { return e.cause }

// movableChain judges dir and the folders above it, as MovableAncestor
// does, by the names in dir alone.
func movableChain(dir, top string) (Movable, error) {
	if exists(dir) {
		if found, err := foreignOwner(dir); err != nil || found.Problem != "" {
			return found, err
		}
	}
	for x := dir; ; x = filepath.Dir(x) {
		parent := filepath.Dir(x)
		if parent == x {
			return Movable{}, nil
		}
		if x != dir && exists(x) {
			if found, err := byList(x, movable); err != nil || found.Problem != "" {
				return found, err
			}
		}
		if top != "" && samePath(parent, top) {
			return Movable{}, nil
		}
		// Every folder between dir and top is judged here, as the one that
		// holds the folder below it, before it is judged as itself.
		if exists(parent) {
			if found, err := foreignOwner(parent); err != nil || found.Problem != "" {
				return found, err
			}
			if found, err := byList(parent, emptiable); err != nil || found.Problem != "" {
				return found, err
			}
		}
	}
}

// foreignOwner reports folder as belonging to someone other than those
// Loadline trusts with it (ForeignOwner), or the zero Movable when it does
// not.
func foreignOwner(folder string) (Movable, error) {
	owner, err := ForeignOwner(folder)
	if err != nil || owner == (Owner{}) {
		return Movable{}, err
	}
	return Movable{
		Folder:  folder,
		Problem: folder + " belongs to " + owner.Name + ", who can give themselves the right to delete or rename it, or what it holds",
		Owner:   owner,
	}, nil
}

// byList reports folder as one that judge, movable or emptiable, finds its
// access list lets someone else take, or the zero Movable when it does not.
func byList(folder string, judge func(string) (string, error)) (Movable, error) {
	problem, err := judge(folder)
	if err != nil || problem == "" {
		return Movable{}, err
	}
	return Movable{Folder: folder, Problem: problem}, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// FinalPath returns the path the system opens for path, which must exist:
// every link on the way followed, on Windows each junction and subst drive
// too, and each name in its long form. On Windows it comes without the \\?\
// prefix, a path on a share as \\server\share\…, and on a volume that has no
// drive letter, and so no such path, FinalPath fails with ErrNoDriveLetter.
// filepath.EvalSymlinks cannot stand in for it on Windows: it fails at a
// junction on the way, which it takes for neither a folder nor a link.
func FinalPath(path string) (string, error) { return finalPath(path) }

// Resolved returns path, made absolute, as the system resolves it: every
// link in it followed and, on Windows, each short 8.3 name in its long form,
// as far as path exists, and the rest of it as written. It names the folder
// that InProfile judges, and that a command given path would act on.
func Resolved(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err == nil {
		abs, err = resolved(abs)
	}
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	return abs, nil
}

// resolved returns path with every link in it followed (finalPath), as far
// as path exists, and the rest of it as written. The rest does not exist
// yet, and Loadline creates it private.
func resolved(path string) (string, error) {
	var rest []string
	for existing := path; ; existing = filepath.Dir(existing) {
		if exists(existing) {
			final, err := finalPath(existing)
			if err != nil {
				return "", err
			}
			return filepath.Join(append([]string{final}, rest...)...), nil
		}
		if filepath.Dir(existing) == existing {
			return path, nil
		}
		rest = append([]string{filepath.Base(existing)}, rest...)
	}
}
