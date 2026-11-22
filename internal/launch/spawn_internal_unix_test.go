//go:build unix

package launch

import "testing"

func TestServerCommand_LeadsASessionOfItsOwn(t *testing.T) {
	cmd, err := serverCommand("/usr/local/bin/loadline", "serve")
	if err != nil {
		t.Fatalf("serverCommand: %v", err)
	}

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Errorf("the server starts with %+v, want a session of its own", cmd.SysProcAttr)
	}
	assertNullStreamsInHome(t, cmd)
}

func TestBrowserCommand_LeadsASessionOfItsOwn(t *testing.T) {
	cmd, err := browserCommand("xdg-open", "http://127.0.0.1:8289/")
	if err != nil {
		t.Fatalf("browserCommand: %v", err)
	}

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Errorf("the browser starts with %+v, want a session of its own", cmd.SysProcAttr)
	}
	assertNullStreamsInHome(t, cmd)
}
