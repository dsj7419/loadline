//go:build unix

package launch

import "syscall"

// serverAttr starts the server in a session of its own, with no
// controlling terminal: closing the terminal of the session that spawned
// it does not hang it up, and a signal to its spawner's process group does
// not reach it.
func serverAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// browserAttr starts a browser in a session of its own too, so that closing
// the terminal that ran loadline open does not close the browser it started.
func browserAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
