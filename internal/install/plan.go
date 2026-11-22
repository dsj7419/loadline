// Package install wires Loadline into a Claude Code settings.json and takes it
// out again: async hooks for the session lifecycle, the tap prefix on the
// status line, and the OpenTelemetry environment that reports per-request
// usage.
//
// Plan and Revert work on bytes and never touch the file system, so a change
// can be shown as a diff before Apply writes it. Their edits are made in place:
// formatting, line endings, a byte-order mark and every value Loadline does not
// own stay as they were, and reverting a plan gives back the original bytes.
package install

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// Options describe the Loadline installation the settings should point at.
type Options struct {
	// Exe is the absolute path of the installed binary, with forward slashes.
	Exe string
	// Port is the loopback port of the ingest endpoint.
	Port int
	// Token authenticates the OpenTelemetry exporter to the ingest endpoint.
	Token string
	// Shell is the shell Claude Code runs the status line with.
	Shell Shell
}

// The actions of a Change.
const (
	ActionAdd    = "add"
	ActionUpdate = "update"
	ActionRemove = "remove"
	// ActionSkip marks an entry deliberately left alone. It edits nothing.
	ActionSkip = "skip"
)

// Change describes one planned edit.
type Change struct {
	// Path names the value, such as hooks.Stop or env.OTEL_LOGS_EXPORTER.
	Path string
	// Action is ActionAdd, ActionUpdate, ActionRemove or ActionSkip.
	Action string
	// Detail says what the value becomes, or why it is skipped. It never
	// holds the ingest token.
	Detail string
}

// HasEdits reports whether any of the changes edits the file.
func HasEdits(changes []Change) bool {
	return slices.ContainsFunc(changes, func(c Change) bool { return c.Action != ActionSkip })
}

// newStatusLineRefresh is how often, in seconds, a status line that install
// creates refreshes.
const newStatusLineRefresh = 10

// Plan returns settings with Loadline wired in, the changes that makes, and
// warnings about what it had to leave alone:
//
//   - Each hook event Loadline listens to gets an async exec-form handler that
//     runs "<Exe> hook". A Loadline handler that is already there is brought
//     up to date in place.
//   - The status line runs through tap in front of the user's own command,
//     quoted for o.Shell, or tap prints its own line when there is none.
//   - env gets the OpenTelemetry setup, unless telemetry is already configured
//     by someone else; then it is skipped with a warning.
//
// Planning its own output again gives no edits. Invalid JSON, or a hooks or
// env value of the wrong type, is ErrInvalidSettings.
func Plan(settings []byte, o Options) ([]byte, []Change, []string, error) {
	if err := o.validate(); err != nil {
		return nil, nil, nil, err
	}
	doc, err := parseDocument(settings)
	if err != nil {
		return nil, nil, nil, err
	}

	p := &planner{doc: doc, opts: o}
	for _, step := range []func() error{p.hooks, p.statusLine, p.env} {
		if err := step(); err != nil {
			return nil, nil, nil, err
		}
	}
	return doc.bytes(), p.changes, p.warnings, nil
}

func (o Options) validate() error {
	if !isAbsoluteSlashPath(o.Exe) {
		return fmt.Errorf("exe %q must be an absolute path with forward slashes", o.Exe)
	}
	if !utf8.ValidString(o.Exe) {
		return fmt.Errorf("exe %q is not valid UTF-8", o.Exe)
	}
	if i := strings.IndexFunc(o.Exe, unquotable); i >= 0 {
		r, _ := utf8.DecodeRuneInString(o.Exe[i:])
		return fmt.Errorf("exe %q holds %q, which the status-line command cannot quote safely", o.Exe, r)
	}
	if o.Port < 1 || o.Port > 65535 {
		return fmt.Errorf("port %d is outside 1-65535", o.Port)
	}
	if o.Token == "" || !utf8.ValidString(o.Token) || strings.ContainsFunc(o.Token, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return fmt.Errorf("token must be a non-empty word without commas")
	}
	if o.Shell != ShellBash && o.Shell != ShellPowerShell {
		return fmt.Errorf("unknown shell %d", o.Shell)
	}
	return nil
}

// isAbsoluteSlashPath accepts /path, //server/share/path and C:/path.
func isAbsoluteSlashPath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	return len(p) >= 3 && (p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z') && p[1] == ':' && p[2] == '/'
}

// unquotable reports whether r means something inside double quotes in bash
// or PowerShell, or cannot appear in a command line at all. PowerShell takes
// the typographic double quotes as quotes too.
func unquotable(r rune) bool {
	switch r {
	case '"', '\\', '`', '$', '\u201c', '\u201d', '\u201e':
		return true
	default:
		return unicode.IsControl(r)
	}
}

type planner struct {
	doc      *document
	opts     Options
	changes  []Change
	warnings []string
}

func (p *planner) record(path, action, detail string) {
	p.changes = append(p.changes, Change{Path: path, Action: action, Detail: detail})
}

func (p *planner) hooks() error {
	hooks := p.doc.get("hooks")
	if !hooks.Exists() {
		if err := p.doc.appendMember("", "hooks", []byte("{}")); err != nil {
			return err
		}
	} else if !hooks.IsObject() {
		return fmt.Errorf("%w: hooks must be an object", ErrInvalidSettings)
	}

	for _, event := range hookEvents {
		if err := p.hookEvent(event); err != nil {
			return err
		}
	}
	return nil
}

