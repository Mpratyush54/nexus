// Package agentfiles keeps a small, Nexus-managed section of project memory
// inside agent instruction files (CLAUDE.md, AGENTS.md, GEMINI.md) so agents
// that do not use the Nexus MCP server still see the key project context.
//
// Everything here is deliberately conservative: text outside the managed
// markers is never changed, malformed markers leave the file untouched,
// files are written atomically and only when the result differs, and the
// file's BOM and line-ending style are preserved.
package agentfiles

import (
	"bytes"
	"errors"
	"strings"
	"unicode/utf8"
)

// StartMarker opens the managed section. Detection only relies on the
// startMarkerPrefix so a hand-edited parenthetical is still recognised.
const StartMarker = "<!-- nexus:memory:start (managed by Nexus; edits inside are replaced) -->"

// EndMarker closes the managed section.
const EndMarker = "<!-- nexus:memory:end -->"

const (
	startMarkerPrefix = "<!-- nexus:memory:start"
	endMarkerPrefix   = "<!-- nexus:memory:end"
)

// MaxSectionChars caps the rendered managed section body (runes).
const MaxSectionChars = 3000

// MaxFileBytes is the largest instruction file Nexus will rewrite.
const MaxFileBytes = 1 << 20

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

var (
	// ErrMalformedMarkers means the file has an unpaired, duplicated or
	// out-of-order marker; the file is left untouched.
	ErrMalformedMarkers = errors.New("agentfiles: malformed nexus memory markers")
	// ErrTooLarge means the file exceeds MaxFileBytes.
	ErrTooLarge = errors.New("agentfiles: file larger than 1 MB")
	// ErrNotUTF8 means the file is not valid UTF-8 text.
	ErrNotUTF8 = errors.New("agentfiles: file is not valid UTF-8")
)

// Merge returns existing with the managed section set to body. It reports
// changed=false (and returns existing unchanged) when the result would be
// byte-identical. existing may be nil for a new file.
//
// Rules: a leading UTF-8 BOM is kept; the file's dominant line ending
// (CRLF or LF) is used for the section; with no markers the section is
// appended after one blank line; with exactly one start marker followed by
// exactly one end marker, only the bytes from the start of the start marker
// to the end of the end marker are replaced; anything else is
// ErrMalformedMarkers.
func Merge(existing []byte, body string) (out []byte, changed bool, err error) {
	if len(existing) > MaxFileBytes {
		return existing, false, ErrTooLarge
	}
	if !utf8.Valid(existing) {
		return existing, false, ErrNotUTF8
	}
	hasBOM := bytes.HasPrefix(existing, utf8BOM)
	text := string(bytes.TrimPrefix(existing, utf8BOM))
	eol := detectEOL(text)
	block := buildBlock(body, eol)

	starts := strings.Count(text, startMarkerPrefix)
	ends := strings.Count(text, endMarkerPrefix)
	var merged string
	switch {
	case starts == 0 && ends == 0:
		merged = appendBlock(text, block, eol)
	case starts == 1 && ends == 1:
		si := strings.Index(text, startMarkerPrefix)
		ei := strings.Index(text, endMarkerPrefix)
		if ei < si {
			return existing, false, ErrMalformedMarkers
		}
		// The start marker must be a complete HTML comment that closes
		// before the end marker; the end marker must close too.
		startClose := strings.Index(text[si:], "-->")
		if startClose < 0 || si+startClose+3 > ei {
			return existing, false, ErrMalformedMarkers
		}
		endClose := strings.Index(text[ei:], "-->")
		if endClose < 0 {
			return existing, false, ErrMalformedMarkers
		}
		merged = text[:si] + block + text[ei+endClose+3:]
	default:
		return existing, false, ErrMalformedMarkers
	}

	var buf bytes.Buffer
	if hasBOM {
		buf.Write(utf8BOM)
	}
	buf.WriteString(merged)
	out = buf.Bytes()
	if bytes.Equal(out, existing) {
		return existing, false, nil
	}
	return out, true, nil
}

// HasMarkers reports whether text contains any nexus memory marker.
func HasMarkers(existing []byte) bool {
	return bytes.Contains(existing, []byte(startMarkerPrefix)) || bytes.Contains(existing, []byte(endMarkerPrefix))
}

// detectEOL returns "\r\n" when CRLF line endings dominate, else "\n".
func detectEOL(text string) string {
	crlf := strings.Count(text, "\r\n")
	lf := strings.Count(text, "\n") - crlf
	if crlf > 0 && crlf >= lf {
		return "\r\n"
	}
	return "\n"
}

// buildBlock renders the marker-wrapped section (no trailing newline after
// the end marker) using eol.
func buildBlock(body, eol string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.Trim(body, "\n")
	var b strings.Builder
	b.WriteString(StartMarker)
	b.WriteString("\n")
	if body != "" {
		b.WriteString(body)
		b.WriteString("\n")
	}
	b.WriteString(EndMarker)
	return strings.ReplaceAll(b.String(), "\n", eol)
}

// appendBlock appends block after one blank line, never altering text.
func appendBlock(text, block, eol string) string {
	switch {
	case text == "":
		return block + eol
	case strings.HasSuffix(text, "\n\n") || strings.HasSuffix(text, "\r\n\r\n"):
		return text + block + eol
	case strings.HasSuffix(text, "\n"):
		return text + eol + block + eol
	default:
		return text + eol + eol + block + eol
	}
}
