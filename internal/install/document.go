package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ErrInvalidSettings means a settings file is not valid JSON, or holds a value
// of the wrong type where Loadline has to add its entries. The file is left as
// it is.
var ErrInvalidSettings = errors.New("unusable settings file")

const (
	utf8BOM = "\xef\xbb\xbf"
	// jsonSpace is the whitespace JSON allows between tokens.
	jsonSpace = " \t\r\n"
	// defaultIndent is what Claude Code writes, used when a file has no
	// indentation of its own to copy.
	defaultIndent = "  "
)

// document is a settings file being edited in place. Every edit splices text
// between the bytes already there and lays it out like its neighbors, so the
// file's formatting, key order, escapes and unrelated values all survive, and
// undoing an edit restores the original bytes.
type document struct {
	bom     bool
	body    []byte // the JSON text after the byte-order mark
	newline string // "\n" or "\r\n", whichever the file uses
	indent  string // one level of the file's indentation
	oneLine bool   // the file keeps its whole object on one line
}

// container is an object or array in the document.
type container struct {
	open int // offset of the opening bracket
	end  int // offset just past the closing bracket
	// items are the members of an object or the elements of an array.
	items []item
}

type item struct {
	start int    // offset of the member's key, or of the element
	end   int    // offset just past the value
	key   string // the member's key; empty for elements
}

func parseDocument(data []byte) (*document, error) {
	body, bom := bytes.CutPrefix(data, []byte(utf8BOM))
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("%w: it is not valid JSON", ErrInvalidSettings)
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return nil, fmt.Errorf("%w: it must hold a JSON object", ErrInvalidSettings)
	}
	if err := checkUniqueKeys(root, ""); err != nil {
		return nil, err
	}

	d := &document{
		bom:     bom,
		body:    slices.Clone(body),
		newline: detectNewline(body),
		indent:  detectIndent(body),
	}
	top, err := d.container("")
	if err != nil {
		return nil, err
	}
	// An empty object gives no layout to follow, so it is formatted the way
	// Claude Code would format it.
	d.oneLine = len(top.items) > 0 && !bytes.ContainsRune(d.body[top.open:top.end], '\n')
	return d, nil
}

