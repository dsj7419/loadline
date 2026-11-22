package launch

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestServerCommand_HasNoConsoleAndStartsHidden(t *testing.T) {
	cmd, err := serverCommand(`C:\Tools\loadline.exe`, "serve")
	if err != nil {
		t.Fatalf("serverCommand: %v", err)
	}

	attr := cmd.SysProcAttr
	if attr == nil {
		t.Fatal("the server starts with the caller's process attributes, want it detached")
	}
	const want = windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS | windows.CREATE_NO_WINDOW
	if attr.CreationFlags != want {
		t.Errorf("creation flags = %#x, want %#x (new process group, detached, no window)", attr.CreationFlags, want)
	}
	if !attr.HideWindow {
		t.Error("HideWindow = false, want the server told to start hidden")
	}
	assertNullStreamsInHome(t, cmd)
}

// A windowed program told to start hidden keeps its first window hidden,
// and the URL handler passes that on to the browser it starts.
func TestBrowserCommand_ShowsItsWindow(t *testing.T) {
	cmd, err := browserCommand("rundll32", "url.dll,FileProtocolHandler", "http://127.0.0.1:8289/")
	if err != nil {
		t.Fatalf("browserCommand: %v", err)
	}

	if attr := cmd.SysProcAttr; attr != nil && (attr.HideWindow || attr.CreationFlags != 0) {
		t.Errorf("the browser starts with %+v, want nothing that hides or detaches its window", attr)
	}
	assertNullStreamsInHome(t, cmd)
}
