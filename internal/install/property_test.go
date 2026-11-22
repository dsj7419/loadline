package install_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/tidwall/gjson"

	"github.com/dsj7419/loadline/internal/install"
)

// node is a JSON value whose object members keep their order, so a generated
// settings file can be laid out the way a person or another tool might have
// written it.
type node struct {
	raw    string // the literal of a scalar; empty for containers
	keys   []string
	items  []node
	array  bool
	inline bool // laid out on one line even in an indented file
}

func scalar(raw string) node { return node{raw: raw} }

func (n *node) add(key string, value node) {
	n.keys = append(n.keys, key)
	n.items = append(n.items, value)
}

type layout struct {
	indent       string // empty for a file on one line
	newline      string
	bom          bool
	finalNewline bool
}

func (l layout) render(root node) []byte {
	var b strings.Builder
	if l.bom {
		b.WriteString(utf8BOM)
	}
	root.write(&b, l, "", false)
	if l.finalNewline {
		b.WriteString(l.newline)
	}
	return []byte(b.String())
}

// write lays n out the way JSON.stringify does with the layout's indent, or
// on one line with ", " and ": " separators when n is inline.
func (n node) write(b *strings.Builder, l layout, prefix string, inline bool) {
	if n.raw != "" {
		b.WriteString(n.raw)
		return
	}
	open, closing := "{", "}"
	if n.array {
		open, closing = "[", "]"
	}
	b.WriteString(open)
	if len(n.items) == 0 {
		b.WriteString(closing)
		return
	}
	oneLine := inline || n.inline || l.indent == ""
	colon, comma := ": ", ", "
	if l.indent == "" {
		colon, comma = ":", ","
	}
	inner := prefix + l.indent
	for i, item := range n.items {
		switch {
		case i > 0 && oneLine:
			b.WriteString(comma)
		case i > 0:
			b.WriteString(",")
		}
		if !oneLine {
			b.WriteString(l.newline + inner)
		}
		if !n.array {
			key, _ := json.Marshal(n.keys[i])
			b.WriteString(string(key) + colon)
		}
		item.write(b, l, inner, oneLine)
	}
	if !oneLine {
		b.WriteString(l.newline + prefix)
	}
	b.WriteString(closing)
}

// generator draws settings files of the shapes Claude Code users have:
// unrelated keys, hooks of their own, a status line and environment
// variables, encoded by whichever tool last wrote the file.
type generator struct{ r *rand.Rand }

func (g generator) chance(p float64) bool { return g.r.Float64() < p }

func pick[T any](g generator, from []T) T { return from[g.r.IntN(len(from))] }

// some returns between 1 and limit distinct elements of from.
func some[T any](g generator, from []T, limit int) []T {
	shuffled := append([]T(nil), from...)
	g.r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	return shuffled[:1+g.r.IntN(min(limit, len(shuffled)))]
}

var awkwardRunes = []rune(`abcxyz019 -_./~|&<>$'"\` + "\t’‘‚‛é😀")

// text draws a string that is hard to quote.
func (g generator) text(minLen int) string {
	runes := make([]rune, minLen+g.r.IntN(12))
	for i := range runes {
		runes[i] = pick(g, awkwardRunes)
	}
	return string(runes)
}

// literal encodes s, choosing among the escapes JSON allows.
func (g generator) literal(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' && g.chance(0.8):
			b.WriteString(`\"`)
		case r == '\\' && g.chance(0.8):
			b.WriteString(`\\`)
		case r == '\t' && g.chance(0.8):
			b.WriteString(`\t`)
		case r == '/' && g.chance(0.2):
			b.WriteString(`\/`)
		case r == '"' || r == '\\' || r < 0x20 || g.chance(0.1):
			for _, unit := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, `\u%04x`, unit)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// foreignHandlers are hooks of other tools, several of which look like
// Loadline's at a glance.
var foreignHandlers = []string{
	`{"type": "command", "command": "node format.js"}`,
	`{"type": "command", "command": "C:/tools/loadline-helper.exe", "args": ["hook"]}`,
	`{"type": "command", "command": "C:/x/loadline.exe", "args": ["tap"]}`,
	`{"type": "command", "command": "\"C:/x/loadline.exe\" hook", "timeout": 5}`,
	`{"type": "command", "command": "bash -c 'echo hi'", "async": true}`,
}

