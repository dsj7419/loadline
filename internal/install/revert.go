package install

import (
	"fmt"

	"github.com/tidwall/gjson"
)

// Revert returns settings with everything of Loadline's taken out, and the
// changes that makes: every hook handler that runs loadline, loadline.exe or
// exe with the hook subcommand, along with the matcher groups and events
// left empty; the tap prefix on the status line, restoring the user's own
// command, or the whole status line when tap printed its own; and the
// OpenTelemetry keys, when they are Loadline's. An env, hooks or statusLine
// object that only Loadline's entries filled is removed too, so reverting a
// plan gives back the bytes it was made from. Invalid JSON is
// ErrInvalidSettings.
func Revert(settings []byte, exe string) ([]byte, []Change, error) {
	doc, err := parseDocument(settings)
	if err != nil {
		return nil, nil, err
	}

	r := &reverter{doc: doc, exe: exe}
	for _, step := range []func() error{r.hooks, r.statusLine, r.env} {
		if err := step(); err != nil {
			return nil, nil, err
		}
	}
	return doc.bytes(), r.changes, nil
}

type reverter struct {
	doc     *document
	exe     string
	changes []Change
}

func (r *reverter) record(path, action, detail string) {
	r.changes = append(r.changes, Change{Path: path, Action: action, Detail: detail})
}

func (r *reverter) hooks() error {
	hooks := r.doc.get("hooks")
	if !hooks.IsObject() {
		return nil
	}

	var events []string
	hooks.ForEach(func(key, _ gjson.Result) bool {
		events = append(events, key.Str)
		return true
	})
	removed := false
	for _, event := range events {
		n, err := r.hookEvent(event)
		if err != nil {
			return err
		}
		removed = removed || n > 0
	}
	if removed && r.doc.isEmpty("hooks") {
		return r.doc.removeMember("", "hooks")
	}
	return nil
}

// hookEvent removes Loadline's handlers from one event and returns how many
// it removed. A matcher group left without handlers goes, and so does the
// event when it has no groups left.
func (r *reverter) hookEvent(event string) (int, error) {
	path := "hooks." + gjson.Escape(event)
	groups := elements(r.doc.get(path))
	removed := 0
	for gi := len(groups) - 1; gi >= 0; gi-- {
		handlers := elements(groups[gi].Get("hooks"))
		var own []int
		for hi, handler := range handlers {
			if isOwnHandler(handler, r.exe) {
				own = append(own, hi)
			}
		}
		if len(own) == 0 {
			continue
		}

		if len(own) == len(handlers) {
			if err := r.doc.removeElement(path, gi); err != nil {
				return 0, err
			}
		} else {
			for i := len(own) - 1; i >= 0; i-- {
				if err := r.doc.removeElement(fmt.Sprintf("%s.%d.hooks", path, gi), own[i]); err != nil {
					return 0, err
				}
			}
		}
		for _, hi := range own {
			r.record("hooks."+event, ActionRemove, handlers[hi].Get("command").Str+" "+hookArg)
		}
		removed += len(own)
	}

	if removed > 0 && r.doc.isEmpty(path) {
		if err := r.doc.removeMember("hooks", event); err != nil {
			return 0, err
		}
	}
	return removed, nil
}

func (r *reverter) statusLine() error {
	line := r.doc.get("statusLine")
	command := line.Get("command")
	if !line.IsObject() || command.Type != gjson.String {
		return nil
	}
	tap, ours := parseTapCommand(command.Raw, r.exe)
	if !ours {
		return nil
	}

	if tap.print {
		if err := r.doc.removeMember("", "statusLine"); err != nil {
			return err
		}
		r.record("statusLine", ActionRemove, command.Str)
		return nil
	}
	original := tap.originalLiteral()
	if err := r.doc.set("statusLine.command", []byte(original)); err != nil {
		return err
	}
	r.record("statusLine.command", ActionUpdate, decodeString(original))
	return nil
}

func (r *reverter) env() error {
	env := r.doc.get("env")
	if !env.IsObject() || !ownsTelemetry(env) {
		return nil
	}

	for _, v := range telemetryEnv(0, "") {
		current := r.doc.get("env." + v.key)
		if !current.Exists() {
			continue
		}
		if err := r.doc.removeMember("env", v.key); err != nil {
			return err
		}
		r.record("env."+v.key, ActionRemove, string(MaskToken([]byte(current.String()))))
	}
	if r.doc.isEmpty("env") {
		return r.doc.removeMember("", "env")
	}
	return nil
}
