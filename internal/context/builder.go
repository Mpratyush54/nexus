// Context Builder: level-override resolution and inference-ready XML
// assembly (implementation-plan.md Phase 1.6).
//
// Resolution order (lower overrides higher on key collision):
//
//	EPHEMERAL > SESSION > PERSONAL > PROJECT > ORGANIZATION
//
// Sections are filled in priority order under a character budget
// (default 4000 chars, ~1000 tokens):
//
//  1. Active task (always included)
//  2. Ephemeral memories (working memory, never outlives its session)
//  3. Session memories
//  4. Relevant episodes
//  5. Personal memories
//  6. Project memories
//  7. Organization memories
//
// Lower-priority items that do not fit are dropped; the XML stays
// well-formed. Retrieval counts are returned alongside the XML so the
// MCP memory_search response can report token_count, budget_remaining
// and items_included (see Phase 1.4).
package context

import (
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"
	"time"

	"central-memory/internal/store"
)

// DefaultBudget is the default context size cap in characters (~1000 tokens).
const DefaultBudget = 4000

// AgentBudget resolves the builder budget for a named agent (issue #41):
// the agent registry's seed budget when agentName is known (claude 10k,
// copilot 8k, cursor/windsurf 6k), otherwise fallback (<= 0 selects
// DefaultBudget). Pass the result as ContextInput.Budget so per-agent caps
// flow into AssembleXML instead of the static default.
func AgentBudget(agentName string, fallback int) int {
	if strings.TrimSpace(agentName) != "" {
		if a, err := store.NewAgentRegistry().GetAgent(agentName); err == nil && a.ContextBudget > 0 {
			return a.ContextBudget
		}
	}
	if fallback <= 0 {
		return DefaultBudget
	}
	return fallback
}

// LevelRank maps a memory level to its override precedence. Higher wins.
// Unknown levels rank below organization so they never shadow real data.
func LevelRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "ephemeral":
		return 4
	case "session":
		return 3
	case "personal":
		return 2
	case "project":
		return 1
	case "organization", "org":
		return 0
	default:
		return -1
	}
}

// ResolveOverrides collapses key collisions so the lowest (most specific)
// level wins: EPHEMERAL > SESSION > PERSONAL > PROJECT > ORGANIZATION. Level ties
// break toward higher base confidence; full ties keep the first item seen.
// Output order follows first-seen winning keys for determinism.
func ResolveOverrides(items []*store.MemoryItem) []*store.MemoryItem {
	best := make(map[string]*store.MemoryItem, len(items))
	order := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		cur, ok := best[item.Key]
		if !ok {
			best[item.Key] = item
			order = append(order, item.Key)
			continue
		}
		rNew, rCur := LevelRank(item.Level), LevelRank(cur.Level)
		if rNew > rCur || (rNew == rCur && item.Confidence > cur.Confidence) {
			best[item.Key] = item
		}
	}
	out := make([]*store.MemoryItem, 0, len(order))
	for _, k := range order {
		out = append(out, best[k])
	}
	return out
}

// ContextInput gathers everything the builder renders. Level slices
// should already be relevance-ordered (e.g. HybridSearch output);
// same-key collisions across slices resolve with lower levels winning.
type ContextInput struct {
	ProjectName    string
	Branch         string
	Updated        time.Time
	Task           *store.Task
	SessionTitle   string
	PersonalUser   string
	EphemeralItems []*store.MemoryItem
	SessionItems   []*store.MemoryItem
	Episodes       []*store.Episode
	PersonalItems  []*store.MemoryItem
	ProjectItems   []*store.MemoryItem
	OrgItems       []*store.MemoryItem
	// Budget caps the XML in characters; <= 0 selects DefaultBudget.
	Budget int
	// Now anchors confidence decay and date rendering; zero means UTC now.
	Now time.Time
}

