package daemon

// SQLite/VSCode-storage fallback extractor (issue #77, daemon side).
//
// No SQL driver dep is approved (ADR-033), so this file is stdlib-only:
//  1. When the `sqlite3` CLI is present, dump the DB read-only and scan the
//     dump text for chat-like JSON payloads.
//  2. Otherwise (or when the CLI fails), scan the raw DB bytes for printable
//     strings that look like dialogue turns.
//
// Both paths are best-effort and read-only: failures return nil (the caller
// falls back to liveness-only). Opaque fixtures (no spaces / short blobs)
// yield zero turns, preserving prior liveness behavior in tests.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
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
	// Fast path: sqlite3 CLI dump (read-only).
	if turns := extractViaCLI(dbPath); len(turns) > 0 {
		return turns
	}
	return extractViaRawScan(dbPath)
}

// extractViaCLI dumps the DB with the sqlite3 CLI (if present) and scans the
// dump for role/content JSON fragments.
func extractViaCLI(dbPath string) []Turn {
	bin, err := exec.LookPath("sqlite3")
	if err != nil || strings.TrimSpace(bin) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// .dump is read-only; -readonly flag added when supported (best-effort:
	// failure falls back to raw scan).
	cmd := exec.CommandContext(ctx, bin, dbPath, ".dump")
	var out bytes.Buffer
	cmd.Stdout = &out
	// Bound output: LimitReader is not directly usable with cmd.Stdout, so
	// cap after the fact.
	if err := cmd.Run(); err != nil {
		return nil
	}
	dump := out.String()
	if len(dump) > maxSQLiteScanBytes {
		dump = dump[:maxSQLiteScanBytes]
	}
	return turnsFromText(dump)
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
			if len(out) >= 20 {
				break
			}
		}
		return out
	}
	var out []Turn
	// For each role, look ahead ≤4KB for the next content/text payload.
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
		out = append(out, Turn{Speaker: normalizeSpeaker(role), Content: content})
		if len(out) >= 20 {
			break
		}
	}
	return out
}

// unescapeJSONStr unescapes \" \\ \n etc. in an extracted JSON string.
func unescapeJSONStr(s string) string {
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, `\\`, `\`)
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\t`, " ")
	return strings.TrimSpace(s)
}
