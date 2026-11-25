package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// Agent settings files are edited in place: only the one server entry Nexus
// owns changes, and everything else (key order, comments, formatting) stays
// byte for byte. Re-encoding the whole document would sort every key of a
// large ~/.claude.json and drop the comments Gemini CLI and VS Code-style
// settings allow.

var errNotPatchable = errors.New("settings file layout not recognised")

// jsoncScanner walks JSON with // and /* */ comments.
type jsoncScanner struct {
	b []byte
	i int
}

// skip moves past whitespace and comments.
func (s *jsoncScanner) skip() {
	for s.i < len(s.b) {
		c := s.b[s.i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			s.i++
		case c == '/' && s.i+1 < len(s.b) && s.b[s.i+1] == '/':
			for s.i < len(s.b) && s.b[s.i] != '\n' {
				s.i++
			}
		case c == '/' && s.i+1 < len(s.b) && s.b[s.i+1] == '*':
			end := bytes.Index(s.b[s.i+2:], []byte("*/"))
			if end < 0 {
				s.i = len(s.b)
				return
			}
			s.i += end + 4
		default:
			return
		}
	}
}

func (s *jsoncScanner) peek() byte {
	s.skip()
	if s.i >= len(s.b) {
		return 0
	}
	return s.b[s.i]
}

// str reads a JSON string at the cursor and returns its decoded value.
func (s *jsoncScanner) str() (string, error) {
	if s.peek() != '"' {
		return "", errNotPatchable
	}
	start := s.i
	s.i++
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case '\\':
			s.i += 2
			continue
		case '"':
			s.i++
			var v string
			if err := json.Unmarshal(s.b[start:s.i], &v); err != nil {
				return "", errNotPatchable
			}
			return v, nil
		}
		s.i++
	}
	return "", errNotPatchable
}

// value skips one value and returns its [start, end) span.
func (s *jsoncScanner) value() (int, int, error) {
	c := s.peek()
	start := s.i
	switch c {
	case '"':
		_, err := s.str()
		return start, s.i, err
	case '{', '[':
		depth := 0
		for s.i < len(s.b) {
			s.skip()
			if s.i >= len(s.b) {
				break
			}
			switch s.b[s.i] {
			case '"':
				if _, err := s.str(); err != nil {
					return 0, 0, err
				}
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					s.i++
					return start, s.i, nil
				}
			}
			s.i++
		}
		return 0, 0, errNotPatchable
	case 0:
		return 0, 0, errNotPatchable
	default:
		for s.i < len(s.b) && !strings.ContainsRune(",}] \t\r\n/", rune(s.b[s.i])) {
			s.i++
		}
		if s.i == start {
			return 0, 0, errNotPatchable
		}
		return start, s.i, nil
	}
}

type jsonMember struct {
	key                  string
	keyStart             int // first byte of the key string
	valueStart, valueEnd int
	commaEnd             int // just past the comma after the value, or valueEnd
}

// members lists the members of the object starting at open ('{').
func members(b []byte, open int) ([]jsonMember, int, error) {
	s := &jsoncScanner{b: b, i: open + 1}
	var out []jsonMember
	for {
		c := s.peek()
		if c == '}' {
			return out, s.i, nil
		}
		if c != '"' {
			return nil, 0, errNotPatchable
		}
		m := jsonMember{keyStart: s.i}
		k, err := s.str()
		if err != nil {
			return nil, 0, err
		}
		m.key = k
		if s.peek() != ':' {
			return nil, 0, errNotPatchable
		}
		s.i++
		if m.valueStart, m.valueEnd, err = s.value(); err != nil {
			return nil, 0, err
		}
		m.commaEnd = m.valueEnd
		if s.peek() == ',' {
			s.i++
			m.commaEnd = s.i
		}
		out = append(out, m)
	}
}

// indentUnit guesses the file's indent from its first indented line.
func indentUnit(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return "  "
}

// lineIndent is the whitespace that starts the line holding offset i.
func lineIndent(b []byte, i int) string {
	start := bytes.LastIndexByte(b[:i], '\n') + 1
	end := start
	for end < len(b) && (b[end] == ' ' || b[end] == '\t') {
		end++
	}
	return string(b[start:end])
}