func (g generator) handler() node {
	handler := node{inline: g.chance(0.3)}
	gjson.Parse(pick(g, foreignHandlers)).ForEach(func(key, value gjson.Result) bool {
		if !value.IsArray() {
			handler.add(key.Str, scalar(value.Raw))
			return true
		}
		args := node{array: true, inline: g.chance(0.5)}
		for _, v := range value.Array() {
			args.items = append(args.items, scalar(v.Raw))
		}
		handler.add(key.Str, args)
		return true
	})
	return handler
}

func (g generator) hooks() node {
	var hooks node
	for _, event := range some(g, []string{"Stop", "PostToolUse", "PreToolUse", "Notification", "SessionStart", "TeammateIdle"}, 3) {
		groups := node{array: true, inline: g.chance(0.1)}
		for range 1 + g.r.IntN(2) {
			var group node
			if g.chance(0.5) {
				group.add("matcher", scalar(g.literal(pick(g, []string{"Write|Edit", "Bash", ""}))))
			}
			handlers := node{array: true}
			for range 1 + g.r.IntN(2) {
				handlers.items = append(handlers.items, g.handler())
			}
			group.add("hooks", handlers)
			groups.items = append(groups.items, group)
		}
		hooks.add(event, groups)
	}
	return hooks
}

func (g generator) statusLine() node {
	line := node{inline: g.chance(0.2)}
	line.add("type", scalar(`"command"`))
	line.add("command", scalar(g.literal(g.text(1))))
	if g.chance(0.5) {
		line.add("padding", scalar(fmt.Sprint(g.r.IntN(3))))
	}
	if g.chance(0.5) {
		line.add("refreshInterval", scalar(fmt.Sprint(1+g.r.IntN(10))))
	}
	return line
}

