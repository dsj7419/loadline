// Package logfile is an append-only log file that rotates itself by size,
// keeping a few of the files it rotated out.
package logfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// File is a log file that rotates once it reaches its size limit. It is safe
// for concurrent use, and each Write lands whole in one file.
type File struct {
	path     string
	maxBytes int64
	keep     int

	mu   sync.Mutex
	file *os.File
	size int64
	// limit is the size past which the next write rotates the file:
	// maxBytes, or further on after a rotation failed.
	limit  int64
	closed bool
}

// Open opens the log at path for appending, creating it, user-only, and its
// directory when they do not exist. Before a write that would take the file
// past maxBytes, the file becomes path.1, an older path.1 becomes path.2 and
// so on up to path.<keep>, the oldest beyond that is dropped, and the write
// goes to a new file. A write longer than maxBytes is still written whole.
func Open(path string, maxBytes int64, keep int) (*File, error) {
	if maxBytes <= 0 || keep < 0 {
		return nil, fmt.Errorf("open log file %s: a limit of %d bytes keeping %d files is not one", path, maxBytes, keep)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log file directory: %w", err)
	}
	f := &File{path: path, maxBytes: maxBytes, keep: keep, limit: maxBytes}
	if err := f.open(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *File) open() error {
	file, err := os.OpenFile(filepath.Clean(f.path), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("open log file: %w", err)
	}
	f.file, f.size = file, info.Size()
	return nil
}

// Write appends p, rotating the file first when p would take it past its
// limit.
func (f *File) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	if f.file != nil && f.size > 0 && f.size+int64(len(p)) > f.limit {
		f.rotate()
	}
	if f.file == nil {
		if err := f.open(); err != nil {
			return 0, err
		}
	}
	n, err := f.file.Write(p)
	f.size += int64(n)
	if err != nil {
		return n, fmt.Errorf("write log file: %w", err)
	}
	return n, nil
}

// rotate moves the current file aside and opens a new one. When the move
// fails, as it does on Windows while another program holds the file open
// without sharing it, writing goes on in the current file, which is tried
// again once another maxBytes have been written there: a log that grows past
// its limit is better than records lost. When the reopen fails, the next
// write tries again.
func (f *File) rotate() {
	_ = f.file.Close() // written with no buffering, so nothing is lost
	f.file = nil
	shiftErr := f.shift()
	if err := f.open(); err != nil {
		return
	}
	f.limit = f.maxBytes
	if shiftErr != nil {
		f.limit = f.size + f.maxBytes
	}
}

// shift renames path.<n> to path.<n+1>, from the oldest kept down to the
// current file, which becomes path.1. A rename replaces the file it lands on,
// which drops the oldest. With no old files to keep, the current one goes.
func (f *File) shift() error {
	if f.keep == 0 {
		return ignoreMissing(os.Remove(f.path))
	}
	for i := f.keep; i >= 1; i-- {
		if err := ignoreMissing(os.Rename(f.name(i-1), f.name(i))); err != nil {
			return err
		}
	}
	return nil
}

// name returns the path of the i-th old file, or of the current file for 0.
func (f *File) name(i int) string {
	if i == 0 {
		return f.path
	}
	return f.path + "." + strconv.Itoa(i)
}

func ignoreMissing(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Close closes the file. Writes after it fail with os.ErrClosed.
func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	if err != nil {
		return fmt.Errorf("close log file: %w", err)
	}
	return nil
}
