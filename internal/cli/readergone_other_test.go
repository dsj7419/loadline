//go:build !windows

package cli

import (
	"os"
	"syscall"
	"testing"
)

func TestIsReaderGone(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "a write to a pipe with no reader", err: &os.PathError{Op: "write", Path: "|1", Err: syscall.EPIPE}, want: true},
		{name: "a closed file", err: os.ErrClosed},
		{name: "a full disk", err: &os.PathError{Op: "write", Path: "out", Err: syscall.ENOSPC}},
		{name: "no error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isReaderGone(tt.err); got != tt.want {
				t.Errorf("isReaderGone(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
