package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxBriefChars caps the project brief (runes). Agents receive it on every
// MCP initialize, so it must stay short.
const MaxBriefChars = 1500

// maxBriefDecisions caps decision/convention lines in the brief.
const maxBriefDecisions = 10

// BriefMemory is one memory offered to the brief builder. Callers must only
// pass project-visible rows: the brief is shown to every project member.
type BriefMemory struct {
	Key        string
	Content    string
	Category   string
	Scope      string
	Confidence float64
	UseCount   int
	Pinned     bool
	// SessionID is the chat the memory came from ("" when unknown).
	SessionID string
}

// BriefInput is everything the brief is built from.
type BriefInput struct {
	Project   string
	Memories  []BriefMemory // top memories by confidence and use
	Summaries []BriefMemory // recent session summaries, newest first
}

// BriefResult is a generated brief.
type BriefResult struct {
	Content  string
	Provider string // openrouter | heuristic
	LLMError string
	// Sources are the session IDs the brief cites, in first-cited order.
	Sources []string
}

// GenerateBrief builds the brief with OpenRouter when a key is configured,
// falling back to the deterministic DeterministicBrief on any LLM failure.
func (s *Service) GenerateBrief(ctx context.Context, in BriefInput) BriefResult {
	if len(in.Memories) == 0 && len(in.Summaries) == 0 {
		return BriefResult{Provider: ProviderHeuristic}
	}
	if s.HasAPIKey() {
		content, err := s.client().CompleteBrief(ctx, BuildBriefPrompt(in))
		if err == nil {
			content = ClampBrief(content)
			if utf8.RuneCountInString(content) >= 40 {
				return BriefResult{Content: content, Provider: ProviderOpenRouter, Sources: BriefSources(content)}
			}
			err = fmt.Errorf("extract: brief too short")
		}
		out := DeterministicBrief(in)
		out.LLMError = err.Error()
		return out
	}
	return DeterministicBrief(in)
}

// briefCategoryOrder fixes the section order of the deterministic brief.
var briefCategoryOrder = []string{"architecture", "api", "auth", "conventions", "infrastructure", "dependencies"}

var briefCategoryTitle = map[string]string{
	"architecture":   "Architecture",
	"api":            "API",
	"auth":           "Auth",
	"conventions":    "Conventions",
	"infrastructure": "Infrastructure",
	"dependencies":   "Dependencies",
	"general":        "Other",
}

