package checkpoint

import "strings"

// Line diff with hunks that can be kept or undone one at a time.
//
// Lines keep their terminators ("a\n", "b\r\n", a last line without one),
// so applying or reverting a hunk rebuilds the file byte for byte.

// Hunk is one group of nearby changes, with up to Context lines around it.
type Hunk struct {
	Index    int    `json:"index"`
	OldStart int    `json:"old_start"` // 1-based, like a unified diff header
	OldLines int    `json:"old_lines"`
	NewStart int    `json:"new_start"`
	NewLines int    `json:"new_lines"`
	Header   string `json:"header"` // "@@ -12,7 +12,8 @@"
	Lines    []Line `json:"lines"`
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
	chunks   []chunk
}

// Line is one row of a hunk: Kind is " " (context), "+" or "-".
type Line struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
}

// chunk is one contiguous change: old[A0:A1] became new[B0:B1].
type chunk struct{ A0, A1, B0, B1 int }

// Context is how many unchanged lines a hunk shows around its changes.
const Context = 3

// maxDiffEdits bounds the diff's work; past it the differing middle is
// treated as one replaced block (still exact, just coarser).
const maxDiffEdits = 4000

// SplitLines splits text into lines that keep their "\n".
func SplitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffChunks returns the changes that turn a into b, in order.
func diffChunks(a, b []string) []chunk {
	// Common prefix and suffix are cheap and usually most of the file.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	am, bm := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if len(am) == 0 && len(bm) == 0 {
		return nil
	}
	ops, ok := myers(am, bm)
	if !ok {
		return []chunk{{pre, pre + len(am), pre, pre + len(bm)}}
	}
	// ops: one byte per step, '=' '-' '+'.
	var out []chunk
	i, j := pre, pre
	var cur *chunk
	for _, op := range ops {
		switch op {
		case '=':
			if cur != nil {
				out = append(out, *cur)
				cur = nil
			}
			i++
			j++
		case '-':
			if cur == nil {
				cur = &chunk{i, i, j, j}
			}
			i++
			cur.A1 = i
		case '+':
			if cur == nil {
				cur = &chunk{i, i, j, j}
			}
			j++
			cur.B1 = j
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// myers is the O(ND) shortest edit script. It gives up (false) when the
// files differ by more than maxDiffEdits lines.
func myers(a, b []string) ([]byte, bool) {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		ops := make([]byte, 0, n+m)
		for range a {
			ops = append(ops, '-')
		}
		for range b {
			ops = append(ops, '+')
		}
		return ops, true
	}
	max := n + m
	if max > maxDiffEdits {
		max = maxDiffEdits
	}
	offset := max + 1
	v := make([]int, 2*max+3)
	var trace [][]int
	found := -1
	for d := 0; d <= max; d++ {
		snap := make([]int, len(v))
		copy(snap, v)
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = d
				break
			}
		}
		if found >= 0 {
			break
		}
	}
	if found < 0 {
		return nil, false
	}
	// Walk back through the trace.
	var rev []byte
	x, y := n, m
	for d := found; d > 0; d-- {
		vd := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && vd[offset+k-1] < vd[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vd[offset+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			rev = append(rev, '=')
			x--
			y--
		}
		if x == prevX {
			rev = append(rev, '+')
			y--
		} else {
			rev = append(rev, '-')
			x--
		}
	}
	for x > 0 && y > 0 {
		rev = append(rev, '=')
		x--
		y--
	}
	ops := make([]byte, len(rev))
	for i := range rev {
		ops[i] = rev[len(rev)-1-i]
	}
	return ops, true
}

