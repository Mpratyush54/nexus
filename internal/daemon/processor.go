// Package daemon implements the local workspace daemon.
//
// processor.go is the Memory Processor (implementation-plan.md §2.6/§2.7/§2.8,
// §1.7 + GitHub Mpratyush54/nexus issue #10): a background goroutine that runs
// on the DESIGNATED workspace only (workspaces.is_designated_processor, the
// project owner's daemon) and turns raw events from all four extraction layers
// into structured memory proposals. No server-side LLM cost: extraction runs
// here, on the user's device, under the user's own LLM key.
//
// It reuses the existing Event (harvester.go, Layer 2 conversation turns +
// SESSION_TRANSCRIPT_COMPLETE batches) and ToolEvent (interceptor.go, Layer 1
// tool/file/git activity) envelopes — neither is redefined here. Stdlib only.
package daemon

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ---------------------------------------------------------------------------
// Memory taxonomy (plan §1.1 memory_items, §2.6 classification prompt)
// ---------------------------------------------------------------------------

// MemoryLevel is the lifetime/scope tier of a memory item.
// Resolution order (lower overrides higher): EPHEMERAL > SESSION > PERSONAL >
// PROJECT > ORGANIZATION (plan §1.6; ephemeral works at plan §1.1's fifth tier).
type MemoryLevel string

const (
	LevelOrganization MemoryLevel = "organization"
	LevelProject      MemoryLevel = "project"
	LevelPersonal     MemoryLevel = "personal"
	LevelSession      MemoryLevel = "session"
	// LevelEphemeral is assigned explicitly by producers for working memory
	// that must never outlive its session. Heuristic classification never
	// emits it (ClassifyLevel defaults to SESSION when unsure).
	LevelEphemeral MemoryLevel = "ephemeral"
)

// MemoryScope is the kind of knowledge a memory item carries (plan §2.6).
type MemoryScope string

const (
	ScopeFact           MemoryScope = "fact"
	ScopePreference     MemoryScope = "preference"
	ScopeDecision       MemoryScope = "decision"
	ScopeConstraint     MemoryScope = "constraint"
	ScopePattern        MemoryScope = "pattern"
	ScopeEpisodeSummary MemoryScope = "episode_summary"
)

// ---------------------------------------------------------------------------
// Records, proposals, store interface
// ---------------------------------------------------------------------------

// MemoryRecord is an already-known (confirmed or proposed) memory item the
// processor deduplicates against. It mirrors the store.memory_items row shape
// without importing internal/store (which needs pgx/pgvector while the daemon
// stays stdlib-only) — field names match so mapping is a plain struct copy.
type MemoryRecord struct {
	Key        string
	Content    string
	Level      MemoryLevel
	Scope      MemoryScope
	Confidence float64
	SessionID  string
}

// Proposal is one extracted memory candidate awaiting confirmation.
type Proposal struct {
	Key        string
	Content    string
	Level      MemoryLevel
	Scope      MemoryScope
	Confidence float64
	Source     string // e.g. "processor:conversation", "processor:episode"
	Explicit   bool   // extracted from an explicit user statement (1h confirm)
	// ConfirmAfter is populated by ConfirmationDelay; how long after proposal
	// the item auto-confirms if nobody rejects it (plan §2.8).
	ConfirmAfter time.Duration
	// ProposedAt is set by the Processor when the proposal is created.
	ProposedAt time.Time
}

// MemoryStore is the minimal persistence surface the Processor needs. The
// production wiring backs it with internal/store (Postgres); tests and the
// local-device path use InMemoryStore. Kept local to this file so the daemon
// package never imports the pgx-backed store.
type MemoryStore interface {
	// Existing returns confirmed (and proposed) memories for dedup.
	Existing() []MemoryRecord
	// Save persists one proposal.
	Save(p Proposal) error
}

// InMemoryStore is a mutex-guarded MemoryStore for tests and offline runs.
type InMemoryStore struct {
	mu    sync.Mutex
	items []MemoryRecord
	saved []Proposal
}

// NewInMemoryStore seeds a store with already-known memories.
func NewInMemoryStore(seed []MemoryRecord) *InMemoryStore {
	return &InMemoryStore{items: append([]MemoryRecord(nil), seed...)}
}

// Existing implements MemoryStore.
func (s *InMemoryStore) Existing() []MemoryRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]MemoryRecord(nil), s.items...)
}

// Save implements MemoryStore.
func (s *InMemoryStore) Save(p Proposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, p)
	s.items = append(s.items, MemoryRecord{
		Key:        p.Key,
		Content:    p.Content,
		Level:      p.Level,
		Scope:      p.Scope,
		Confidence: p.Confidence,
	})
	return nil
}

// Saved returns proposals accepted so far (inspection helper for tests).
func (s *InMemoryStore) Saved() []Proposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Proposal(nil), s.saved...)
}

// ---------------------------------------------------------------------------
// Confirmation timers (plan §2.8)
// ---------------------------------------------------------------------------