func (g generator) env() node {
	keys := []string{"EDITOR", "CLAUDE_CODE_USE_POWERSHELL_TOOL", "BASH_DEFAULT_TIMEOUT_MS", "DISABLE_AUTOUPDATER"}
	if g.chance(0.2) {
		keys = append(keys, "OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	env := node{inline: g.chance(0.2)}
	for _, key := range some(g, keys, 3) {
		env.add(key, scalar(g.literal(g.text(0))))
	}
	return env
}

func (g generator) settings() node {
	var root node
	if g.chance(0.5) {
		root.add("model", scalar(g.literal("opus")))
	}
	if g.chance(0.4) {
		var permissions node
		permissions.add("allow", node{array: true, items: []node{scalar(`"Bash(git status)"`)}, inline: g.chance(0.5)})
		permissions.add("deny", node{array: true})
		root.add("permissions", permissions)
	}
	if g.chance(0.6) {
		root.add("hooks", g.hooks())
	}
	if g.chance(0.6) {
		root.add("statusLine", g.statusLine())
	}
	if g.chance(0.6) {
		root.add("env", g.env())
	}
	if g.chance(0.3) {
		root.add("includeCoAuthoredBy", scalar("false"))
	}
	g.r.Shuffle(len(root.keys), func(i, j int) {
		root.keys[i], root.keys[j] = root.keys[j], root.keys[i]
		root.items[i], root.items[j] = root.items[j], root.items[i]
	})
	return root
}

func (g generator) layout() layout {
	return layout{
		indent:       pick(g, []string{"  ", "    ", "\t", ""}),
		newline:      pick(g, []string{"\n", "\r\n"}),
		bom:          g.chance(0.2),
		finalNewline: g.chance(0.7),
	}
}

func countLoadlineHandlers(settings []byte, exe string) int {
	count := 0
	gjson.GetBytes(settings, "hooks").ForEach(func(_, groups gjson.Result) bool {
		for _, group := range groups.Array() {
			for _, h := range group.Get("hooks").Array() {
				args := h.Get("args").Array()
				if h.Get("command").Str == exe && len(args) == 1 && args[0].Str == "hook" && h.Get("async").Bool() {
					count++
				}
			}
		}
		return true
	})
	return count
}

// TestPlanRevert_GeneratedSettings checks the properties install relies on
// across many layouts and shapes: the result is valid, keeps the file's BOM
// and line endings, wires every hook, is stable when planned again, and
// reverts to the original bytes.
func TestPlanRevert_GeneratedSettings(t *testing.T) {
	g := generator{r: rand.New(rand.NewPCG(2026, 1121))} //nolint:gosec // G404: a fixed seed makes every run check the same cases
	for i := range 400 {
		l := g.layout()
		input := l.render(g.settings())
		o := options(pick(g, shells))
		o.Exe = pick(g, []string{goldenExe, spacedExe, otherExe})

		out, _, _, err := install.Plan(input, o)
		if err != nil {
			t.Fatalf("case %d: Plan(%q): %v", i, input, err)
		}

		body := bytes.TrimPrefix(out, []byte(utf8BOM))
		if !json.Valid(body) || bytes.HasPrefix(out, []byte(utf8BOM)) != l.bom {
			t.Fatalf("case %d: Plan(%q) = %q, not valid JSON with the input's BOM", i, input, out)
		}
		// A file with no line break at all has no line ending to follow.
		usesCRLF := bytes.Contains(input, []byte("\r\n"))
		if crlfs := bytes.Count(body, []byte("\r\n")); usesCRLF && crlfs != bytes.Count(body, []byte("\n")) || !usesCRLF && crlfs != 0 {
			t.Fatalf("case %d: Plan(%q) = %q does not keep the file's line endings", i, input, out)
		}
		if got := countLoadlineHandlers(body, o.Exe); got != 13 {
			t.Fatalf("case %d: Plan(%q) wired %d hooks, want 13:\n%s", i, input, got, out)
		}
		again, changes, _, err := install.Plan(out, o)
		if err != nil || !bytes.Equal(again, out) || install.HasEdits(changes) {
			t.Fatalf("case %d: re-planning %q gave %q, %+v, %v", i, out, again, changes, err)
		}
		for _, exe := range []string{o.Exe, ""} {
			reverted, _, err := install.Revert(out, exe)
			if err != nil || !bytes.Equal(reverted, input) {
				t.Fatalf("case %d: Revert(Plan(x), %q) = %q, %v\nwant %q", i, exe, reverted, err, input)
			}
		}
	}
}

// FuzzPlan checks that, for any valid JSON, Plan either refuses with
// ErrInvalidSettings or produces valid JSON that is stable when planned again,
// and that reverting it gives valid JSON with nothing left to revert.
func FuzzPlan(f *testing.F) {
	for _, seed := range []string{
		`{}`,
		`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "loadline", "args": ["hook"]}]}]}}`,
		`{"statusLine": {"type": "command", "command": "& \"C:/x/loadline.exe\" tap --run 'a''b'"}}`,
		`{"env": {"OTEL_EXPORTER_OTLP_LOGS_HEADERS": "x-loadline-token=1", "OTEL_LOGS_EXPORTER": 1}}`,
		`{"a":[],"hooks":{},"env":{}}`,
	} {
		f.Add([]byte(seed), false)
	}
	fixtures, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil {
		f.Fatal(err)
	}
	for _, fixture := range fixtures {
		data, err := os.ReadFile(fixture)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data, true)
	}

	f.Fuzz(func(t *testing.T, settings []byte, powershell bool) {
		if !json.Valid(bytes.TrimPrefix(settings, []byte(utf8BOM))) {
			return
		}
		o := options(install.ShellBash)
		if powershell {
			o.Shell = install.ShellPowerShell
		}

		out, _, _, err := install.Plan(settings, o)
		if err != nil {
			if !errors.Is(err, install.ErrInvalidSettings) {
				t.Fatalf("Plan(%q): unexpected error %v", settings, err)
			}
			return
		}
		if !json.Valid(bytes.TrimPrefix(out, []byte(utf8BOM))) {
			t.Fatalf("Plan(%q) = %q, not valid JSON", settings, out)
		}
		again, changes, _, err := install.Plan(out, o)
		if err != nil || !bytes.Equal(again, out) || install.HasEdits(changes) {
			t.Fatalf("re-planning %q gave %q, %+v, %v", out, again, changes, err)
		}
		reverted, _, err := install.Revert(out, o.Exe)
		if err != nil || !json.Valid(bytes.TrimPrefix(reverted, []byte(utf8BOM))) {
			t.Fatalf("Revert(%q) = %q, %v", out, reverted, err)
		}
		if twice, removed, err := install.Revert(reverted, o.Exe); err != nil || len(removed) != 0 || !bytes.Equal(twice, reverted) {
			t.Fatalf("reverting %q again gave %q, %+v, %v", reverted, twice, removed, err)
		}
	})
}