// Hunks groups the changes from old to new into hunks with context.
func Hunks(oldText, newText string) []Hunk {
	a, b := SplitLines(oldText), SplitLines(newText)
	chunks := diffChunks(a, b)
	if len(chunks) == 0 {
		return nil
	}
	var groups [][]chunk
	for _, c := range chunks {
		if n := len(groups); n > 0 {
			last := groups[n-1][len(groups[n-1])-1]
			if c.A0-last.A1 <= 2*Context {
				groups[n-1] = append(groups[n-1], c)
				continue
			}
		}
		groups = append(groups, []chunk{c})
	}
	hunks := make([]Hunk, 0, len(groups))
	for gi, g := range groups {
		first, last := g[0], g[len(g)-1]
		a0 := max(0, first.A0-Context)
		a1 := min(len(a), last.A1+Context)
		b0 := first.B0 - (first.A0 - a0)
		b1 := last.B1 + (a1 - last.A1)
		h := Hunk{Index: gi, OldStart: a0 + 1, OldLines: a1 - a0, NewStart: b0 + 1, NewLines: b1 - b0, chunks: g}
		if h.OldLines == 0 {
			h.OldStart = a0
		}
		if h.NewLines == 0 {
			h.NewStart = b0
		}
		h.Header = "@@ -" + rangeText(h.OldStart, h.OldLines) + " +" + rangeText(h.NewStart, h.NewLines) + " @@"
		i, j := a0, b0
		for _, c := range g {
			for i < c.A0 {
				h.Lines = append(h.Lines, Line{Kind: " ", Text: trimEOL(a[i]), Old: i + 1, New: j + 1})
				i++
				j++
			}
			for ; i < c.A1; i++ {
				h.Lines = append(h.Lines, Line{Kind: "-", Text: trimEOL(a[i]), Old: i + 1})
				h.Removed++
			}
			for ; j < c.B1; j++ {
				h.Lines = append(h.Lines, Line{Kind: "+", Text: trimEOL(b[j]), New: j + 1})
				h.Added++
			}
		}
		for i < a1 {
			h.Lines = append(h.Lines, Line{Kind: " ", Text: trimEOL(a[i]), Old: i + 1, New: j + 1})
			i++
			j++
		}
		hunks = append(hunks, h)
	}
	return hunks
}

func rangeText(start, lines int) string {
	if lines == 1 {
		return itoa(start)
	}
	return itoa(start) + "," + itoa(lines)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func trimEOL(s string) string {
	s = strings.TrimSuffix(s, "\n")
	return strings.TrimSuffix(s, "\r")
}

// RevertHunk returns newText with hunk `index` put back the way oldText
// had it; every other change stays. ok is false when there is no such hunk.
func RevertHunk(oldText, newText string, index int) (string, bool) {
	a, b := SplitLines(oldText), SplitLines(newText)
	h, ok := hunkAt(oldText, newText, index)
	if !ok {
		return "", false
	}
	var out strings.Builder
	at := 0
	for _, c := range h.chunks {
		out.WriteString(strings.Join(b[at:c.B0], ""))
		out.WriteString(strings.Join(a[c.A0:c.A1], ""))
		at = c.B1
	}
	out.WriteString(strings.Join(b[at:], ""))
	return out.String(), true
}

// ApplyHunk returns oldText with hunk `index` taken from newText: the
// review's new base after that hunk is kept.
func ApplyHunk(oldText, newText string, index int) (string, bool) {
	a, b := SplitLines(oldText), SplitLines(newText)
	h, ok := hunkAt(oldText, newText, index)
	if !ok {
		return "", false
	}
	var out strings.Builder
	at := 0
	for _, c := range h.chunks {
		out.WriteString(strings.Join(a[at:c.A0], ""))
		out.WriteString(strings.Join(b[c.B0:c.B1], ""))
		at = c.A1
	}
	out.WriteString(strings.Join(a[at:], ""))
	return out.String(), true
}

func hunkAt(oldText, newText string, index int) (Hunk, bool) {
	hunks := Hunks(oldText, newText)
	if index < 0 || index >= len(hunks) {
		return Hunk{}, false
	}
	return hunks[index], true
}

// LineStats counts added and removed lines between two texts.
func LineStats(oldText, newText string) (added, removed int) {
	for _, c := range diffChunks(SplitLines(oldText), SplitLines(newText)) {
		added += c.B1 - c.B0
		removed += c.A1 - c.A0
	}
	return added, removed
}
