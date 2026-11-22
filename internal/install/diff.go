package install

import (
	"bytes"
	"fmt"
	"strings"
)

const (
	// contextLines is how many unchanged lines a hunk shows around a change.
	contextLines = 3
	// maxDiffCells bounds the memory of the line matching: two files whose
	// differing middles would need more cells are shown as one replacement.
	maxDiffCells = 1 << 22
)

// lineOp is one line of a diff: unchanged (' '), removed ('-') or added
// ('+'). The line keeps its line ending.
type lineOp struct {
	kind byte
	line string
}

// Diff returns a unified diff from before to after with three lines of
// context under the headers "--- <path>" and "+++ <path> (loadline)", or ""
// when they are the same.
func Diff(path string, before, after []byte) string {
	ops := diffLines(splitLines(before), splitLines(after))

	var b strings.Builder
	for _, h := range hunks(ops) {
		if b.Len() == 0 {
			fmt.Fprintf(&b, "--- %s\n+++ %s (loadline)\n", path, path)
		}
		h.write(&b, ops)
	}
	return b.String()
}

func splitLines(text []byte) []string {
	var lines []string
	for line := range bytes.Lines(text) {
		lines = append(lines, string(line))
	}
	return lines
}

// diffLines returns a shortest edit script from a to b. The lines both share
// at the start and end are matched first, which leaves only the edited middle
// to the quadratic longest-common-subsequence search.
func diffLines(a, b []string) []lineOp {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}

	ops := make([]lineOp, 0, len(a)+len(b))
	for _, line := range a[:prefix] {
		ops = append(ops, lineOp{' ', line})
	}
	ops = append(ops, matchLines(a[prefix:len(a)-suffix], b[prefix:len(b)-suffix])...)
	for _, line := range a[len(a)-suffix:] {
		ops = append(ops, lineOp{' ', line})
	}
	return ops
}

// matchLines diffs a and b through their longest common subsequence,
// removing before adding where both are possible.
func matchLines(a, b []string) []lineOp {
	n, m := len(a), len(b)
	ops := make([]lineOp, 0, n+m)
	if (n+1)*(m+1) > maxDiffCells {
		for _, line := range a {
			ops = append(ops, lineOp{'-', line})
		}
		for _, line := range b {
			ops = append(ops, lineOp{'+', line})
		}
		return ops
	}

	// common[i*width+j] is the length of the longest common subsequence of
	// a[i:] and b[j:].
	width := m + 1
	common := make([]int32, (n+1)*width)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				common[i*width+j] = common[(i+1)*width+j+1] + 1
			} else {
				common[i*width+j] = max(common[(i+1)*width+j], common[i*width+j+1])
			}
		}
	}

	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, lineOp{' ', a[i]})
			i++
			j++
		case common[(i+1)*width+j] >= common[i*width+j+1]:
			ops = append(ops, lineOp{'-', a[i]})
			i++
		default:
			ops = append(ops, lineOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, lineOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, lineOp{'+', b[j]})
	}
	return ops
}

// hunk is a run of ops shown together: changes, and the context around them.
type hunk struct{ start, end int }

// hunks groups the changes in ops. Changes separated by no more than twice
// the context share a hunk, as their contexts would touch.
func hunks(ops []lineOp) []hunk {
	var out []hunk
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start, end := max(0, i-contextLines), i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			next := end
			for next < len(ops) && ops[next].kind == ' ' {
				next++
			}
			if next == len(ops) || next-end > 2*contextLines {
				break
			}
			end = next
		}
		end = min(len(ops), end+contextLines)
		out = append(out, hunk{start: start, end: end})
		i = end
	}
	return out
}

func (h hunk) write(b *strings.Builder, ops []lineOp) {
	before, after := lineCount(ops[:h.start])
	beforeLen, afterLen := lineCount(ops[h.start:h.end])
	fmt.Fprintf(b, "@@ -%s +%s @@\n", lineRange(before, beforeLen), lineRange(after, afterLen))

	for _, op := range ops[h.start:h.end] {
		line, terminated := strings.CutSuffix(op.line, "\n")
		b.WriteByte(op.kind)
		b.WriteString(strings.TrimSuffix(line, "\r"))
		b.WriteByte('\n')
		if !terminated {
			b.WriteString("\\ No newline at end of file\n")
		}
	}
}

// lineCount returns how many of ops are lines of the old file and of the new.
func lineCount(ops []lineOp) (before, after int) {
	for _, op := range ops {
		if op.kind != '+' {
			before++
		}
		if op.kind != '-' {
			after++
		}
	}
	return before, after
}

// lineRange formats a hunk's range of count lines after the first skipped
// lines, as unified diffs do: a lone line by its number, an empty range by
// the line before it.
func lineRange(skipped, count int) string {
	switch count {
	case 0:
		return fmt.Sprintf("%d,0", skipped)
	case 1:
		return fmt.Sprint(skipped + 1)
	default:
		return fmt.Sprintf("%d,%d", skipped+1, count)
	}
}