func encodeIndented(v any, prefix, unit string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent(prefix, unit)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// insertMember adds `"name": value` as the last member of the object whose
// members and closing brace are given.
func insertMember(b []byte, open, closeAt int, ms []jsonMember, name string, value any, unit string) ([]byte, error) {
	objIndent := lineIndent(b, open)
	memberIndent := objIndent + unit
	if len(ms) > 0 {
		memberIndent = lineIndent(b, ms[0].keyStart)
	}
	key, _ := json.Marshal(name)
	val, err := encodeIndented(value, memberIndent, unit)
	if err != nil {
		return nil, err
	}
	insert := "\n" + memberIndent + string(key) + ": " + string(val)
	at := closeAt
	if len(ms) > 0 {
		last := ms[len(ms)-1]
		if last.commaEnd == last.valueEnd { // no trailing comma yet
			insert = "," + insert
		}
		at = last.commaEnd
	}
	// Keep what followed the last member (a comment, the newline) after the
	// new one; the closing brace goes on its own line.
	tail := b[at:closeAt]
	out := append([]byte{}, b[:at]...)
	out = append(out, insert...)
	if len(bytes.TrimSpace(tail)) == 0 {
		out = append(out, "\n"+objIndent...)
	} else {
		out = append(out, tail...)
	}
	return append(out, b[closeAt:]...), nil
}

// patchServer sets (entry != nil) or removes (entry == nil) servers[name]
// under the top-level key, editing only that part of the text. The result
// is checked to decode to exactly the intended document.
func patchServer(old []byte, key, name string, entry map[string]any, want map[string]any) ([]byte, error) {
	b := old
	bom := []byte{0xef, 0xbb, 0xbf}
	hadBOM := bytes.HasPrefix(b, bom)
	b = bytes.TrimPrefix(b, bom)
	unit := indentUnit(b)
	var out []byte
	s := &jsoncScanner{b: b}
	if strings.TrimSpace(string(b)) == "" || s.peek() != '{' {
		return nil, errNotPatchable
	}
	open := s.i
	top, topClose, err := members(b, open)
	if err != nil {
		return nil, err
	}
	var section *jsonMember
	for i := range top {
		if top[i].key == key {
			section = &top[i]
		}
	}
	switch {
	case section == nil && entry == nil:
		out = b
	case section == nil:
		out, err = insertMember(b, open, topClose, top, key, map[string]any{name: entry}, unit)
	default:
		if b[section.valueStart] != '{' {
			return nil, errNotPatchable
		}
		inner, innerClose, err2 := members(b, section.valueStart)
		if err2 != nil {
			return nil, err2
		}
		var found *jsonMember
		for i := range inner {
			if inner[i].key == name {
				found = &inner[i]
			}
		}
		switch {
		case found == nil && entry == nil:
			out = b
		case found == nil:
			out, err = insertMember(b, section.valueStart, innerClose, inner, name, entry, unit)
		case entry == nil:
			// Remove the member and its comma; if it was the last one, drop
			// the comma before it instead.
			start, end := found.keyStart, found.commaEnd
			idx := 0
			for i := range inner {
				if inner[i].keyStart == found.keyStart {
					idx = i
				}
			}
			if found.commaEnd == found.valueEnd && idx > 0 {
				prev := inner[idx-1]
				start = prev.valueEnd
			}
			// Take the line the member was on with it.
			lineStart := bytes.LastIndexByte(b[:start], '\n')
			if lineStart >= 0 && len(bytes.TrimSpace(b[lineStart:start])) == 0 {
				start = lineStart
			}
			out = append(append([]byte{}, b[:start]...), b[end:]...)
		default:
			val, err2 := encodeIndented(entry, lineIndent(b, found.keyStart), unit)
			if err2 != nil {
				return nil, err2
			}
			out = append(append(append([]byte{}, b[:found.valueStart]...), val...), b[found.valueEnd:]...)
		}
	}
	if err != nil {
		return nil, err
	}
	got, err := decodeJSONC(out)
	if err != nil || !same(got, want) {
		return nil, errNotPatchable
	}
	if hadBOM {
		out = append(append([]byte{}, bom...), out...)
	}
	return out, nil
}

// decodeJSONC reads JSON that may carry comments and trailing commas.
func decodeJSONC(b []byte) (map[string]any, error) {
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	b = stripTrailingCommas(stripJSONComments(b))
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errNotPatchable
	}
	var extra any
	if dec.Decode(&extra) == nil {
		return nil, errors.New("trailing JSON content")
	}
	return m, nil
}

// stripTrailingCommas blanks a comma that only precedes } or ] (after
// comments were blanked). String-aware.
func stripTrailingCommas(b []byte) []byte {
	out := append([]byte(nil), b...)
	quoted, escaped := false, false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
			continue
		}
		if c != ',' {
			continue
		}
		j := i + 1
		for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\r' || out[j] == '\n') {
			j++
		}
		if j < len(out) && (out[j] == '}' || out[j] == ']') {
			out[i] = ' '
		}
	}
	return out
}

// hasJSONComments reports whether re-encoding would lose comments.
func hasJSONComments(b []byte) bool {
	return !bytes.Equal(stripJSONComments(b), b)
}
