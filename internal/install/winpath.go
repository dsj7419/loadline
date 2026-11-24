package install

import (
	"path"
	"strings"
)

// The shell detection reads Windows paths on every system, so that its tests
// run everywhere, and these functions read them without path/filepath, whose
// rules are the running system's.

// splitWindowsVolume splits the Windows path p into the drive (C:) or share
// (\\host\share) it starts with, "" for neither, and the rest.
func splitWindowsVolume(p string) (volume, rest string) {
	switch {
	case len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]):
		return p[:2], p[2:]
	case len(p) >= 2 && isSeparator(p[0]) && isSeparator(p[1]):
		share := nextSeparator(p, nextSeparator(p, 2)+1)
		return p[:share], p[share:]
	}
	return "", p
}

// isWindowsAbs reports whether the Windows path p names the same folder from
// any working directory: a drive with a separator after it, or a share.
// Other paths, such as cmd, \cmd and C:cmd, depend on the working directory
// or on the folder current on a drive.
func isWindowsAbs(p string) bool {
	volume, rest := splitWindowsVolume(p)
	switch {
	case volume == "":
		return false
	case isSeparator(volume[0]):
		return true
	default:
		return rest != "" && isSeparator(rest[0])
	}
}

// resolveWindows returns the volume and the rest of the Windows path p,
// resolved against workDir as Windows resolves it in a process whose working
// directory that is. workDir must be absolute and p must not be. Which folder
// is current on any other drive is not known here, so a drive-relative p on
// another drive resolves from that drive's root, the folder Windows takes
// when no other has been made current there.
func resolveWindows(workDir, p string) (volume, rest string) {
	workVolume, workRest := splitWindowsVolume(workDir)
	volume, rest = splitWindowsVolume(p)
	switch {
	case volume == "" && rest != "" && isSeparator(rest[0]):
		return workVolume, rest
	case volume == "", strings.EqualFold(volume, workVolume):
		return workVolume, workRest + `\` + rest
	default:
		return volume, `\` + rest
	}
}

// joinWindows joins elems to the root of volume and resolves . and .. as
// Windows does: lexically, and never above that root. That names the file
// Claude Code's own path.join names.
func joinWindows(volume string, elems ...string) string {
	slashed := make([]string, 0, len(elems)+1)
	slashed = append(slashed, "/")
	for _, elem := range elems {
		slashed = append(slashed, strings.ReplaceAll(elem, `\`, "/"))
	}
	return volume + strings.ReplaceAll(path.Join(slashed...), "/", `\`)
}

// nextSeparator returns the index of the first separator in p at or after
// from, or len(p) when there is none.
func nextSeparator(p string, from int) int {
	if from >= len(p) {
		return len(p)
	}
	if i := strings.IndexAny(p[from:], `\/`); i >= 0 {
		return from + i
	}
	return len(p)
}

func isSeparator(c byte) bool { return c == '\\' || c == '/' }

func isASCIILetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