const (
	// DefaultConfirmAfter: PROPOSED items auto-confirm after 24h.
	DefaultConfirmAfter = 24 * time.Hour
	// HighConfidenceConfirmAfter: items with confidence > 0.9 confirm after 4h.
	HighConfidenceConfirmAfter = 4 * time.Hour
	// ExplicitConfirmAfter: items from explicit user statements confirm after 1h.
	ExplicitConfirmAfter = 1 * time.Hour
	// HighConfidenceThreshold marks "high-confidence" items.
	HighConfidenceThreshold = 0.9
)

// ConfirmationDelay returns how long a proposal waits before auto-confirming:
// explicit user statements → 1h; confidence > 0.9 → 4h; otherwise 24h.
func ConfirmationDelay(p Proposal) time.Duration {
	if p.Explicit {
		return ExplicitConfirmAfter
	}
	if p.Confidence > HighConfidenceThreshold {
		return HighConfidenceConfirmAfter
	}
	return DefaultConfirmAfter
}

// ---------------------------------------------------------------------------
// Classification (plan §2.6 prompt)
// ---------------------------------------------------------------------------

// personalMarkers signal PERSONAL level ("I prefer", "I like", "I always").
var personalMarkers = []string{
	"i prefer", "i like", "i love", "i hate", "i always", "i never",
	"my preference", "my style", "i want you to",
}

// sessionMarkers signal SESSION level ("for now", "right now", ...).
var sessionMarkers = []string{
	"for now", "right now", "during this", "in this task", "in this session",
	"temporarily", "just for this", "don't touch", "do not touch",
	"do not modify", "don't modify",
}

// organizationMarkers signal ORGANIZATION level (universal policy).
var organizationMarkers = []string{
	"all apis", "all projects", "every project", "across all",
	"company policy", "company-wide", "company wide", "org-wide",
	"organization policy", "all services must", "all apis must",
}

// projectMarkers signal PROJECT level (team decision / codebase fact).
var projectMarkers = []string{
	"we use", "we decided", "team decided", "team uses", "our stack",
	"our codebase", "the project uses", "we chose", "agreed by the team",
}

// ClassifyLevel assigns a memory level using the §2.6 heuristics. Priority:
// personal markers → session markers → organization markers → project
// markers → SESSION (safe default: session items can be promoted later, plan
// §2.7, while an over-scoped item would leak across sessions).
func ClassifyLevel(text string) MemoryLevel {
	lowered := strings.ToLower(text)
	for _, m := range personalMarkers {
		if strings.Contains(lowered, m) {
			return LevelPersonal
		}
	}
	for _, m := range sessionMarkers {
		if strings.Contains(lowered, m) {
			return LevelSession
		}
	}
	for _, m := range organizationMarkers {
		if strings.Contains(lowered, m) {
			return LevelOrganization
		}
	}
	for _, m := range projectMarkers {
		if strings.Contains(lowered, m) {
			return LevelProject
		}
	}
	return LevelSession
}

// ClassifyScope assigns a memory scope using the §2.6 heuristics. Priority:
// constraint (hard rules must win) → episode_summary → decision → preference
// → pattern → fact (neutral default).
func ClassifyScope(text string) MemoryScope {
	lowered := strings.ToLower(text)
	for _, m := range []string{
		"must not", "must never", "never ", "do not ", "don't ",
		"required", "forbidden", "prohibited", "always use jwt",
		"no api key",
	} {
		if strings.Contains(lowered, m) {
			return ScopeConstraint
		}
	}
	for _, m := range []string{
		"root cause", "root-cause", "bug fix", "bugfix", "incident",
		"postmortem", "post-mortem", "how we fixed",
	} {
		if strings.Contains(lowered, m) {
			return ScopeEpisodeSummary
		}
	}
	for _, m := range []string{
		"decided", "decision", "chose ", "chosen", "agreed",
		"over memcached", "over ", "trade-off", "tradeoff",
	} {
		if strings.Contains(lowered, m) {
			return ScopeDecision
		}
	}
	for _, m := range []string{
		"prefer", "preference", "i like", "i love", "i hate", "my style",
	} {
		if strings.Contains(lowered, m) {
			return ScopePreference
		}
	}
	for _, m := range []string{
		"pattern", "convention", "typically", "usually", "best practice",
	} {
		if strings.Contains(lowered, m) {
			return ScopePattern
		}
	}
	return ScopeFact
}

// explicitMarkers detect explicit user statements (1h auto-confirm lane).
// These are first-person declarations or imperatives, not inferred context.
// Keep phrases specific — bare "must "/"never "/"do not " match agent prompts.
var explicitMarkers = []string{
	"i prefer", "i like", "i want", "i decided", "we decided",
	"let's use", "lets use", "use redis", "we must ", "i must ",
	"never use", "always use", "must not", "must never",
	"don't use", "do not use",
}