func (p *planner) hookEvent(event hookEvent) error {
	path := "hooks." + event.name
	groups := p.doc.get(path)
	if groups.Exists() && !groups.IsArray() {
		return fmt.Errorf("%w: %s must be an array", ErrInvalidSettings, path)
	}

	found, repaired, err := p.repairHandlers(path, groups)
	switch {
	case err != nil:
		return err
	case repaired:
		p.record(path, ActionUpdate, event.describe())
		return nil
	case found:
		return nil
	}

	group, err := encodeJSON(event.group(p.opts.Exe))
	if err != nil {
		return err
	}
	if groups.Exists() {
		err = p.doc.appendElement(path, group)
	} else {
		err = p.doc.appendMember("hooks", event.name, slices.Concat([]byte("["), group, []byte("]")))
	}
	if err != nil {
		return err
	}
	p.record(path, ActionAdd, event.describe())
	return nil
}

// repairHandlers brings every Loadline handler under one event up to date,
// wherever it sits, and reports whether there was one and whether any needed
// repair.
func (p *planner) repairHandlers(path string, groups gjson.Result) (found, repaired bool, _ error) {
	for gi, group := range elements(groups) {
		for hi, handler := range elements(group.Get("hooks")) {
			if !isOwnHandler(handler, p.opts.Exe) {
				continue
			}
			found = true
			fixed, err := p.repairHandler(fmt.Sprintf("%s.%d.hooks.%d", path, gi, hi), handler)
			if err != nil {
				return false, false, err
			}
			repaired = repaired || fixed
		}
	}
	return found, repaired, nil
}

// repairHandler sets each field of a Loadline handler that differs from what
// install writes, and keeps any field the user added, such as a timeout.
func (p *planner) repairHandler(path string, handler gjson.Result) (bool, error) {
	fields := []struct {
		name  string
		value string
		ok    bool
	}{
		{"type", `"command"`, handler.Get("type").Type == gjson.String && handler.Get("type").Str == "command"},
		{"command", jsonString(p.opts.Exe), handler.Get("command").Str == p.opts.Exe},
		{"args", `["` + hookArg + `"]`, isHookArgs(handler.Get("args"))},
		{"async", "true", handler.Get("async").Type == gjson.True},
	}
	repaired := false
	for _, field := range fields {
		if field.ok {
			continue
		}
		repaired = true
		fieldPath := path + "." + field.name
		var err error
		if p.doc.get(fieldPath).Exists() {
			err = p.doc.set(fieldPath, []byte(field.value))
		} else {
			err = p.doc.appendMember(path, field.name, []byte(field.value))
		}
		if err != nil {
			return false, err
		}
	}
	return repaired, nil
}

// newStatusLine is the status line install adds when there is none.
type newStatusLine struct {
	Type            string          `json:"type"`
	Command         json.RawMessage `json:"command"`
	RefreshInterval int             `json:"refreshInterval"`
}

func (p *planner) statusLine() error {
	line := p.doc.get("statusLine")
	if !line.Exists() {
		command := tapCommand{print: true}.literal(p.opts.Exe, p.opts.Shell)
		value, err := encodeJSON(newStatusLine{Type: "command", Command: json.RawMessage(command), RefreshInterval: newStatusLineRefresh})
		if err != nil {
			return err
		}
		if err := p.doc.appendMember("", "statusLine", value); err != nil {
			return err
		}
		p.record("statusLine", ActionAdd, decodeString(command))
		return nil
	}

	command := line.Get("command")
	if !line.IsObject() || command.Type != gjson.String || command.Str == "" {
		p.record("statusLine", ActionSkip, "it has no command to run")
		p.warnings = append(p.warnings,
			"statusLine has no command, so Loadline left it alone and sessions will report only through hooks")
		return nil
	}

	tap, ours := parseTapCommand(command.Raw, p.opts.Exe)
	if !ours {
		tap = tapCommand{original: splitString(command.Raw)}
	}
	want := tap.literal(p.opts.Exe, p.opts.Shell)
	if decodeString(want) == command.Str {
		return nil
	}
	if err := p.doc.set("statusLine.command", []byte(want)); err != nil {
		return err
	}
	p.record("statusLine.command", ActionUpdate, decodeString(want))
	return nil
}

func (p *planner) env() error {
	env := p.doc.get("env")
	if env.Exists() && !env.IsObject() {
		return fmt.Errorf("%w: env must be an object", ErrInvalidSettings)
	}
	if !ownsTelemetry(env) {
		if keys := strings.Join(telemetryKeys(env), ", "); keys != "" {
			p.record("env", ActionSkip, "telemetry is already configured by "+keys)
			p.warnings = append(p.warnings, "env already sets "+keys+
				", so Loadline left OpenTelemetry alone and the per-model view will be unavailable")
			return nil
		}
	}

	if !env.Exists() {
		if err := p.doc.appendMember("", "env", []byte("{}")); err != nil {
			return err
		}
	}
	for _, v := range telemetryEnv(p.opts.Port, p.opts.Token) {
		path := "env." + v.key
		current := p.doc.get(path)
		literal := []byte(jsonString(v.value))
		detail := string(MaskToken([]byte(v.value)))
		switch {
		case !current.Exists():
			if err := p.doc.appendMember("env", v.key, literal); err != nil {
				return err
			}
			p.record(path, ActionAdd, detail)
		case current.Type != gjson.String || current.Str != v.value:
			if err := p.doc.set(path, literal); err != nil {
				return err
			}
			p.record(path, ActionUpdate, detail)
		}
	}
	return nil
}