// checkUniqueKeys refuses an object anywhere in value that has a key twice.
// Claude Code would read the last one, but a JSON path finds the first, so an
// edit would change the copy Claude Code ignores, and an uninstall could
// remove the hooks it runs.
func checkUniqueKeys(value gjson.Result, path string) error {
	var err error
	switch {
	case value.IsObject():
		seen := map[string]bool{}
		value.ForEach(func(key, member gjson.Result) bool {
			if seen[key.Str] {
				where := path
				if where == "" {
					where = "the top level"
				}
				err = fmt.Errorf("%w: %s has the key %q more than once, and Claude Code reads only the last",
					ErrInvalidSettings, where, key.Str)
				return false
			}
			seen[key.Str] = true
			err = checkUniqueKeys(member, joinPath(path, key.Str))
			return err == nil
		})
	case value.IsArray():
		for i, element := range value.Array() {
			if err = checkUniqueKeys(element, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				break
			}
		}
	}
	return err
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func detectNewline(body []byte) string {
	if i := bytes.IndexByte(body, '\n'); i > 0 && body[i-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// detectIndent returns the leading whitespace of the first indented line,
// which in a formatted object is one level deep.
func detectIndent(body []byte) string {
	for line := range bytes.Lines(body) {
		content := bytes.TrimLeft(line, " \t")
		if len(content) < len(line) && len(bytes.Trim(content, jsonSpace)) > 0 {
			return string(line[:len(line)-len(content)])
		}
	}
	return defaultIndent
}

func (d *document) bytes() []byte {
	if d.bom {
		return slices.Concat([]byte(utf8BOM), d.body)
	}
	return d.body
}

func (d *document) get(path string) gjson.Result {
	return gjson.GetBytes(d.body, path)
}

// set replaces the value at path, which must exist, with raw.
func (d *document) set(path string, raw []byte) error {
	if !d.get(path).Exists() {
		return fmt.Errorf("set %s: there is no value there", path)
	}
	body, err := sjson.SetRawBytes(d.body, path, raw)
	if err != nil {
		return fmt.Errorf("set %s: %w", path, err)
	}
	d.body = body
	return nil
}

// container locates the object or array at path; the empty path is the top
// level.
func (d *document) container(path string) (container, error) {
	var open int
	var raw string
	if path == "" {
		open = len(d.body) - len(bytes.TrimLeft(d.body, jsonSpace))
		raw = string(d.body[open:len(bytes.TrimRight(d.body, jsonSpace))])
	} else {
		value := d.get(path)
		if !value.IsObject() && !value.IsArray() {
			return container{}, fmt.Errorf("%s is not an object or an array", path)
		}
		open, raw = value.Index, value.Raw
	}
	if open+len(raw) > len(d.body) || string(d.body[open:open+len(raw)]) != raw {
		return container{}, fmt.Errorf("locate %s in the settings file", path)
	}

	c := container{open: open, end: open + len(raw)}
	parsed := gjson.Parse(raw)
	isObject := parsed.IsObject()
	parsed.ForEach(func(key, value gjson.Result) bool {
		start := value.Index
		if isObject {
			start = key.Index
		}
		c.items = append(c.items, item{start: open + start, end: open + value.Index + len(value.Raw), key: key.Str})
		return true
	})
	return c, nil
}

func (d *document) isEmpty(path string) bool {
	c, err := d.container(path)
	return err == nil && len(c.items) == 0
}

// appendMember adds "key": value, value being compact JSON, as the last
// member of the object at path.
func (d *document) appendMember(path, key string, value []byte) error {
	colon := ": "
	if d.oneLine {
		colon = ":"
	}
	member := jsonString(key) + colon
	return d.appendItem(path, func(indent string, inline bool) ([]byte, error) {
		laidOut, err := d.layOut(value, indent, inline)
		return slices.Concat([]byte(member), laidOut), err
	})
}

// appendElement adds value, compact JSON, as the last element of the array at
// path.
func (d *document) appendElement(path string, value []byte) error {
	return d.appendItem(path, func(indent string, inline bool) ([]byte, error) {
		return d.layOut(value, indent, inline)
	})
}

// render returns an item's text for a line that starts with indent, or for a
// container laid out on one line when inline is set.
type render func(indent string, inline bool) ([]byte, error)

// appendItem adds an item after the last one in the container at path: on a
// line of its own when the items are on lines of their own, otherwise on the
// same line.
func (d *document) appendItem(path string, item render) error {
	c, err := d.container(path)
	if err != nil {
		return err
	}
	if len(c.items) == 0 {
		return d.fill(c, item)
	}

	last := c.items[len(c.items)-1]
	if d.oneLine || !bytes.ContainsRune(d.body[c.open:last.start], '\n') {
		separator := ", "
		if d.oneLine {
			separator = ","
		}
		text, err := item("", true)
		if err != nil {
			return err
		}
		d.splice(last.end, last.end, slices.Concat([]byte(separator), text))
		return nil
	}

	indent := d.lineIndent(last.start)
	text, err := item(indent, false)
	if err != nil {
		return err
	}
	d.splice(last.end, last.end, slices.Concat([]byte(","+d.newline+indent), text))
	return nil
}

// fill gives the empty container c its first item.
func (d *document) fill(c container, item render) error {
	open, closing := d.body[c.open], d.body[c.end-1]
	if d.oneLine {
		text, err := item("", true)
		if err != nil {
			return err
		}
		d.splice(c.open, c.end, slices.Concat([]byte{open}, text, []byte{closing}))
		return nil
	}

	outer := d.lineIndent(c.open)
	inner := outer + d.indent
	text, err := item(inner, false)
	if err != nil {
		return err
	}
	d.splice(c.open, c.end, slices.Concat(
		[]byte{open}, []byte(d.newline+inner), text, []byte(d.newline+outer), []byte{closing}))
	return nil
}

// layOut formats compact JSON to continue a line that starts with indent,
// indenting nested lines as the file does, or keeps it compact inline.
func (d *document) layOut(value []byte, indent string, inline bool) ([]byte, error) {
	if inline {
		return value, nil
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, value, indent, d.indent); err != nil {
		return nil, fmt.Errorf("format %s: %w", value, err)
	}
	if d.newline == "\n" {
		return buf.Bytes(), nil
	}
	return bytes.ReplaceAll(buf.Bytes(), []byte("\n"), []byte(d.newline)), nil
}

// lineIndent returns the spaces and tabs that start the line holding offset.
func (d *document) lineIndent(offset int) string {
	start := bytes.LastIndexByte(d.body[:offset], '\n') + 1
	end := start
	for end < offset && (d.body[end] == ' ' || d.body[end] == '\t') {
		end++
	}
	return string(d.body[start:end])
}

// removeMember deletes the member named key from the object at path. Keys
// are unique, as parseDocument checks.
func (d *document) removeMember(path, key string) error {
	c, err := d.container(path)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(c.items, func(it item) bool { return it.key == key })
	if i < 0 {
		return fmt.Errorf("remove %s.%s: there is no such member", path, key)
	}
	d.removeAt(c, i)
	return nil
}

// removeElement deletes the element at index from the array at path.
func (d *document) removeElement(path string, index int) error {
	c, err := d.container(path)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(c.items) {
		return fmt.Errorf("remove %s.%d: there is no such element", path, index)
	}
	d.removeAt(c, index)
	return nil
}

// removeAt deletes item i of c together with the separator that joined it to
// a neighbor, which is exactly the text appendItem adds, so the two undo each
// other. A container left empty becomes {} or [].
func (d *document) removeAt(c container, i int) {
	switch {
	case len(c.items) == 1:
		d.splice(c.open, c.end, []byte{d.body[c.open], d.body[c.end-1]})
	case i > 0:
		d.splice(c.items[i-1].end, c.items[i].end, nil)
	default:
		d.splice(c.items[0].start, c.items[1].start, nil)
	}
}

func (d *document) splice(start, end int, text []byte) {
	d.body = slices.Concat(d.body[:start], text, d.body[end:])
}

// encodeJSON returns the compact JSON for v. Unlike json.Marshal it leaves <,
// > and & alone, as Claude Code does, so the settings file reads naturally.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, fmt.Errorf("encode %T: %w", v, err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// jsonString returns the JSON string literal for s, which must be valid
// UTF-8, encoded as JSON.stringify encodes it, which is how Claude Code
// writes the file.
func jsonString(s string) string {
	return `"` + escapeJSON(s) + `"`
}

var shortEscapes = map[rune]string{
	'"': `\"`, '\\': `\\`, '\b': `\b`, '\f': `\f`, '\n': `\n`, '\r': `\r`, '\t': `\t`,
}

func escapeJSON(s string) string {
	var b strings.Builder
	for _, r := range s {
		escape, short := shortEscapes[r]
		switch {
		case short:
			b.WriteString(escape)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// elements returns the elements of an array, and nothing for any other value.
func elements(r gjson.Result) []gjson.Result {
	if !r.IsArray() {
		return nil
	}
	return r.Array()
}
