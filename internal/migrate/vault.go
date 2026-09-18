// Vault markdown parsing (Phase 1b): legacy `mem remember` chunks become
// insert-ready MemoryItems.
//
// Writer format (v1 main.go, still the only producer):
//
//	\n## 2006-01-02 15:04\ntags: a, b\n\n<2-3 line fact>\n
//
// Recall split chunks on "\n## ", so the parser does the same. Parsing is
// heuristic by necessity — the vault has no schema, just conventions:
// a "## " header line carrying a timestamp plus optional trailing title,
// an optional "tags:" line, then free-text content. Anything that cannot be
// shaped into an insertable row (empty, below the 20-char CHECK floor) is
// skipped and counted, never failed: one malformed chunk must not abort an
// import of thousands.
package migrate

import (
	"fmt"
	"hash/crc32"
	"strings"
	"time"
	"unicode"
)

// MemoryItem is the migration-local shape of a memory_items row, mirroring
// internal/store.MemoryItem field-for-field (same names, same level/scope /
// status vocabularies) so a later adapter maps it 1:1 without translation.
type MemoryItem struct {
	Key            string
	Content        string
	ContextSnippet string
	Level          string // organization | project | personal | session | ephemeral
	Scope          string // fact | preference | decision | constraint | pattern | episode_summary
	Tags           []string
	Confidence     float32
	Status         string // PROPOSED | CONFIRMED | REJECTED | SUPERSEDED
	Source         string
	Project        string
	CreatedAt      time.Time
}

// Bounds mirror migrations/001_initial.up.sql CHECK
// (length(content) 20..2000). Chunks below the floor are skipped; chunks
// above the ceiling are truncated on a rune boundary.
const (
	minContentRunes = 20
	maxContentRunes = 2000
)

// ParseLearningsMD parses memory/global/learnings.md: global chunks become
// organization-level CONFIRMED items (they were written without a project
// scope, so they belong to every project, not one).
func ParseLearningsMD(data string) []MemoryItem {
	items, _ := parseMarkdownReport(data, "organization", "", "vault:learnings.md")
	return items
}

// ParseProjectMemoryMD parses memory/projects/<name>/MEMORY.md: chunks
// become project-level CONFIRMED items scoped to that project.
func ParseProjectMemoryMD(projectName, data string) []MemoryItem {
	items, _ := parseMarkdownReport(data, "project", projectName, "vault:MEMORY.md/"+projectName)
	return items
}

// parseMarkdownReport is the shared engine behind both public parsers. It
// returns insert-ready items plus the number of skipped chunks (title-only
// prelude excluded — "# Learnings" is file furniture, not a failed chunk).
func parseMarkdownReport(data, level, projectName, source string) ([]MemoryItem, int) {
	skipped := 0
	var out []MemoryItem
	for _, ch := range splitChunks(data) {
		item, ok := chunkToItem(ch.header, ch.lines, level, projectName, source)
		if !ok {
			skipped++
			continue
		}
		out = append(out, item)
	}
	return out, skipped
}

// mdChunk is one raw "## "-delimited section: header line + body lines.
type mdChunk struct {
	header string
	lines  []string
}

// splitChunks splits on "\n## " (the same delimiter v1 recall used), so a
// file recall could read is a file this parser reads identically. A chunk at
// byte 0 and CRLF line endings are both tolerated. The "# Title" prelude is
// dropped without counting it as skipped.
func splitChunks(data string) []mdChunk {
	text := strings.ReplaceAll(data, "\r\n", "\n")
	if strings.HasPrefix(text, "## ") {
		text = "\n" + text
	}
	var out []mdChunk
	for _, part := range strings.Split(text, "\n## ") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lines := strings.Split(part, "\n")
		first := strings.TrimSpace(lines[0])
		if first == "#" || strings.HasPrefix(first, "# ") {
			// File title prelude ("# Learnings"): furniture, not a chunk.
			rest := strings.TrimSpace(strings.Join(lines[1:], "\n"))
			if rest == "" {
				continue
			}
			out = append(out, mdChunk{header: "", lines: lines[1:]})
			continue
		}
		out = append(out, mdChunk{header: first, lines: lines[1:]})
	}
	return out
}

// chunkToItem shapes one chunk into a MemoryItem. ok=false means the chunk
// is not insertable (empty or below the content floor) and must be counted
// as skipped by the caller.
func chunkToItem(header string, lines []string, level, projectName, source string) (MemoryItem, bool) {
	stamp, hasTime, title := parseChunkHeader(header)

	tags, rest := extractTags(lines)
	content, ok := fitContent(strings.Join(rest, "\n"))
	if !ok {
		return MemoryItem{}, false
	}

	confidence := float32(0.8)
	switch {
	case hasTime && len(tags) > 0:
		confidence = 1.0
	case hasTime || len(tags) > 0:
		confidence = 0.9
	}

	snippet := "Migrated from v1 vault " + source
	if title != "" {
		snippet = title + " — migrated from v1 vault " + source
	}

	return MemoryItem{
		Key:            deriveKey(content),
		Content:        content,
		ContextSnippet: snippet,
		Level:          level,
		Scope:          classifyScope(content),
		Tags:           tags,
		Confidence:     confidence,
		Status:         "CONFIRMED",
		Source:         source,
		Project:        projectName,
		CreatedAt:      stamp,
	}, true
}

