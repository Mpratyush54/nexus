package store

// line_match.go — content anchors for line-level change history.
//
// LineHash is the fingerprint the desktop stores for each added line and
// for the unchanged lines around a hunk, and that queries compute from the
// text of the line asked about. The desktop (internal/daemon/code_hunks.go)
// implements the same normalisation; TestLineHashVector pins both.

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
)

const (
	// MaxAddedHashes caps the added-line hashes kept per hunk.
	MaxAddedHashes = 2000
	// MaxContextHashes caps the context hashes kept on each side.
	MaxContextHashes = 2
	lineHashHexLen   = 16
)

// NormalizeLine makes a line comparable across platforms and
// re-indentation: CR removed, leading and trailing whitespace trimmed,
// runs of spaces and tabs collapsed to one space.
func NormalizeLine(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.TrimSpace(s)
	if !strings.ContainsAny(s, " \t") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteByte(c)
	}
	return b.String()
}

// LineHash is the first 8 bytes (hex) of the sha1 of the normalised line.
func LineHash(s string) string {
	sum := sha1.Sum([]byte(NormalizeLine(s)))
	return hex.EncodeToString(sum[:8])
}

// lowInfoLines are common lines that say nothing on their own.
var lowInfoLines = map[string]bool{
	"return": true, "return nil": true, "return err": true, "return true": true, "return false": true,
	"return null;": true, "return;": true, "return 0": true, "return 0;": true, "return none": true,
	"} else {": true, "else {": true, "else:": true, "} else if": true, "break": true, "break;": true,
	"continue": true, "continue;": true, "pass": true, "end": true, "});": true, "})": true, "};": true,
	"default:": true, "try {": true, "} catch {": true, "finally {": true, "} finally {": true,
	"if err != nil {": true, "*/": true, "/*": true, "/**": true, "#endif": true, "fi": true, "done": true,
}

// LowInformationLine reports whether a line is too common to identify a
// change alone (at most 3 non-space characters, or a stoplisted line):
// such lines match only with context.
func LowInformationLine(s string) bool {
	n := NormalizeLine(s)
	if len(strings.ReplaceAll(n, " ", "")) <= 3 {
		return true
	}
	return lowInfoLines[strings.ToLower(n)]
}

func validLineHash(h string) bool {
	if len(h) != lineHashHexLen {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// cleanLineHashes lower-cases hashes, blanks invalid ones (positions
// matter: adjacency is compared) and keeps at most max, the last ones when
// keepTail (context above a hunk: the nearest lines are last).
func cleanLineHashes(in []string, max int, keepTail bool) []string {
	if len(in) > max {
		if keepTail {
			in = in[len(in)-max:]
		} else {
			in = in[:max]
		}
	}
	out := make([]string, 0, len(in))
	for _, h := range in {
		h = strings.ToLower(strings.TrimSpace(h))
		if !validLineHash(h) {
			h = ""
		}
		out = append(out, h)
	}
	return out
}

// Match ranks, best first.
const (
	MatchExact   = 0 // the line and its neighbours match
	MatchContext = 1 // the line matches, a neighbour appears near the change
	MatchHash    = 2 // the line alone matches (not for low-information lines)
	MatchLine    = 3 // no content match: line-number placement (approximate)
)

// MatchName is the API word for a rank.
func MatchName(rank int) string {
	switch rank {
	case MatchExact:
		return "exact"
	case MatchContext:
		return "context"
	case MatchHash:
		return "hash"
	default:
		return "line"
	}
}

// LineProbe is one line asked about: its hash, its neighbours' hashes
// ("" when unknown) and whether it is low-information.
type LineProbe struct {
	Hash, Prev, Next string
	LowInfo          bool
}

// ProbesForLines builds probes for consecutive lines of the current file.
// prev/next are the lines just outside the range (nil when unknown).
func ProbesForLines(texts []string, prev, next *string) []LineProbe {
	out := make([]LineProbe, 0, len(texts))
	for i, t := range texts {
		p := LineProbe{Hash: LineHash(t), LowInfo: LowInformationLine(t)}
		switch {
		case i > 0:
			p.Prev = LineHash(texts[i-1])
		case prev != nil:
			p.Prev = LineHash(*prev)
		}
		switch {
		case i+1 < len(texts):
			p.Next = LineHash(texts[i+1])
		case next != nil:
			p.Next = LineHash(*next)
		}
		out = append(out, p)
	}
	return out
}

// MatchProbes returns the best rank at which c added one of the probed
// lines (ok=false when none matches by content).
func (c *CodeChange) MatchProbes(probes []LineProbe) (int, bool) {
	best, ok := MatchLine, false
	for _, p := range probes {
		for i, h := range c.AddedHashes {
			if h == "" || h != p.Hash {
				continue
			}
			above, below := "", ""
			if i > 0 {
				above = c.AddedHashes[i-1]
			} else if n := len(c.ContextBefore); n > 0 {
				above = c.ContextBefore[n-1]
			}
			if i+1 < len(c.AddedHashes) {
				below = c.AddedHashes[i+1]
			} else if len(c.ContextAfter) > 0 {
				below = c.ContextAfter[0]
			}
			rank := -1
			provided := p.Prev != "" || p.Next != ""
			switch {
			case provided && (p.Prev == "" || p.Prev == above) && (p.Next == "" || p.Next == below):
				rank = MatchExact
			case provided && (c.nearHash(p.Prev, above, below) || c.nearHash(p.Next, above, below)):
				rank = MatchContext
			case !p.LowInfo:
				rank = MatchHash
			}
			if rank >= 0 && (!ok || rank < best) {
				best, ok = rank, true
			}
		}
	}
	return best, ok
}

// nearHash reports whether h is one of the change's context hashes or the
// lines adjacent to the matched one.
func (c *CodeChange) nearHash(h, above, below string) bool {
	if h == "" {
		return false
	}
	if h == above || h == below {
		return true
	}
	for _, x := range c.ContextBefore {
		if x == h {
			return true
		}
	}
	for _, x := range c.ContextAfter {
		if x == h {
			return true
		}
	}
	return false
}
