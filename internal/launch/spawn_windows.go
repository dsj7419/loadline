package launch

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// serverAttr starts the server in a process group of its own, so that a
// Ctrl+C in the terminal that started its spawner never reaches it, and
// with no console at all: a console program started from a hook would
// otherwise flash a console window. Hiding its window as well covers any
// window it might still be given.
func serverAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS | windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
}

// browserAttr starts a browser as it is: a windowed program uses no console,
// and one told to start hidden keeps its first window hidden, as does the
// URL handler, which passes that on to the browser it starts.
func browserAttr() *syscall.SysProcAttr { return nil }
