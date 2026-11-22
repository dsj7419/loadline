package install_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/install"
)

// numbered returns lines 1..n, one per line, with line i replaced by
// replacements[i] when present.
func numbered(n int, replacements map[int]string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		line, ok := replacements[i]
		if !ok {
			line = fmt.Sprint(i)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func TestDiff(t *testing.T) {
	const header = "--- f\n+++ f (loadline)\n"
	tests := []struct {
		name          string
		before, after string
		want          string
	}{
		{
			name:   "identical",
			before: "a\nb\n",
			after:  "a\nb\n",
			want:   "",
		},
		{
			name:   "a change with three lines of context",
			before: numbered(10, nil),
			after:  numbered(10, map[int]string{5: "five"}),
			want:   header + "@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+five\n 6\n 7\n 8\n",
		},
		{
			name:   "a new file",
			before: "",
			after:  "a\nb\n",
			want:   header + "@@ -0,0 +1,2 @@\n+a\n+b\n",
		},
		{
			name:   "everything removed",
			before: "a\n",
			after:  "",
			want:   header + "@@ -1 +0,0 @@\n-a\n",
		},
		{
			name:   "an insertion before a last line without a newline",
			before: "{\n}",
			after:  "{\n  \"x\": 1\n}",
			want:   header + "@@ -1,2 +1,3 @@\n {\n+  \"x\": 1\n }\n\\ No newline at end of file\n",
		},
		{
			name:   "adding the final newline",
			before: "a",
			after:  "a\n",
			want:   header + "@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+a\n",
		},
		{
			name:   "the lines both share stay, though they move up",
			before: "x\ny\nx\n",
			after:  "y\nx\nz\n",
			want:   header + "@@ -1,3 +1,3 @@\n-x\n y\n x\n+z\n",
		},
		{
			name:   "the longest run in common stays, not a shorter one",
			before: "x\ny\nz\nz\n",
			after:  "z\nz\nx\n",
			want:   header + "@@ -1,4 +1,3 @@\n-x\n-y\n z\n z\n+x\n",
		},
		{
			name:   "lines in common on both sides of a removal stay",
			before: "x\ny\nx\n",
			after:  "z\nx\nx\nz\n",
			want:   header + "@@ -1,3 +1,4 @@\n+z\n x\n-y\n x\n+z\n",
		},
		{
			name:   "removals and additions interleave around the lines in common",
			before: "x\nx\ny\nz\n",
			after:  "z\nx\nz\nx\nx\n",
			want:   header + "@@ -1,4 +1,5 @@\n-x\n+z\n x\n-y\n z\n+x\n+x\n",
		},
		{
			name:   "a line added after a copy of itself",
			before: "a\n",
			after:  "a\na\n",
			want:   header + "@@ -1 +1,2 @@\n a\n+a\n",
		},
		{
			name:   "a line removed after a copy of itself",
			before: "a\na\n",
			after:  "a\n",
			want:   header + "@@ -1,2 +1 @@\n a\n-a\n",
		},
		{
			name:   "distant changes make two hunks",
			before: numbered(20, nil),
			after:  numbered(20, map[int]string{2: "two", 18: "eighteen"}),
			want: header +
				"@@ -1,5 +1,5 @@\n 1\n-2\n+two\n 3\n 4\n 5\n" +
				"@@ -15,6 +15,6 @@\n 15\n 16\n 17\n-18\n+eighteen\n 19\n 20\n",
		},
		{
			name:   "nearby changes share a hunk",
			before: numbered(12, nil),
			after:  numbered(12, map[int]string{2: "two", 8: "eight"}),
			want: header +
				"@@ -1,11 +1,11 @@\n 1\n-2\n+two\n 3\n 4\n 5\n 6\n 7\n-8\n+eight\n 9\n 10\n 11\n",
		},
		{
			name:   "changes six lines apart share a hunk, as their contexts touch",
			before: numbered(14, nil),
			after:  numbered(14, map[int]string{2: "two", 9: "nine"}),
			want: header +
				"@@ -1,12 +1,12 @@\n 1\n-2\n+two\n 3\n 4\n 5\n 6\n 7\n 8\n-9\n+nine\n 10\n 11\n 12\n",
		},
		{
			name:   "insertions shift the new line numbers",
			before: numbered(10, nil),
			after:  strings.Replace(numbered(10, nil), "2\n", "2\nx\ny\n", 1),
			want:   header + "@@ -1,5 +1,7 @@\n 1\n 2\n+x\n+y\n 3\n 4\n 5\n",
		},
		{
			name:   "CRLF lines are shown without the carriage return",
			before: "a\r\nb\r\n",
			after:  "a\r\nc\r\n",
			want:   header + "@@ -1,2 +1,2 @@\n a\n-b\n+c\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			// Diff takes microseconds here. One whose hunks stopped moving
			// forward would never return, and would append hunks until
			// memory ran out.
			within(10*time.Second, "Diff", func() { got = install.Diff("f", []byte(tt.before), []byte(tt.after)) })

			if got != tt.want {
				t.Errorf("Diff =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestDiff_IsMinimalOnGoldenFiles(t *testing.T) {
	before := readFile(t, "testdata/settings_owner.in.json")
	after := readFile(t, "testdata/settings_owner.out.json")

	got := install.Diff("settings.json", before, after)

	added, removed := 0, 0
	for _, line := range strings.Split(got, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	// Only two of the original lines are missing from the result: the old
	// status-line command and the env entry that gained a trailing comma.
	if removed != 2 {
		t.Errorf("diff removes %d lines, want 2:\n%s", removed, got)
	}
	if wantAdded := strings.Count(string(after), "\n") - strings.Count(string(before), "\n") + 2; added != wantAdded {
		t.Errorf("diff adds %d lines, want %d", added, wantAdded)
	}
}

// sides rebuilds the old and the new text a single-hunk diff covers.
func sides(diff string) (before, after string) {
	var b, a strings.Builder
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "@@"), line == "":
		case strings.HasPrefix(line, " "):
			b.WriteString(line[1:] + "\n")
			a.WriteString(line[1:] + "\n")
		case strings.HasPrefix(line, "-"):
			b.WriteString(line[1:] + "\n")
		case strings.HasPrefix(line, "+"):
			a.WriteString(line[1:] + "\n")
		}
	}
	return b.String(), a.String()
}

func TestDiff_StaysCorrectWhenFilesAreTooLargeToMatch(t *testing.T) {
	const lines = 3000
	changed := map[int]string{}
	for i := 1; i <= lines; i += 2 {
		changed[i] = fmt.Sprintf("changed %d", i)
	}
	before, after := numbered(lines, nil), numbered(lines, changed)

	got := install.Diff("f", []byte(before), []byte(after))

	if !strings.HasPrefix(got, "--- f\n+++ f (loadline)\n@@ -1,3000 +1,") {
		t.Fatalf("diff does not cover the whole file in one hunk:\n%.200s", got)
	}
	if b, a := sides(got); b != before || a != after {
		t.Error("the diff does not turn the old file into the new one")
	}
}

// Matching n differing lines against m takes (n+1) x (m+1) cells, and is
// done for up to 2048 x 2048. Files that differ in every other line, the
// first and the last among them, are matched line by line at 2047 lines,
// and at 2048 are shown as one replacement instead.
func TestDiff_MatchesLinesUpToTheBoundAndReplacesPastIt(t *testing.T) {
	for lines, minimal := range map[int]bool{2047: true, 2048: false} {
		changed := map[int]string{lines: "changed last"}
		for i := 1; i < lines; i += 2 {
			changed[i] = fmt.Sprintf("changed %d", i)
		}

		got := install.Diff("f", []byte(numbered(lines, nil)), []byte(numbered(lines, changed)))

		removed, addedBeforeLastRemoval := 0, false
		for _, line := range strings.Split(got, "\n") {
			switch {
			case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			case strings.HasPrefix(line, "-"):
				removed++
			case strings.HasPrefix(line, "+") && removed < lines:
				addedBeforeLastRemoval = true
			}
		}
		if want := len(changed); minimal && removed != want {
			t.Errorf("%d lines: diff removes %d lines, want only the %d that changed", lines, removed, want)
		}
		if !minimal && (removed != lines || addedBeforeLastRemoval) {
			t.Errorf("%d lines: diff removes %d lines, adding some before the last: %v; want all %d removed, then added",
				lines, removed, addedBeforeLastRemoval, lines)
		}
	}
}

func TestMaskToken(t *testing.T) {
	tests := map[string]string{ //nolint:gosec // G101: made-up tokens that MaskToken must hide
		`"OTEL_EXPORTER_OTLP_LOGS_HEADERS": "x-loadline-token=0123abcd"`:   `"OTEL_EXPORTER_OTLP_LOGS_HEADERS": "x-loadline-token=****"`,
		`x-loadline-token=<created-on-install>`:                            `x-loadline-token=****`,
		"-x-loadline-token=aa\n+x-loadline-token=bb\n":                     "-x-loadline-token=****\n+x-loadline-token=****\n",
		`"OTEL_EXPORTER_OTLP_LOGS_HEADERS": "x-loadline-token=ab,other=1"`: `"OTEL_EXPORTER_OTLP_LOGS_HEADERS": "x-loadline-token=****,other=1"`,
		`no token here`: `no token here`,
		// Whatever the value holds, all of it is masked.
		`"x-loadline-token=ab\"cdAef\\gh"`:  `"x-loadline-token=****"`,
		`"x-loadline-token=ab cd	ef"`:       `"x-loadline-token=****"`,
		"x-loadline-token=ab cd\nnext line": "x-loadline-token=****\nnext line",
	}
	for in, want := range tests {
		if got := string(install.MaskToken([]byte(in))); got != want {
			t.Errorf("MaskToken(%q) = %q, want %q", in, got, want)
		}
	}
}
