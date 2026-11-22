package cli

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestIsReaderGone(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "a write to a pipe being closed", err: &os.PathError{Op: "write", Path: "|1", Err: windows.ERROR_NO_DATA}, want: true},
		{name: "a write to a pipe that has ended", err: &os.PathError{Op: "write", Path: "|1", Err: windows.ERROR_BROKEN_PIPE}, want: true},
		{name: "a closed file", err: os.ErrClosed},
		{name: "a full disk", err: &os.PathError{Op: "write", Path: "out", Err: windows.ERROR_DISK_FULL}},
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
