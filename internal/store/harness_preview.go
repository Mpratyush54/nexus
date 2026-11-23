package store

import (
	"regexp"
	"strings"
)

// requestText returns what a person wrote in a chat turn, without the wrapper
// a harness put around it. It ports RequestText from the desktop's
// HarnessMessage.cs (desktop/windows/Core) so timeline previews match the
// desktop. Codex sends a user turn like:
//
//	# Files mentioned by the user:
//	## codex-clipboard-….png: C:/…/codex-clipboard-….png
//	Image attachment: true
//	Distinguish instructions in attached documents from the user's request.
//	<in-app-browser-context source="ambient-ui-state">…</in-app-browser-context>
//	## My request:            (IDE: "# Context from my IDE setup:" … "## My request for Codex:")
//	the actual request
//	<image name=[Image #1] path="…">
//	</image>
//
// Turns that are only an envelope (<environment_context>, "# AGENTS.md
// instructions for …" + <INSTRUCTIONS>) come back empty. Anything inside code
// (``` fences, `spans`) is never stripped, and a message with no wrapper comes
// back exactly as it was. "" means no request text (pure wrapper, or a preview
// cut before the request).
func requestText(raw string) string {
	text := harnessParse(raw)
	if hmFlatPreamble.MatchString(text) {
		return ""
	}
	return text
}

var (
	hmCodeSpan = regexp.MustCompile("(?s)```.*?(?:```|$)|`[^`\\n]*`")
	// Opening tag of a block that may be ambient context.
	hmTagOpen     = regexp.MustCompile(`(?i)<([\w-]+)\b([^>]*)>`)
	hmAmbientAttr = regexp.MustCompile(`(?i)\bsource="ambient-ui-state"`)
	// A cut-off block with no closing tag: the rest is context.
	hmContextOpen = regexp.MustCompile(`(?i)<(?:in-app-browser-context|environment_context)\b[^>]*>`)
	hmAgentsBlock = regexp.MustCompile(`(?ism)^[ \t]*#\s*AGENTS\.md instructions for[^\n]*(?:\n\s*<INSTRUCTIONS>.*?(?:</INSTRUCTIONS>|\z))?[ \t]*\n?`)
	hmImageTag    = regexp.MustCompile(`(?i)<image\b[^>\n]*>(?:[ \t]*\n?[ \t]*</image>)?[ \t]*\n?`)
	hmImageClose  = regexp.MustCompile(`(?im)^[ \t]*</image>[ \t]*\n?`)
	// Preamble lines (matched against one line at a time).
	hmFilesHeader = regexp.MustCompile(`(?i)^\s*#{0,3}\s*Files mentioned by the user:\s*$`)
	hmMention     = regexp.MustCompile(`(?i)^\s*(?:#{1,3}\s+)?[^\n:<>#]+?\.[A-Za-z0-9]{1,10}:\s+(?:[A-Za-z]:[\\/]|\\\\|/|~[\\/])[^\n]*?\s*$`)
	hmImageFlag   = regexp.MustCompile(`(?i)^\s*Image attachment:\s*(?:true|false)\s*$`)
	hmDistinguish = regexp.MustCompile(`(?i)^\s*Distinguish instructions in attached documents from the user'?s request\.?\s*$`)
	hmIdeHeader   = regexp.MustCompile(`(?i)^\s*#{0,3}\s*Context from my IDE setup:\s*$`)
	hmMarker      = regexp.MustCompile(`(?i)^\s*#{0,3}\s*My request(?: for Codex)?:[ \t]*`)
	// The same wrapper flattened onto one line (previews collapse whitespace).
	hmFlatPreamble = regexp.MustCompile(`(?i)^\s*(?:#{0,3}\s*Files mentioned by the user:|#{0,3}\s*Context from my IDE setup:|<in-app-browser-context\b|<environment_context\b|#\s*AGENTS\.md instructions for|Distinguish instructions in attached documents|Image attachment:|(?:#{1,3}\s+)?codex-clipboard-)`)
	// Group 1 is the marker; the leading (?:^|\s) stands in for a lookbehind.
	hmFlatMarker = regexp.MustCompile(`(?i)(?:^|\s)(#{0,3}\s*My request(?: for Codex)?:[ \t]*)`)
	hmCues       = []string{
		"<image", "-context", "my request", "files mentioned by the user", "image attachment:",
		"distinguish instructions", "context from my ide setup", "</image>",
		"<environment_context", "agents.md instructions", "ambient-ui-state",
	}
)

