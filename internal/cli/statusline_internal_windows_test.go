package cli

import (
	"os/exec"
	"slices"
	"testing"
)

// PowerShell 7 runs what Windows PowerShell 5.1 runs, and more besides, such
// as && and ??, so the wrapper uses it when it is installed.
func TestPowerShell_PrefersPowerShell7(t *testing.T) {
	const ps7Path = `C:\Program Files\PowerShell\7\pwsh.exe`
	tests := map[string]struct {
		found map[string]string
		want  string
	}{
		"PowerShell 7 on PATH":    {found: map[string]string{"pwsh.exe": ps7Path, "powershell.exe": `C:\Windows\powershell.exe`}, want: ps7Path},
		"only Windows PowerShell": {found: map[string]string{"powershell.exe": `C:\Windows\powershell.exe`}, want: "powershell.exe"},
		"neither found":           {found: map[string]string{}, want: "powershell.exe"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var asked []string
			lookPath := func(file string) (string, error) {
				asked = append(asked, file)
				if path, ok := tt.found[file]; ok {
					return path, nil
				}
				return "", exec.ErrNotFound
			}

			if got := powerShell(lookPath); got != tt.want {
				t.Errorf("powerShell = %q, want %q", got, tt.want)
			}
			if !slices.Equal(asked, []string{"pwsh.exe"}) {
				t.Errorf("looked up %q, want only pwsh.exe", asked)
			}
		})
	}
}

func TestStatusLineCommand_RunsTheEncodedCommandThroughPowerShell(t *testing.T) {
	cmd := statusLineCommand(t.Context(), "Write-Output 'hi'")

	want := []string{"-NoProfile", "-NonInteractive", "-EncodedCommand", encodedCommand("Write-Output 'hi'")}
	if !slices.Equal(cmd.Args[1:], want) {
		t.Errorf("args = %q, want %q", cmd.Args[1:], want)
	}
	if path, err := exec.LookPath("pwsh.exe"); err == nil && cmd.Path != path {
		t.Errorf("runs %s although PowerShell 7 is at %s", cmd.Path, path)
	}
}
