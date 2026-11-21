// Package domain holds Loadline's core types. It performs no I/O.
package domain

// ConfigDir is the normalized absolute path of a Claude Code configuration
// directory. Loadline keys each account by it.
type ConfigDir string
