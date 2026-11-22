package fsperm

import (
	"testing"

	"golang.org/x/sys/windows"
)

// An access list can hold generic rights, which Windows turns into what they
// stand for on a file. The folder checks must count them the same.
func TestFileRights_CountsGenericRightsAsWhatTheyStandFor(t *testing.T) {
	tests := []struct {
		name string
		mask windows.ACCESS_MASK
		want windows.ACCESS_MASK
		not  windows.ACCESS_MASK
	}{
		{name: "all", mask: windows.GENERIC_ALL, want: windows.DELETE | fileDeleteChild},
		{name: "write", mask: windows.GENERIC_WRITE, want: windows.FILE_WRITE_DATA, not: windows.DELETE | fileDeleteChild},
		{name: "read", mask: windows.GENERIC_READ, want: windows.FILE_READ_DATA, not: windows.DELETE},
		{name: "execute", mask: windows.GENERIC_EXECUTE, want: windows.FILE_EXECUTE, not: windows.DELETE},
		{name: "specific", mask: windows.DELETE, want: windows.DELETE, not: windows.GENERIC_ALL},
	}
	for _, tt := range tests {
		got := fileRights(tt.mask)
		if got&tt.want != tt.want || got&tt.not != 0 {
			t.Errorf("fileRights(%s) = %#x, want %#x in it and none of %#x", tt.name, got, tt.want, tt.not)
		}
	}
}
