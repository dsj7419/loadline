//go:build windows

package protocol_test

import "testing"

func TestEnvelopeValidate_WindowsConfigDirs(t *testing.T) {
	tests := []struct {
		dir     string
		wantErr bool
	}{
		{dir: `C:\Users\example\.claude`},
		{dir: `C:/Users/example/.claude`},
		{dir: `\\server\share\.claude`},
		{dir: `C:.claude`, wantErr: true},
		{dir: `C:`, wantErr: true},
		{dir: `\Users\example\.claude`, wantErr: true},
		{dir: `/Users/example/.claude`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			env := validEnvelope(t)
			env.ConfigDir = tt.dir

			err := env.Validate()

			if tt.wantErr && err == nil {
				t.Errorf("Validate() accepted %q", tt.dir)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() rejected %q: %v", tt.dir, err)
			}
		})
	}
}