// headerLayouts are the timestamp shapes the v1 writer could have produced
// (it used "2006-01-02 15:04"); the rest are tolerated for hand-edited files.
var headerLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseChunkHeader pulls an optional leading timestamp out of a "## "
// header. Any trailing words are a human title, kept as the context
// snippet — never parsed as tags (titles are sentences, not keywords).
func parseChunkHeader(header string) (stamp time.Time, hasTime bool, title string) {
	fields := strings.Fields(header)
	if len(fields) == 0 {
		return time.Time{}, false, ""
	}
	if len(fields) >= 2 {
		for _, layout := range []string{"2006-01-02 15:04", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, fields[0]+" "+fields[1]); err == nil {
				return t, true, strings.Join(fields[2:], " ")
			}
		}
	}
	for _, layout := range headerLayouts {
		if t, err := time.Parse(layout, fields[0]); err == nil {
			return t, true, strings.Join(fields[1:], " ")
		}
	}
	return time.Time{}, false, header
}

// extractTags consumes an optional leading "tags:" line (case-insensitive,
// comma- and/or space-separated) and returns the remaining body lines.
func extractTags(lines []string) ([]string, []string) {
	if len(lines) == 0 {
		return nil, lines
	}
	trimmed := strings.TrimSpace(lines[0])
	if len(trimmed) < 5 || !strings.EqualFold(trimmed[:5], "tags:") {
		return nil, lines
	}
	raw := strings.ReplaceAll(trimmed[5:], ",", " ")
	var tags []string
	seen := map[string]bool{}
	for _, t := range strings.Fields(raw) {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		tags = append(tags, t)
	}
	return tags, lines[1:]
}

// fitContent trims, enforces the schema CHECK floor (reject), and enforces
// the ceiling by truncating on a rune boundary (keep — a clipped memory
// still recalls; a dropped one recalls nothing).
func fitContent(content string) (string, bool) {
	r := []rune(strings.TrimSpace(content))
	if len(r) < minContentRunes {
		return "", false
	}
	if len(r) > maxContentRunes {
		r = r[:maxContentRunes]
	}
	return string(r), true
}

// deriveKey builds a deterministic, human-debuggable key:
// "migrated/<slug>-<crc>". The slug (first words) lets a reader guess the
// memory; the CRC of the normalized content makes identical content hash
// identically (dedup-friendly) and distinct content collide negligibly.
func deriveKey(content string) string {
	slug := slugify(content)
	sum := crc32.ChecksumIEEE([]byte(strings.ToLower(strings.TrimSpace(content))))
	return fmt.Sprintf("migrated/%s-%08x", slug, sum)
}

// slugify keeps the first five alphanumeric words, lowercased, dash-joined.
func slugify(content string) string {
	var words []string
	for _, w := range strings.Fields(content) {
		var b strings.Builder
		for _, r := range strings.ToLower(w) {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(r)
			}
		}
		if b.Len() > 0 {
			words = append(words, b.String())
		}
		if len(words) == 5 {
			break
		}
	}
	if len(words) == 0 {
		return "note"
	}
	return strings.Join(words, "-")
}

// classifyScope is a keyword heuristic over the same vocabulary the plan's
// Memory Processor prompt uses (fact / preference / decision / constraint /
// pattern). Order matters: hard rules beat opinions beat plain facts.
func classifyScope(content string) string {
	low := strings.ToLower(content)
	switch {
	case strings.Contains(low, "must"),
		strings.Contains(low, "never"),
		strings.Contains(low, "always"),
		strings.Contains(low, "required"),
		strings.Contains(low, "do not"):
		return "constraint"
	case strings.Contains(low, "prefer"),
		strings.Contains(low, "likes"),
		strings.Contains(low, "i like"),
		strings.Contains(low, "i prefer"):
		return "preference"
	case strings.Contains(low, "decid"),
		strings.Contains(low, "chose"),
		strings.Contains(low, "chosen"):
		return "decision"
	case strings.Contains(low, "pattern"),
		strings.Contains(low, "convention"),
		strings.Contains(low, "practice"):
		return "pattern"
	default:
		return "fact"
	}
}

// DeduplicateItems drops exact-duplicate content (case-insensitive,
// whitespace-normalized) across global + project files. First item wins so
// import order is stable and the surviving key is deterministic.
func DeduplicateItems(items []MemoryItem) []MemoryItem {
	seen := make(map[string]bool, len(items))
	out := make([]MemoryItem, 0, len(items))
	for _, it := range items {
		key := strings.ToLower(strings.Join(strings.Fields(it.Content), " "))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, it)
	}
	return out
}