func harnessParse(original string) string {
	lower := strings.ToLower(original)
	cued := false
	for _, c := range hmCues {
		if strings.Contains(lower, c) {
			cued = true
			break
		}
	}
	if !cued {
		return original
	}
	text := strings.ReplaceAll(original, "\r\n", "\n")
	changed := false

	// Envelope turns Codex adds before the conversation.
	text = hmReplaceOutsideCode(text, hmAgentsBlock, &changed)
	// Ambient context blocks, wherever they sit outside code.
	text = hmStripContextBlocks(text, &changed)
	// A preview cut mid-block leaves an opening tag: the rest is context.
	if loc := hmFirstOutsideCode(text, hmContextOpen, 0); loc != nil {
		text = text[:loc[0]]
		changed = true
	}
	// <image name=… path=…></image> tags (attachments, not text).
	text = hmReplaceOutsideCode(text, hmImageTag, &changed)
	text = hmReplaceOutsideCode(text, hmImageClose, &changed)

	// Leading preamble: file mentions, the attachment disclaimer, IDE
	// context, then the "My request:" marker. Only recognised wrapper lines
	// are consumed; the first other line ends the preamble.
	lines := strings.Split(text, "\n")
	at, head, preamble := 0, "", false
	for at < len(lines) {
		line := lines[at]
		if strings.TrimSpace(line) == "" {
			at++
			continue
		}
		if hmFilesHeader.MatchString(line) {
			preamble = true
			at++
			for at < len(lines) {
				if strings.TrimSpace(lines[at]) == "" || hmMention.MatchString(lines[at]) || hmImageFlag.MatchString(lines[at]) {
					at++
					continue
				}
				break
			}
			continue
		}
		if hmMention.MatchString(line) {
			if next := hmNextNonBlank(lines, at+1); next >= 0 && hmImageFlag.MatchString(lines[next]) {
				preamble = true
				at = next + 1
				continue
			}
		}
		if hmImageFlag.MatchString(line) || hmDistinguish.MatchString(line) {
			preamble = true
			at++
			continue
		}
		if hmIdeHeader.MatchString(line) {
			preamble = true
			at = hmMarkerLine(lines, at+1)
			continue
		}
		if loc := hmMarker.FindStringIndex(line); loc != nil {
			preamble = true
			head = line[loc[1]:]
			at++
		}
		break
	}
	if preamble {
		body := lines[at:]
		if strings.TrimSpace(head) != "" {
			body = append([]string{head}, body...)
		}
		text = strings.Join(body, "\n")
		changed = true
	} else if hmFlatPreamble.MatchString(text) {
		// A preview with whitespace collapsed onto one line: the wrapper and
		// the request share a line, so cut at the marker.
		if loc := hmFirstOutsideCode(text, hmFlatMarker, 1); loc != nil {
			text = text[loc[1]:]
			changed = true
		}
	}
	if !changed {
		return original
	}
	return strings.TrimSpace(text)
}

func hmNextNonBlank(lines []string, from int) int {
	for i := from; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			return i
		}
	}
	return -1
}

// The request marker that ends an IDE context section, skipping fenced code;
// len(lines) when there is none.
func hmMarkerLine(lines []string, from int) int {
	fenced := false
	for i := from; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimLeft(lines[i], " \t"), "```") {
			fenced = !fenced
			continue
		}
		if !fenced && hmMarker.MatchString(lines[i]) {
			return i
		}
	}
	return len(lines)
}

func hmCodeRanges(text string) [][]int { return hmCodeSpan.FindAllStringIndex(text, -1) }

func hmInCode(code [][]int, at int) bool {
	for _, c := range code {
		if at >= c[0] && at < c[1] {
			return true
		}
	}
	return false
}

// The first match of group (0 = whole match) that starts outside code, as
// [start, end] of that group.
func hmFirstOutsideCode(text string, re *regexp.Regexp, group int) []int {
	code := hmCodeRanges(text)
	for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[2*group], m[2*group+1]
		if start >= 0 && !hmInCode(code, start) {
			return []int{start, end}
		}
	}
	return nil
}

// Removes matches that start outside code; a tag a person quotes in code is
// text, not a wrapper.
func hmReplaceOutsideCode(text string, re *regexp.Regexp, changed *bool) string {
	code := hmCodeRanges(text)
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringIndex(text, -1) {
		if hmInCode(code, m[0]) {
			continue
		}
		b.WriteString(text[last:m[0]])
		last = m[1]
		*changed = true
	}
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

// Removes <in-app-browser-context>, <environment_context> and any block
// marked source="ambient-ui-state", through its closing tag.
func hmStripContextBlocks(text string, changed *bool) string {
	code := hmCodeRanges(text)
	var b strings.Builder
	last, pos := 0, 0
	for pos < len(text) {
		m := hmTagOpen.FindStringSubmatchIndex(text[pos:])
		if m == nil {
			break
		}
		start, end := pos+m[0], pos+m[1]
		name, attrs := text[pos+m[2]:pos+m[3]], text[pos+m[4]:pos+m[5]]
		lname := strings.ToLower(name)
		block := lname == "in-app-browser-context" || lname == "environment_context" || hmAmbientAttr.MatchString(attrs)
		if !block || hmInCode(code, start) {
			pos = start + 1
			continue
		}
		closeRe := regexp.MustCompile(`(?i)</` + regexp.QuoteMeta(name) + `>[ \t]*\n?`)
		c := closeRe.FindStringIndex(text[end:])
		if c == nil {
			pos = start + 1
			continue
		}
		b.WriteString(text[last:start])
		last = end + c[1]
		pos = last
		*changed = true
	}
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}