// IsExplicitStatement reports whether text looks like an explicit user
// statement rather than inferred background context.
func IsExplicitStatement(text string) bool {
	lowered := strings.ToLower(text)
	for _, m := range explicitMarkers {
		if strings.Contains(lowered, m) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Deduplication (issue #10: skip proposal if similarity > 0.9)
// ---------------------------------------------------------------------------

// DuplicateSimilarityThreshold mirrors the issue: cosine similarity > 0.9
// against an existing confirmed memory discards the proposal.
const DuplicateSimilarityThreshold = 0.9

// tokenize lowercases text into alphanumeric word tokens (stdlib-only
// embedding substitute: bag-of-words vectors, no model dependency).
func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// CosineSimilarity returns the cosine similarity of two texts' bag-of-words
// vectors in [0, 1]. Production replaces this with pgvector embedding cosine
// distance; the threshold semantics (> 0.9 ⇒ duplicate) stay identical.
func CosineSimilarity(a, b string) float64 {
	fa := map[string]float64{}
	for _, t := range tokenize(a) {
		fa[t]++
	}
	fb := map[string]float64{}
	for _, t := range tokenize(b) {
		fb[t]++
	}
	if len(fa) == 0 || len(fb) == 0 {
		return 0
	}
	var dot, na, nb float64
	for tok, ca := range fa {
		na += ca * ca
		if cb, ok := fb[tok]; ok {
			dot += ca * cb
		}
	}
	for _, cb := range fb {
		nb += cb * cb
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt(na) * sqrt(nb))
}

// sqrt is a local square-root helper (Newton iteration) so this file stays
// import-lean; semantics match math.Sqrt for the non-negative inputs used.
func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x / 2
	if z == 0 {
		z = 1
	}
	for i := 0; i < 32; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}

// IsDuplicate reports whether content is a near-duplicate (> 0.9 similarity)
// of any existing memory, with the best score found.
func IsDuplicate(content string, existing []MemoryRecord) (bool, float64) {
	best := 0.0
	for _, m := range existing {
		if s := CosineSimilarity(content, m.Content); s > best {
			best = s
		}
	}
	return best > DuplicateSimilarityThreshold, best
}

// Deduplicate filters proposals, dropping any that duplicate existing
// memories OR earlier proposals in the same batch (keeps first occurrence).
func Deduplicate(proposals []Proposal, existing []MemoryRecord) []Proposal {
	seen := append([]MemoryRecord(nil), existing...)
	out := make([]Proposal, 0, len(proposals))
	for _, p := range proposals {
		if dup, _ := IsDuplicate(p.Content, seen); dup {
			continue
		}
		out = append(out, p)
		seen = append(seen, MemoryRecord{
			Key: p.Key, Content: p.Content, Level: p.Level,
			Scope: p.Scope, Confidence: p.Confidence,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Promotion (plan §2.7)
// ---------------------------------------------------------------------------

// PromotionSessionThreshold: a SESSION fact seen in 3+ sessions is
// auto-proposed for promotion to PROJECT.
const PromotionSessionThreshold = 3

// ShouldPromoteSessionToProject reports whether a session-scoped fact observed
// in sessionCount distinct sessions should be promoted to project scope.
func ShouldPromoteSessionToProject(sessionCount int) bool {
	return sessionCount >= PromotionSessionThreshold
}

// PromotionFor maps a record + its distinct-session count to the level it
// should hold: SESSION records at/above threshold become PROJECT; everything
// else keeps its level ("" means "no promotion").
func PromotionFor(rec MemoryRecord, sessionCount int) MemoryLevel {
	if rec.Level == LevelSession && ShouldPromoteSessionToProject(sessionCount) {
		return LevelProject
	}
	return ""
}

// ---------------------------------------------------------------------------
// Episode auto-detection (plan §2.3)
// ---------------------------------------------------------------------------

// Episode is a detected bug/incident arc over Layer 1 tool events.
type Episode struct {
	Type          string   // e.g. "bug_fix"
	Title         string   // short human title derived from the trigger
	Trigger       string   // failing command + output excerpt
	Investigation string   // files read during diagnosis
	Resolution    string   // files modified + commit message
	Verification  string   // passing re-run of the same command
	FilesInvolved []string // modified files
	ErrorPatterns []string // first error-ish line(s) of the trigger output
	Status        string   // "OPEN" | "RESOLVED" (commit present ⇒ RESOLVED)
}

func toolStr(payload map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := payload[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func toolInt(payload map[string]any, keys ...string) (int, bool) {
	for _, k := range keys {
		switch v := payload[k].(type) {
		case int:
			return v, true
		case int64:
			return int(v), true
		case float64:
			return int(v), true
		}
	}
	return 0, false
}

func toolCommandKey(payload map[string]any) string {
	cmd := toolStr(payload, "command")
	var args []string
	if a, ok := payload["args"].([]string); ok {
		args = a
	} else if a, ok := payload["args"].([]any); ok {
		for _, x := range a {
			if s, ok := x.(string); ok {
				args = append(args, s)
			}
		}
	}
	return strings.TrimSpace(cmd + " " + strings.Join(args, " "))
}

// errorExcerpt pulls the first non-empty output line as the error pattern.
func errorExcerpt(payload map[string]any) string {
	out := toolStr(payload, "output", "stderr", "stdout")
	for _, line := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			if len(s) > 200 {
				s = s[:200]
			}
			return s
		}
	}
	return ""
}

// DetectEpisodePattern scans Layer 1 ToolEvents for the §2.3 bug arc:
//
//	COMMAND_EXECUTED(exit≠0) → FILE_READ(s) → FILE_MODIFIED →
//	COMMAND_EXECUTED(same command, exit=0) → GIT_COMMITTED
//
// It returns the first complete arc found, or nil. Investigation reads and
// the closing commit are optional (arc still counts without them); the
// trigger, fix, and passing re-run are required.
func DetectEpisodePattern(evs []ToolEvent) *Episode {
	for i := 0; i < len(evs); i++ {
		if evs[i].Type != ToolEventCommandExecuted {
			continue
		}
		code, ok := toolInt(evs[i].Payload, "exit_code")
		if !ok || code == 0 {
			continue
		}
		triggerCmd := toolCommandKey(evs[i].Payload)
		triggerErr := errorExcerpt(evs[i].Payload)

		var reads, fixes []string
		fixIdx := -1
		for j := i + 1; j < len(evs); j++ {
			switch evs[j].Type {
			case ToolEventFileRead:
				if p := toolStr(evs[j].Payload, "path"); p != "" {
					reads = append(reads, p)
				}
			case ToolEventFileModified:
				if p := toolStr(evs[j].Payload, "path"); p != "" {
					fixes = append(fixes, p)
				}
				if fixIdx == -1 {
					fixIdx = j
				}
			}
		}
		if fixIdx == -1 {
			continue // no attempted fix after this trigger
		}
		verifyIdx := -1
		var verification string
		for j := fixIdx + 1; j < len(evs); j++ {
			if evs[j].Type != ToolEventCommandExecuted {
				continue
			}
			code, ok := toolInt(evs[j].Payload, "exit_code")
			if !ok || code != 0 {
				continue
			}
			if triggerCmd != "" && toolCommandKey(evs[j].Payload) != triggerCmd {
				continue // must be the SAME command re-run
			}
			verifyIdx = j
			verification = toolCommandKey(evs[j].Payload)
			break
		}
		if verifyIdx == -1 {
			continue // fix never verified green
		}
		var commitMsg string
		for j := verifyIdx + 1; j < len(evs); j++ {
			if evs[j].Type == ToolEventGitCommitted {
				commitMsg = toolStr(evs[j].Payload, "message")
				break
			}
		}
		title := triggerCmd
		if title == "" {
			title = "Recurring failure fixed"
		}
		if triggerErr != "" {
			title = triggerCmd + ": " + triggerErr
		}
		ep := &Episode{
			Type:          "bug_fix",
			Title:         title,
			Trigger:       strings.TrimSpace(triggerCmd + "\n" + triggerErr),
			Investigation: "Read: " + strings.Join(reads, ", "),
			Resolution:    "Fixed: " + strings.Join(fixes, ", ") + commitSuffix(commitMsg),
			Verification:  "Re-ran green: " + verification,
			FilesInvolved: append([]string(nil), fixes...),
			Status:        "OPEN",
		}
		if triggerErr != "" {
			ep.ErrorPatterns = []string{triggerErr}
		}
		if commitMsg != "" {
			ep.Status = "RESOLVED"
		}
		return ep
	}
	return nil
}

func commitSuffix(msg string) string {
	if strings.TrimSpace(msg) == "" {
		return ""
	}
	return " (commit: " + strings.TrimSpace(msg) + ")"
}

// ---------------------------------------------------------------------------
// Provider: local-device LLM (issue #10 — zero server-side LLM cost)
// ---------------------------------------------------------------------------

// Provider extracts memory proposals from events. Implementations MUST run on
// the user's device (the designated daemon); the server never sees prompts or
// keys. HeuristicProvider is the stdlib stub; a network-backed provider can
// replace it later behind this same interface.
type Provider interface {
	// Extract turns one event batch into proposals. ctx carries cancellation;
	// project names the project for the §2.6 prompt header.
	Extract(ctx context.Context, project string, events []Event, existing []MemoryRecord) ([]Proposal, error)
}

// HeuristicProvider is the offline stub: sentence-split conversation content,
// classify level/scope per §2.6, score confidence, mark explicit statements.
// Deterministic and key-free — used when no LLM key is configured and in tests.
// It only proposes durable user/decision-like lines; skill dumps, agent
// instructions, and assistant narration are dropped.
type HeuristicProvider struct{}

// Extract implements Provider.
func (HeuristicProvider) Extract(_ context.Context, _ string, events []Event, _ []MemoryRecord) ([]Proposal, error) {
	var out []Proposal
	for _, ev := range events {
		for _, cand := range eventCandidates(ev) {
			for _, sent := range splitSentences(cand.Text) {
				sent = strings.TrimSpace(sent)
				if len(sent) < 20 {
					continue // plan §1.1: content must be ≥ 20 chars
				}
				if len(sent) > 2000 {
					sent = sent[:2000]
				}
				if !isDurableHeuristic(sent, cand.Speaker) {
					continue
				}
				explicit := IsExplicitStatement(sent)
				conf := 0.75
				if explicit {
					conf = 0.95
				} else if ClassifyScope(sent) == ScopeDecision {
					conf = 0.85
				} else if strings.Contains(strings.ToLower(sent), "because") {
					conf = 0.8
				}
				p := Proposal{
					Key:        KeyFromContent(sent),
					Content:    sent,
					Level:      ClassifyLevel(sent),
					Scope:      ClassifyScope(sent),
					Confidence: conf,
					Source:     "processor:heuristic",
					Explicit:   explicit,
				}
				p.ConfirmAfter = ConfirmationDelay(p)
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// textCandidate is one turn (or detail blob) with its speaker for filtering.
type textCandidate struct {
	Speaker string
	Text    string
}

// eventCandidates pulls speaker-aware texts from conversation-style Events.
func eventCandidates(ev Event) []textCandidate {
	var out []textCandidate
	if ev.Type == EventConversationTurn {
		s, _ := ev.Payload["content"].(string)
		if s == "" {
			return nil
		}
		sp, _ := ev.Payload["speaker"].(string)
		return []textCandidate{{Speaker: sp, Text: s}}
	}
	if ev.Type == EventSessionComplete {
		if turns, ok := ev.Payload["turns"].([]any); ok {
			for _, t := range turns {
				m, ok := t.(map[string]any)
				if !ok {
					continue
				}
				s, _ := m["content"].(string)
				if s == "" {
					continue
				}
				sp, _ := m["speaker"].(string)
				out = append(out, textCandidate{Speaker: sp, Text: s})
			}
		}
		if s, ok := ev.Payload["detail"].(string); ok && s != "" &&
			!strings.Contains(s, "sqlite/vscdb") {
			out = append(out, textCandidate{Speaker: "system", Text: s})
		}
		return out
	}
	return nil
}

// junkMemoryMarkers match agent/skill/prompt dumps that must never become memories.
var junkMemoryMarkers = []string{
	"subagent_type", "skill.md", "use this skill", "launch exactly",
	"full repository path:", "custom instructions:", "by default, the review",
	"when launching this subagent", "run_in_background",
	"agent transcripts", "do not dump entire chat",
	"prefer nexus mcp", "memory_search", "memory_write",
	"always_applied_workspace_rule", "available_skills",
	"you are an ai coding", "follow the user's instructions",
	"diff: branch changes", "change description:",
	"bugbot", "security review", "review-bugbot",
	"<user_query>", "<communication>", "citing_code",
	"tool call", "function calls to help you",
	"<timestamp>", "</timestamp>", "<user_info>", "<git_status>",
	"agent-transcripts", "open_and_recently_viewed", "todo_update",
	"conversation_summary", "calldynamictool", "getdynamictools",
	"you must read the tool schemas", "always inspect a tool",
	"please always cite", "never write a or d",
	"this subagent is single-shot", "model family",
	"namespace and single-tool", "[truncated]",
	"use when ", "use for ", "use this ", "use the ",
	"use `[", "unless the user explicitly", "browser automation",
	"when speaking to the user", "kebab-case model",
	"available_subagent", "subagent_type", "best-of-n",
	"exact prompt shape", "analyze why", "do not use browser",
}

// durableMarkers are phrases that signal a reusable decision/preference/fact.
var durableMarkers = []string{
	"i prefer", "i like", "i want", "i decided", "we decided", "we chose",
	"let's use", "lets use", "we're using", "we are using", "we use ",
	"going with", "switched to", "migrate to", "migrated to",
	"decision recorded", "decision:", "we chose", "agreed to",
	"never use", "always use", "must not", "must never", "we must ",
	"prefer ", "preference", "my style",
	"use redis", "use postgres", "use sqlite",
	"harvested cursor", "auto-sync", "auto sync",
}

// isJunkMemoryContent reports transcript/skill/prompt noise.
func isJunkMemoryContent(text string) bool {
	lowered := strings.ToLower(strings.TrimSpace(text))
	if lowered == "" {
		return true
	}
	for _, m := range junkMemoryMarkers {
		if strings.Contains(lowered, m) {
			return true
		}
	}
	// Skill / instruction bullet lists: "- `something`"
	if strings.HasPrefix(lowered, "- `") || strings.HasPrefix(lowered, "- **") {
		return true
	}
	// Path-only or path-heavy fragments (Windows or POSIX).
	if looksLikePathFragment(text) {
		return true
	}
	// Dense backtick instruction lines without a durable marker.
	if strings.Count(text, "`") >= 4 && !hasDurableMarker(lowered) {
		return true
	}
	return false
}

func looksLikePathFragment(text string) bool {
	t := strings.TrimSpace(text)
	if len(t) < 8 {
		return false
	}
	// Drive path or absolute unix path with little prose.
	if (len(t) >= 3 && t[1] == ':' && (t[2] == '\\' || t[2] == '/')) ||
		strings.HasPrefix(t, "/") || strings.HasPrefix(t, `\\`) {
		spaceCount := strings.Count(t, " ")
		if spaceCount <= 2 && (strings.Contains(t, `\`) || strings.Contains(t, "/")) {
			return true
		}
	}
	if strings.Contains(t, `\.cursor\`) || strings.Contains(t, "/.cursor/") ||
		strings.Contains(t, `skills-cursor`) || strings.Contains(t, "SKILL.md") {
		return true
	}
	return false
}

func hasDurableMarker(lowered string) bool {
	for _, m := range durableMarkers {
		if strings.Contains(lowered, m) {
			return true
		}
	}
	return false
}

// isDurableHeuristic gates heuristic proposals: user turns with durable
// language, or rare assistant lines that clearly record a decision.
func isDurableHeuristic(text, speaker string) bool {
	if isJunkMemoryContent(text) {
		return false
	}
	sp := strings.ToLower(strings.TrimSpace(speaker))
	if sp == "tool" || sp == "system" {
		return false
	}
	lowered := strings.ToLower(text)
	durable := hasDurableMarker(lowered) || IsExplicitStatement(text)
	switch sp {
	case "user", "human":
		// User lines: durable markers only (no bare "use …" — that matches skill blurbs).
		if durable {
			return true
		}
		return isStackChoiceImperative(lowered)
	case "assistant", "ai", "model", "bot":
		// Assistants narrate constantly — only keep clear decision records.
		for _, m := range []string{
			"we decided", "i decided", "we chose", "agreed to",
			"going with", "we'll use", "we will use", "decision recorded", "decision:",
		} {
			if strings.Contains(lowered, m) {
				return true
			}
		}
		return false
	default:
		// Unknown speaker: only strong durable markers.
		return durable
	}
}

// isStackChoiceImperative matches short "use <tool> …" decisions, not skill
// blurbs like "Use when the user asks…" or "Use for best-of-N…".
func isStackChoiceImperative(lowered string) bool {
	if !strings.HasPrefix(lowered, "use ") || len(lowered) > 120 {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(lowered, "use "))
	words := strings.Fields(rest)
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "when", "this", "the", "a", "an", "for", "to", "in", "with", "exactly":
		return false
	}
	if strings.HasPrefix(words[0], "`") || strings.HasPrefix(words[0], "[") {
		return false
	}
	return true
}

// splitSentences splits on sentence terminators and newlines.
func splitSentences(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == '\n'
	})
}

// KeyFromContent derives a machine key ("testing/framework" style) from the
// first few significant words of a sentence.
func KeyFromContent(text string) string {
	var words []string
	for _, t := range tokenize(text) {
		if len(t) < 3 || len(words) >= 4 {
			continue
		}
		words = append(words, t)
	}
	if len(words) == 0 {
		return "misc/note"
	}
	if len(words) == 1 {
		return "misc/" + words[0]
	}
	return words[0] + "/" + strings.Join(words[1:], "-")
}

// ProviderKeyFromEnv reports which (if any) user-configured LLM key is
// present. Keys live ONLY on the local device (env vars / local config) and
// are never shipped to the server — that is the whole point of the designated
// local processor (plan Locked Decisions: "user supplies own API keys; data
// stays local"). A network-backed Provider would consult this to pick its
// backend; the heuristic stub ignores it.
func ProviderKeyFromEnv() (provider string, hasKey bool) {
	if v := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); v != "" {
		return "anthropic", true
	}
	if v := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); v != "" {
		return "openai", true
	}
	if v := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); v != "" {
		return "ollama", true
	}
	return "", false
}

// BuildExtractionPrompt renders the §2.6 classification prompt for a batch.
// Used by a future network-backed Provider; kept here (not in the provider)
// so prompt wording stays versioned with the heuristics it must agree with.
func BuildExtractionPrompt(project string, existing []MemoryRecord, batch []Event) string {
	var sb strings.Builder
	sb.WriteString("Given these events from project \"" + project + "\", extract memories.\n\n")
	sb.WriteString("Classify each memory's level:\n")
	sb.WriteString("- ORGANIZATION: universal policy across all projects (e.g., \"all APIs use JWT\")\n")
	sb.WriteString("- PROJECT: team decision or codebase fact (e.g., \"we use pytest\")\n")
	sb.WriteString("- PERSONAL: individual preference (e.g., \"Alice prefers verbose errors\")\n")
	sb.WriteString("  → Look for \"I prefer\", \"I like\", \"I always\"\n")
	sb.WriteString("- SESSION: temporary, task-specific (e.g., \"don't touch payments/ right now\")\n")
	sb.WriteString("  → Look for \"for now\", \"right now\", \"during this\", \"in this task\"\n\n")
	sb.WriteString("Classify each memory's scope:\n")
	sb.WriteString("- fact: objective truth about the codebase\n")
	sb.WriteString("- preference: subjective choice\n")
	sb.WriteString("- decision: deliberate team choice with reasoning\n")
	sb.WriteString("- constraint: hard rule that must not be violated\n")
	sb.WriteString("- pattern: recurring code/architecture pattern\n")
	sb.WriteString("- episode_summary: condensed bug/incident takeaway\n\n")
	sb.WriteString("Default to SESSION level if unsure (safer — can be promoted later).\n\n")
	sb.WriteString("Existing confirmed memories (do not duplicate):\n")
	if len(existing) == 0 {
		sb.WriteString("(none)\n")
	}
	for _, m := range existing {
		sb.WriteString("- [" + string(m.Level) + "/" + string(m.Scope) + "] " + m.Content + "\n")
	}
	sb.WriteString("\nNew events to process:\n")
	for _, ev := range batch {
		sb.WriteString("- " + string(ev.Type) + ": ")
		if s, ok := ev.Payload["content"].(string); ok {
			sb.WriteString(s)
		} else {
			sb.WriteString("(structured tool event)")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Processor: background batching on the designated device
// ---------------------------------------------------------------------------

const (
	// DefaultBatchSize caps proposals extracted per batch.
	DefaultBatchSize = 20
	// DefaultBudget caps proposal content chars per batch (~1000 tokens, the
	// per-agent budget scale from plan §1.6).
	DefaultBudget = 4000
	// IdleFlushAfter matches the harvester: 5min of silence (or a
	// SESSION_TRANSCRIPT_COMPLETE) triggers the deep-analysis pass.
	IdleFlushAfter = 5 * time.Minute
)

// Designation timing (issue #115).
//
//   - PresenceThreshold mirrors store.OfflineThreshold (90s): a workspace
//     silent past 90s reads offline for presence/liveness. Defined here so
//     the daemon never imports the pgx-backed store.
//   - DesignatedFailoverAfter is the distinct 1h failover delay (plan §6.3):
//     the designated-processor role is sticky for 1h past last heartbeat so
//     laptops flapping on sleep do not churn designation 40x earlier than
//     spec. Presence may flap at 90s; the role does not.
const (
	// PresenceThreshold mirrors store.OfflineThreshold for presence checks.
	PresenceThreshold = 90 * time.Second
	// DesignatedFailoverAfter is how long a designated processor may be
	// silent before another daemon may take over (plan §6.3, 1h).
	DesignatedFailoverAfter = time.Hour
)

// Processor consumes event batches on the designated daemon and proposes
// structured memories. Construct with NewProcessor; run ProcessEvents from a
// background goroutine fed by the harvester/interceptor channels.
type Processor struct {
	Store      MemoryStore
	Budget     int // max content chars accepted per batch
	BatchSize  int // max proposals emitted per batch
	Designated bool
	Provider   Provider

	// lastExtractProvider is set after a successful server extract flush
	// ("openrouter" | "heuristic") for Connect telemetry.
	lastExtractProvider string
}

// NewProcessor builds a Processor. budget <= 0 selects DefaultBudget,
// batchSize <= 0 selects DefaultBatchSize, provider == nil selects the
// offline HeuristicProvider. designated must mirror
// workspaces.is_designated_processor: only the project owner's daemon sets it.
func NewProcessor(store MemoryStore, budget, batchSize int, designated bool, provider Provider) *Processor {
	if budget <= 0 {
		budget = DefaultBudget
	}
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	if provider == nil {
		provider = HeuristicProvider{}
	}
	return &Processor{
		Store:      store,
		Budget:     budget,
		BatchSize:  batchSize,
		Designated: designated,
		Provider:   provider,
	}
}

// ShouldRun reports whether this daemon may process: designated device only.
// Non-designated daemons consume memories; they never propose (plan Locked
// Decisions: designated processor eliminates multi-device divergence).
func (p *Processor) ShouldRun() bool { return p.Designated }

// LastExtractProvider returns the last server extract provider label, if any.
func (p *Processor) LastExtractProvider() string {
	if p == nil {
		return ""
	}
	return p.lastExtractProvider
}

// ShouldFlush reports whether a batch is due: true on
// SESSION_TRANSCRIPT_COMPLETE, or when now-lastActive >= IdleFlushAfter.
func ShouldFlush(ev Event, lastActive, now time.Time) bool {
	if ev.Type == EventSessionComplete {
		return true
	}
	if lastActive.IsZero() {
		return false
	}
	return now.Sub(lastActive) >= IdleFlushAfter
}

// ProcessEvents extracts proposals from conversation events (Layer 2/3/4),
// dedups against the store, enforces budget/batch caps, stamps confirmation
// delays, and saves survivors. Non-designated daemons return nil without
// doing anything. When Store is an HTTPMemoryStore (portal-connected), turns
// are uploaded to POST /memory/extract so the server owns LLM/heuristic
// quality — no local propose+POST /memory junk path.
func (p *Processor) ProcessEvents(ctx context.Context, project string, events []Event) ([]Proposal, error) {
	if !p.ShouldRun() {
		return nil, nil
	}
	if len(events) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if hs, ok := p.Store.(*HTTPMemoryStore); ok {
		turns := eventsToExtractTurns(events)
		if len(turns) == 0 {
			return nil, nil
		}
		provider, proposals, err := hs.ExtractRemote(ctx, turns)
		if err != nil {
			return nil, err
		}
		p.lastExtractProvider = provider
		return proposals, nil
	}
	proposals, err := p.Provider.Extract(ctx, project, events, p.Store.Existing())
	if err != nil {
		return nil, err
	}
	proposals = Deduplicate(proposals, p.Store.Existing())
	proposals = applyCaps(proposals, p.Budget, p.BatchSize)
	now := time.Now().UTC()
	var firstErr error
	saved := 0
	for i := range proposals {
		proposals[i].ConfirmAfter = ConfirmationDelay(proposals[i])
		proposals[i].ProposedAt = now
		// Secret-screened (issue #132): proposal content derives from
		// conversation/LLM text that routinely contains pasted secrets.
		proposals[i].Content = redact(proposals[i].Content)
		if p.Store != nil {
			if err := p.Store.Save(proposals[i]); err != nil {
				// Keep uploading the rest of the batch — one bad row
				// (e.g. transient 5xx) must not drop sibling proposals.
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			saved++
		}
	}
	if firstErr != nil && saved == 0 {
		return nil, firstErr
	}
	if firstErr != nil {
		return proposals, firstErr
	}
	return proposals, nil
}

// eventsToExtractTurns flattens conversation events into speaker/content maps
// for POST /memory/extract.
func eventsToExtractTurns(events []Event) []map[string]string {
	var out []map[string]string
	for _, ev := range events {
		for _, cand := range eventCandidates(ev) {
			c := strings.TrimSpace(cand.Text)
			if c == "" {
				continue
			}
			if len(c) > 4000 {
				c = c[:4000]
			}
			out = append(out, map[string]string{
				"speaker": cand.Speaker,
				"content": c,
			})
			if len(out) >= 40 {
				return out
			}
		}
	}
	return out
}

// ProcessToolEvents runs episode auto-detection (plan §2.3, issue #115) over
// a Layer-1 ToolEvent window and emits an episode_summary proposal when the
// fail->read->fix->green-rerun arc completes. Non-designated daemons return
// nil (fail-closed). It is the ToolEvent counterpart to ProcessEvents:
// call it from the interceptor event loop; ProcessEvents itself stays
// conversation-only for backward compatibility.
func (p *Processor) ProcessToolEvents(ctx context.Context, project string, evs []ToolEvent) ([]Proposal, error) {
	if !p.ShouldRun() {
		return nil, nil
	}
	if len(evs) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ep := DetectEpisodePattern(evs)
	if ep == nil {
		return nil, nil
	}
	content := strings.TrimSpace(ep.Title + "\nTrigger: " + ep.Trigger + "\n" + ep.Resolution + "\n" + ep.Verification)
	if len(content) < 20 {
		return nil, nil
	}
	if len(content) > 2000 {
		content = content[:2000]
	}
	pr := Proposal{
		Key:        KeyFromContent(content),
		Content:    redact(content),
		Level:      LevelProject,
		Scope:      ScopeEpisodeSummary,
		Confidence: 0.8,
		Source:     "processor:episode",
		Explicit:   false,
	}
	pr.ConfirmAfter = ConfirmationDelay(pr)
	pr.ProposedAt = time.Now().UTC()
	if dup, _ := IsDuplicate(pr.Content, p.Store.Existing()); dup {
		return nil, nil
	}
	if p.Store != nil {
		if err := p.Store.Save(pr); err != nil {
			return nil, err
		}
	}
	return []Proposal{pr}, nil
}

// applyCaps enforces the per-batch proposal count and content-char budget
// (keeps first-N; mirrors the Context Builder's bounded-budget philosophy).
func applyCaps(proposals []Proposal, budget, batchSize int) []Proposal {
	if batchSize > 0 && len(proposals) > batchSize {
		proposals = proposals[:batchSize]
	}
	if budget <= 0 {
		return proposals
	}
	out := proposals[:0]
	used := 0
	for _, pr := range proposals {
		if used+len(pr.Content) > budget {
			break
		}
		used += len(pr.Content)
		out = append(out, pr)
	}
	return out
}
