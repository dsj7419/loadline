package install

import (
	"path"
	"strings"

	"github.com/tidwall/gjson"
)

// hookArg is the subcommand Loadline's hook handlers run, and with the base
// name of the command it is how Loadline recognizes its own handlers.
const hookArg = "hook"

// hookEvent is a Claude Code hook event Loadline listens to, and the matcher
// that narrows it, if any.
type hookEvent struct {
	name    string
	matcher string
}

// hookEvents are the events install wires, in the order it adds them.
var hookEvents = []hookEvent{
	{name: "SessionStart"},
	{name: "SessionEnd"},
	{name: "UserPromptSubmit"},
	{name: "Stop"},
	{name: "StopFailure"},
	{name: "PermissionRequest"},
	{name: "PostToolUse"},
	{name: "SubagentStart"},
	{name: "SubagentStop"},
	{name: "PreCompact"},
	{name: "PostCompact"},
	{name: "Notification", matcher: "permission_prompt|elicitation_dialog|agent_needs_input|idle_prompt"},
	{name: "PreToolUse", matcher: "AskUserQuestion"},
}

// matcherGroup and hookHandler are the shapes of Claude Code's hook settings,
// with their fields in the order install writes them.
type matcherGroup struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookHandler `json:"hooks"`
}

// hookHandler is an exec-form command hook: Claude Code starts Command with
// Args directly, without a shell, so the path needs no quoting. It runs in
// the background so it never delays the session.
type hookHandler struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Async   bool     `json:"async"`
}

func (e hookEvent) group(exe string) matcherGroup {
	return matcherGroup{
		Matcher: e.matcher,
		Hooks:   []hookHandler{{Type: "command", Command: exe, Args: []string{hookArg}, Async: true}},
	}
}

func (e hookEvent) describe() string {
	if e.matcher == "" {
		return "async loadline hook"
	}
	return "async loadline hook for " + e.matcher
}

// isOwnHandler reports whether a hook handler is Loadline's: it runs
// loadline, or exe, with the hook subcommand.
func isOwnHandler(handler gjson.Result, exe string) bool {
	if !handler.IsObject() || !handler.Get("args").IsArray() {
		return false
	}
	command, first := handler.Get("command"), handler.Get("args.0")
	return command.Type == gjson.String && isLoadlinePath(command.Str, exe) &&
		first.Type == gjson.String && first.Str == hookArg
}

// isHookArgs reports whether args is exactly the argument list install
// writes.
func isHookArgs(args gjson.Result) bool {
	list := elements(args)
	return len(list) == 1 && list[0].Type == gjson.String && list[0].Str == hookArg
}

// isLoadlinePath reports whether p names the Loadline binary: it is exe, or
// its base name is loadline or loadline.exe in any case, as Windows ignores
// case.
func isLoadlinePath(p, exe string) bool {
	if exe != "" && p == exe {
		return true
	}
	base := path.Base(strings.ReplaceAll(p, `\`, "/"))
	return strings.EqualFold(base, "loadline") || strings.EqualFold(base, "loadline.exe")
}