// AssembledContext is the builder result: well-formed XML plus the
// accounting the MCP layer returns next to it.
type AssembledContext struct {
	XML             string
	TokenCount      int
	BudgetRemaining int
	ItemsIncluded   int
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func effConf(item *store.MemoryItem, now time.Time) string {
	return strconv.FormatFloat(EffectiveConfidenceForItem(item, now), 'f', 2, 64)
}

func itemDate(item *store.MemoryItem) string {
	t := item.UpdatedAt
	if t.IsZero() {
		t = item.CreatedAt
	}
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

// renderItem renders one memory <item>. The served text is the content
// plus its provenance snippet, so the LLM sees both fact and source.
func renderItem(item *store.MemoryItem, now time.Time) string {
	var b strings.Builder
	b.WriteString(`<item key="`)
	b.WriteString(esc(item.Key))
	b.WriteString(`" confidence="`)
	b.WriteString(effConf(item, now))
	b.WriteString(`" scope="`)
	b.WriteString(esc(item.Scope))
	b.WriteString(`"`)
	if item.ConfirmedBy != "" {
		b.WriteString(` decided_by="`)
		b.WriteString(esc(item.ConfirmedBy))
		b.WriteString(`"`)
	} else if item.ProposedBy != "" {
		b.WriteString(` decided_by="`)
		b.WriteString(esc(item.ProposedBy))
		b.WriteString(`"`)
	}
	if d := itemDate(item); d != "" {
		b.WriteString(` date="`)
		b.WriteString(d)
		b.WriteString(`"`)
	}
	b.WriteString(`>`)
	text := strings.TrimSpace(item.Content)
	if item.ContextSnippet != "" {
		text += " (" + strings.TrimSpace(item.ContextSnippet) + ")"
	}
	b.WriteString(esc(text))
	b.WriteString(`</item>`)
	return b.String()
}

func renderEpisode(ep *store.Episode) string {
	var b strings.Builder
	b.WriteString(`<episode type="`)
	b.WriteString(esc(ep.EpisodeType))
	b.WriteString(`" title="`)
	b.WriteString(esc(ep.Title))
	b.WriteString(`" status="`)
	b.WriteString(esc(ep.Status))
	b.WriteString(`"`)
	if !ep.ResolvedAt.IsZero() {
		b.WriteString(` date="`)
		b.WriteString(ep.ResolvedAt.Format("2006-01-02"))
		b.WriteString(`"`)
	} else if !ep.OpenedAt.IsZero() {
		b.WriteString(` date="`)
		b.WriteString(ep.OpenedAt.Format("2006-01-02"))
		b.WriteString(`"`)
	}
	b.WriteString(`>`)
	parts := []string{}
	if ep.Trigger != "" {
		parts = append(parts, "Trigger: "+ep.Trigger)
	}
	if ep.RootCause != "" {
		parts = append(parts, "Root cause: "+ep.RootCause)
	}
	if ep.Resolution != "" {
		parts = append(parts, "Fix: "+ep.Resolution)
	}
	if ep.Verification != "" {
		parts = append(parts, "Verification: "+ep.Verification)
	}
	b.WriteString(esc(strings.Join(parts, " ")))
	b.WriteString(`</episode>`)
	return b.String()
}

func renderTask(t *store.Task) string {
	var b strings.Builder
	b.WriteString(`<active_task title="`)
	b.WriteString(esc(t.Title))
	b.WriteString(`" status="`)
	b.WriteString(esc(t.Status))
	b.WriteString(`"`)
	if t.AssignedTo != "" {
		b.WriteString(` assigned="`)
		b.WriteString(esc(t.AssignedTo))
		b.WriteString(`"`)
	}
	b.WriteString(`>`)
	b.WriteString(esc(strings.TrimSpace(t.Description)))
	b.WriteString(`</active_task>`)
	return b.String()
}

// AssembleXML renders the layered XML context block within budget.
// The active task is always included; every other section contributes
// items greedily in priority order (session, episodes, personal,
// project, organization), skipping items that would overflow the budget.
// Empty sections are omitted. Global same-key collisions resolve with
// lower levels winning before rendering.
func AssembleXML(in ContextInput) AssembledContext {
	budget := in.Budget
	if budget <= 0 {
		budget = DefaultBudget
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	// Global override resolution: an item shadowed by a lower level must
	// not reappear in its own section.
	shadowed := make(map[string]string) // key -> winning level
	for _, item := range append(append(append(append(append(
		[]*store.MemoryItem{},
		in.EphemeralItems...),
		in.SessionItems...), in.PersonalItems...), in.ProjectItems...), in.OrgItems...) {
		if item == nil {
			continue
		}
		if w, ok := shadowed[item.Key]; !ok || LevelRank(item.Level) > LevelRank(w) {
			shadowed[item.Key] = item.Level
		}
	}
	visible := func(items []*store.MemoryItem, level string) []*store.MemoryItem {
		var out []*store.MemoryItem
		for _, item := range items {
			if item == nil {
				continue
			}
			if w, ok := shadowed[item.Key]; ok && LevelRank(w) > LevelRank(level) {
				continue
			}
			out = append(out, item)
		}
		return out
	}
	session := visible(in.SessionItems, "session")
	ephemeral := visible(in.EphemeralItems, "ephemeral")
	personal := visible(in.PersonalItems, "personal")
	project := visible(in.ProjectItems, "project")
	org := visible(in.OrgItems, "organization")

	updated := in.Updated
	if updated.IsZero() {
		updated = now
	}
	var head strings.Builder
	head.WriteString(`<project_memory project="`)
	head.WriteString(esc(in.ProjectName))
	head.WriteString(`" branch="`)
	head.WriteString(esc(in.Branch))
	head.WriteString(`" updated="`)
	head.WriteString(updated.Format("2006-01-02"))
	head.WriteString(`">`)
	header, footer := head.String(), `</project_memory>`

	// Sections in fill priority order.
	type section struct {
		open  string
		close string
		units []string
	}
	sections := []section{}
	if in.Task != nil {
		taskXML := renderTask(in.Task)
		sections = append(sections, section{open: "", close: "", units: []string{taskXML}})
	}
	mkItems := func(items []*store.MemoryItem) []string {
		units := make([]string, 0, len(items))
		for _, item := range items {
			units = append(units, renderItem(item, now))
		}
		return units
	}
	mkEpisodes := func(eps []*store.Episode) []string {
		units := make([]string, 0, len(eps))
		for _, ep := range eps {
			if ep == nil {
				continue
			}
			units = append(units, renderEpisode(ep))
		}
		return units
	}
	if len(ephemeral) > 0 {
		sections = append(sections, section{
			open: `<ephemeral>`, close: `</ephemeral>`, units: mkItems(ephemeral),
		})
	}
	if len(session) > 0 {
		title := in.SessionTitle
		if title == "" {
			title = "session"
		}
		sections = append(sections, section{
			open:  `<session title="` + esc(title) + `">`,
			close: `</session>`,
			units: mkItems(session),
		})
	}
	if len(in.Episodes) > 0 {
		sections = append(sections, section{
			open:  `<recent_episodes>`,
			close: `</recent_episodes>`,
			units: mkEpisodes(in.Episodes),
		})
	}
	if len(personal) > 0 {
		user := in.PersonalUser
		if user == "" {
			user = "user"
		}
		sections = append(sections, section{
			open:  `<personal user="` + esc(user) + `">`,
			close: `</personal>`,
			units: mkItems(personal),
		})
	}
	if len(project) > 0 {
		sections = append(sections, section{
			open: `<project>`, close: `</project>`, units: mkItems(project),
		})
	}
	if len(org) > 0 {
		sections = append(sections, section{
			open: `<organization>`, close: `</organization>`, units: mkItems(org),
		})
	}

	// Greedy fill: the task section (index 0, when present) is always
	// included; all other units are added only if they fit.
	var body strings.Builder
	count := 0
	for i, s := range sections {
		always := i == 0 && in.Task != nil
		var sb strings.Builder
		kept := 0
		for _, u := range s.units {
			// Project total if we added this unit (plus section
			// wrapper on first kept unit, plus root footer).
			add := len(u)
			if kept == 0 {
				add += len(s.open) + len(s.close)
			} else {
				// The section wrapper closes once after the loop;
				// re-account for it on every subsequent unit.
				add += len(s.close)
			}
			if !always && body.Len()+len(header)+sb.Len()+add+len(footer) > budget {
				continue
			}
			if kept == 0 {
				sb.WriteString(s.open)
			}
			sb.WriteString(u)
			kept++
		}
		if kept > 0 {
			sb.WriteString(s.close)
			body.WriteString(sb.String())
			count += kept
		} else if always {
			// Task must render even under an impossibly small
			// budget; emit it bare so output stays parseable.
			for _, u := range s.units {
				body.WriteString(u)
				count++
			}
		}
	}

	xmlOut := header + body.String() + footer
	remaining := budget - len(xmlOut)
	if remaining < 0 {
		remaining = 0
	}
	return AssembledContext{
		XML:             xmlOut,
		TokenCount:      len(xmlOut),
		BudgetRemaining: remaining,
		ItemsIncluded:   count,
	}
}
