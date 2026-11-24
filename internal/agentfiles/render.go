package agentfiles

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Brief is the project brief (GET /v1/projects/{id}/brief).
type Brief struct {
	Content   string `json:"content"`
	UpdatedAt string `json:"updated_at"`
}

// Memory is one confirmed project-level memory to list in the section.
type Memory struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Content   string `json:"content"`
	Scope     string `json:"scope"`
	UpdatedAt string `json:"updated_at"`
}

// MaxMemories caps how many memories are listed.
const MaxMemories = 10

const (
	memoryItemMaxChars = 280
	briefMinChars      = 200
	emptyBody          = "_No confirmed project memory yet._"
)

// Render builds the managed section body (without markers), capped at
// MaxSectionChars runes. It returns "" when there is nothing to say.
func Render(brief *Brief, memories []Memory) string {
	briefText := ""
	if brief != nil {
		briefText = cleanBlock(brief.Content)
	}
	var items []string
	for _, m := range memories {
		if len(items) >= MaxMemories {
			break
		}
		content := oneLine(m.Content)
		if content == "" {
			continue
		}
		line := "- "
		if key := oneLine(m.Key); key != "" {
			line += "**" + key + "**: "
		}
		items = append(items, truncateRunes(line+content, memoryItemMaxChars))
	}
	if briefText == "" && len(items) == 0 {
		return ""
	}

	header := "## Project memory (from Nexus)\n\n" +
		"_Synced by the Nexus desktop app. Change these in Nexus; edits here are replaced._"
	decisions := ""
	if len(items) > 0 {
		decisions = "### Key decisions\n\n" + strings.Join(items, "\n")
	}
	// Decisions take priority over the brief, but never squeeze the brief
	// below briefMinChars while decisions can still be dropped.
	for len(items) > 0 && briefText != "" &&
		MaxSectionChars-runes(header)-runes(decisions)-24 < briefMinChars {
		items = items[:len(items)-1]
		decisions = ""
		if len(items) > 0 {
			decisions = "### Key decisions\n\n" + strings.Join(items, "\n")
		}
	}
	parts := []string{header}
	if briefText != "" {
		budget := MaxSectionChars - runes(header) - runes(decisions) - 24
		if budget > 0 {
			parts = append(parts, "### Brief\n\n"+truncateRunes(briefText, budget))
		}
	}
	if decisions != "" {
		parts = append(parts, decisions)
	}
	return truncateRunes(strings.Join(parts, "\n\n"), MaxSectionChars)
}

// RenderOrPlaceholder returns Render's output or a short placeholder used
// to clear an existing section when the project has no memory to show.
func RenderOrPlaceholder(brief *Brief, memories []Memory) string {
	if body := Render(brief, memories); body != "" {
		return body
	}
	return emptyBody
}

// Fingerprint hashes everything that affects the written files: the brief
// (content and updated_at), the memory set, and the settings.
func Fingerprint(brief *Brief, memories []Memory, s Settings) string {
	h := sha256.New()
	if brief != nil {
		fmt.Fprintf(h, "brief\x00%s\x00%s\x00", brief.UpdatedAt, brief.Content)
	}
	for _, m := range memories {
		fmt.Fprintf(h, "mem\x00%s\x00%s\x00%s\x00%s\x00", m.ID, m.UpdatedAt, m.Key, m.Content)
	}
	s = s.Normalize()
	fmt.Fprintf(h, "settings\x00%v\x00%v\x00%s", s.Enabled, s.CreateMissing, strings.Join(s.Files, ","))
	return hex.EncodeToString(h.Sum(nil))
}

// sanitize keeps rendered text from forging or breaking the markers.
func sanitize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "nexus:memory:start", "nexus-memory-start")
	s = strings.ReplaceAll(s, "nexus:memory:end", "nexus-memory-end")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return s
}

func cleanBlock(s string) string {
	s = sanitize(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n ")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(sanitize(s)), " ")
}

func runes(s string) int { return utf8.RuneCountInString(s) }

// truncateRunes cuts s to at most max runes, ending with an ellipsis.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if runes(s) <= max {
		return s
	}
	r := []rune(s)
	if max == 1 {
		return "…"
	}
	return strings.TrimRight(string(r[:max-1]), " \n") + "…"
}