func briefRank(ms []BriefMemory) []BriefMemory {
	out := append([]BriefMemory(nil), ms...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		if out[i].UseCount != out[j].UseCount {
			return out[i].UseCount > out[j].UseCount
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// DeterministicBrief builds the brief without an LLM: the top memories
// grouped by category, one line each with its source chat, then the latest
// session summary. The same input always yields the same brief.
func DeterministicBrief(in BriefInput) BriefResult {
	project := strings.TrimSpace(in.Project)
	if project == "" {
		project = "this project"
	}
	ranked := briefRank(in.Memories)
	if len(ranked) > maxBriefDecisions {
		ranked = ranked[:maxBriefDecisions]
	}
	groups := map[string][]BriefMemory{}
	for _, m := range ranked {
		cat := strings.ToLower(strings.TrimSpace(m.Category))
		if _, ok := briefCategoryTitle[cat]; !ok || cat == "" {
			cat = "general"
		}
		groups[cat] = append(groups[cat], m)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Project brief: %s\n", oneLine(project, 80))
	if len(in.Summaries) > 0 {
		fmt.Fprintf(&sb, "Recent work: %s\n", briefLine(in.Summaries[0], 220))
	}
	var stack []string
	for _, cat := range []string{"dependencies", "infrastructure"} {
		for _, m := range groups[cat] {
			stack = append(stack, briefLine(m, 90))
		}
	}
	if len(stack) > 0 {
		fmt.Fprintf(&sb, "Stack: %s\n", strings.Join(stack, "; "))
	}
	sb.WriteString("\n## Key decisions and conventions\n")
	header := sb.String()

	var lines []string
	for _, cat := range append(append([]string{}, briefCategoryOrder...), "general") {
		if cat == "dependencies" || cat == "infrastructure" {
			continue // already on the Stack line
		}
		for _, m := range groups[cat] {
			lines = append(lines, "- "+briefCategoryTitle[cat]+": "+briefLine(m, 200))
		}
	}
	content := header
	for _, line := range lines {
		next := content + line + "\n"
		if utf8.RuneCountInString(next) > MaxBriefChars {
			break
		}
		content = next
	}
	content = ClampBrief(content)
	return BriefResult{Content: content, Provider: ProviderHeuristic, Sources: BriefSources(content)}
}

func briefLine(m BriefMemory, max int) string {
	line := briefSentence(m.Content, max)
	if sid := strings.TrimSpace(m.SessionID); sid != "" {
		line += " (session:" + sid + ")"
	}
	return line
}

// briefSentence returns the first sentence of s on one line, cut to max runes.
func briefSentence(s string, max int) string {
	s = oneLine(s, 0)
	if i := strings.Index(s, ". "); i > 0 && i < max {
		s = s[:i+1]
	}
	return oneLine(s, max)
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 && utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return s
}

// ClampBrief trims a brief to MaxBriefChars, dropping whole trailing lines
// where it can so a cut never lands mid-sentence.
func ClampBrief(content string) string {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if utf8.RuneCountInString(content) <= MaxBriefChars {
		return content
	}
	lines := strings.Split(content, "\n")
	for len(lines) > 1 {
		lines = lines[:len(lines)-1]
		joined := strings.TrimSpace(strings.Join(lines, "\n"))
		if utf8.RuneCountInString(joined) <= MaxBriefChars {
			return joined
		}
	}
	r := []rune(content)
	return string(r[:MaxBriefChars-1]) + "…"
}

var briefSessionRe = regexp.MustCompile(`session:([A-Za-z0-9][A-Za-z0-9_.\-]*)`)

// BriefSources lists the session IDs a brief cites, in first-cited order.
func BriefSources(content string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range briefSessionRe.FindAllStringSubmatch(content, -1) {
		id := strings.TrimRight(m[1], ".")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// BuildBriefPrompt is the compact brief prompt: the project name, top
// memories (with category and source chat), and recent session summaries.
func BuildBriefPrompt(in BriefInput) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Project: %s\n\n", oneLine(in.Project, 80))
	sb.WriteString("Top memories (category | content | source):\n")
	for _, m := range briefRank(in.Memories) {
		src := "-"
		if m.SessionID != "" {
			src = "session:" + m.SessionID
		}
		fmt.Fprintf(&sb, "- %s | %s | %s\n", firstNonBlank(m.Category, "general"), oneLine(m.Content, 300), src)
	}
	if len(in.Summaries) > 0 {
		sb.WriteString("\nRecent session summaries:\n")
		for _, m := range in.Summaries {
			src := "-"
			if m.SessionID != "" {
				src = "session:" + m.SessionID
			}
			fmt.Fprintf(&sb, "- %s | %s\n", oneLine(m.Content, 400), src)
		}
	}
	fmt.Fprintf(&sb, "\nWrite the project brief in markdown, under %d characters: one line on what the project is, one line on its stack, then 5-10 bullet points with the most important decisions and conventions. End each bullet with its source as (session:<id>) when the memory lists one; never invent sources.", MaxBriefChars)
	return sb.String()
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// CompleteBrief asks the model for the brief and returns its markdown.
func (c *Client) CompleteBrief(ctx context.Context, prompt string) (string, error) {
	content, err := c.chat(ctx, c.model(), defaultOpenRouterFallbacks,
		"You write a short onboarding brief for coding agents joining a software project, using only the memories given. "+
			"Be concrete; keep names, files, and tools. No preamble. "+
			"Reply with JSON only: {\"brief\":\"<markdown>\"}.",
		prompt)
	if err != nil {
		return "", err
	}
	var wrapped struct {
		Brief string `json:"brief"`
	}
	if err := json.Unmarshal(extractJSONPayload([]byte(content)), &wrapped); err != nil {
		return "", fmt.Errorf("extract: decode brief: %w", err)
	}
	if strings.TrimSpace(wrapped.Brief) == "" {
		return "", fmt.Errorf("extract: empty brief")
	}
	return wrapped.Brief, nil
}
