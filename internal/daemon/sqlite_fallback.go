package daemon

// SQLite/VSCode-storage fallback extractor (issue #77, daemon side).
//
//  1. Open the DB read-only in-process (modernc.org/sqlite, already used for
//     OpenCode) and scan the newest key/value rows for chat-like JSON
//     payloads, with a hard byte and time budget.
//  2. Otherwise (not SQLite, locked, unknown schema), scan the raw DB bytes
//     for printable strings that look like dialogue turns.
//
// The previous `sqlite3 .dump` path spawned a process that serialised the
// whole database (IDE state DBs reach gigabytes) into memory on every change,
// which was a main cause of the machine stalling while capture ran.
//
// Both paths are best-effort and read-only: failures return nil (the caller
// falls back to liveness-only). Opaque fixtures (no spaces / short blobs)
// yield zero turns, preserving prior liveness behavior in tests.

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// maxSQLiteScanBytes bounds raw DB reads (8MB).
const maxSQLiteScanBytes = 8 << 20

var (
	rolePat    = regexp.MustCompile(`(?i)"role"\s*:\s*"(user|assistant|human|ai|agent)"`)
	contentPat = regexp.MustCompile(`"content"\s*:\s*"(([^"\\]|\\.){20,})"`)
	textPat    = regexp.MustCompile(`"text"\s*:\s*"(([^"\\]|\\.){20,})"`)
)

// extractSQLiteFallback attempts stdlib-only extraction of conversation turns
// from a Cursor/VSCode .vscdb/.db file. It returns nil when nothing
// chat-like is found (caller keeps liveness-only).
func extractSQLiteFallback(dbPath string, since time.Time) []Turn {
	if turns, handled := extractViaDriver(dbPath); handled {
		return turns
	}
	return extractViaRawScan(dbPath)
}

// fallbackKVTables are the key/value tables VS Code-family IDEs use for chat
// state (Cursor composer bubbles live in cursorDiskKV, others in ItemTable).
var fallbackKVTables = []string{"cursorDiskKV", "ItemTable"}

// extractViaDriver reads the newest rows of the IDE key/value tables through
// a read-only in-process connection. handled=false means the file could not
// be read this way and the raw scan should be tried instead.
func extractViaDriver(dbPath string) (turns []Turn, handled bool) {
	path := filepath.ToSlash(dbPath)
	if filepath.VolumeName(dbPath) != "" {
		path = "/" + path
	}
	// mode=ro: never write or create a journal; the
	// IDE stays the only writer.
	uri := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, false
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var text strings.Builder
	found := false
	for _, table := range fallbackKVTables {
		var name string
		if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			continue
		}
		found = true
		// rowid DESC: newest writes first, so the byte budget keeps recent
		// conversation rather than whatever happens to sit at the file start.
		rows, err := db.QueryContext(ctx, `SELECT CAST(value AS TEXT) FROM "`+table+`" ORDER BY rowid DESC`)
		if err != nil {
			continue
		}
		var chunks []string
		budget := maxSQLiteScanBytes - text.Len()
		for budget > 0 && rows.Next() {
			var v sql.NullString
			if rows.Scan(&v) != nil || !v.Valid || len(v.String) < 20 {
				continue
			}
			if !strings.Contains(v.String, `"text"`) && !strings.Contains(v.String, `"content"`) {
				continue
			}
			s := v.String
			if len(s) > budget {
				s = s[:budget]
			}
			chunks = append(chunks, s)
			budget -= len(s)
		}
		rows.Close()
		// Restore chronological order for turnsFromText's trailing window.
		for i := len(chunks) - 1; i >= 0; i-- {
			text.WriteString(chunks[i])
			text.WriteByte('\n')
		}
		if text.Len() >= maxSQLiteScanBytes {
			break
		}
	}
	if !found {
		return nil, false
	}
	return turnsFromText(text.String()), true
}

// extractViaRawScan reads up to 8MB of the DB file and scans printable
// strings for chat-like content.
func extractViaRawScan(dbPath string) []Turn {
	f, err := os.Open(dbPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() <= 0 {
		return nil
	}
	size := st.Size()
	if size > maxSQLiteScanBytes {
		size = maxSQLiteScanBytes
	}
	buf := make([]byte, size)
	n, err := f.ReadAt(buf, 0)
	if err != nil && n <= 0 {
		return nil
	}
	buf = buf[:n]
	return turnsFromText(printableStrings(string(buf)))
}

// printableStrings extracts printable runs (>=20 chars, must contain a
// space so opaque fixtures like "sqlite-format-data-more" yield nothing).
func printableStrings(s string) string {
	var b strings.Builder
	run := 0
	start := -1
	flush := func() {
		if run >= 20 && start >= 0 {
			frag := s[start : start+run]
			if strings.Contains(frag, " ") {
				b.WriteString(frag)
				b.WriteByte('\n')
			}
		}
		run = 0
		start = -1
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 32 && c < 127 || c == '\n' || c == '\t' {
			if start < 0 {
				start = i
			}
			run++
		} else {
			flush()
		}
	}
	flush()
	return b.String()
}

// turnsFromText scans text for role/content pairs and returns Turns.
// It pairs each content/text match with the nearest preceding role.
func turnsFromText(text string) []Turn {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	roles := rolePat.FindAllStringSubmatchIndex(text, -1)
	if len(roles) == 0 {
		// No roles: treat long content blobs as unknown-speaker turns
		// (still genuine CONVERSATION_TURN signal for Cursor/VSCode).
		var out []Turn
		for _, m := range contentPat.FindAllStringSubmatch(text, -1) {
			if len(m) < 2 {
				continue
			}
			c := unescapeJSONStr(strings.TrimSpace(m[1]))
			if len(c) < 20 || !strings.Contains(c, " ") {
				continue
			}
			out = append(out, Turn{Speaker: "unknown", Content: c})
		}
		return lastTurns(out, 20)
	}
	var out []Turn
	// For each role, look ahead <=4KB for the next content/text payload.
	for _, r := range roles {
		if len(r) < 4 {
			continue
		}
		role := strings.ToLower(text[r[2]:r[3]])
		end := r[1] + 4096
		if end > len(text) {
			end = len(text)
		}
		window := text[r[1]:end]
		var content string
		if m := contentPat.FindStringSubmatch(window); len(m) >= 2 {
			content = unescapeJSONStr(strings.TrimSpace(m[1]))
		} else if m := textPat.FindStringSubmatch(window); len(m) >= 2 {
			content = unescapeJSONStr(strings.TrimSpace(m[1]))
		}
		if len(content) < 20 || !strings.Contains(content, " ") {
			continue
		}
		turn := Turn{Speaker: normalizeSpeaker(role), Content: content}
		if turn.Speaker == "user" {
			// Cursor chat stores keep the prompt's <timestamp> header.
			turn.Timestamp, _ = cursorPromptTime(content)
		}
		out = append(out, turn)
	}
	return lastTurns(out, 20)
}

// lastTurns keeps the trailing window of fallback turns. Raw SQLite scans
// walk the file from the start, so an un-capped first-20 window can stay
// pinned to ancient rows forever.
func lastTurns(turns []Turn, limit int) []Turn {
	if limit <= 0 || len(turns) <= limit {
		return turns
	}
	return turns[len(turns)-limit:]
}

// unescapeJSONStr unescapes \" \\ \n etc. in an extracted JSON string.
func unescapeJSONStr(s string) string {
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, `\\`, `\`)
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\t`, " ")
	return strings.TrimSpace(s)
}
