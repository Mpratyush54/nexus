// Package context assembles inference-ready XML memory blocks for agents
// (issue #6, plan §§1.6–1.7).
//
// Pure, DB-free core:
//   - Decay / DaysSince / EffectiveConfidence: base*0.95^(days/30) time decay
//   - ResolveOverride: SESSION > PERSONAL > PROJECT > ORGANIZATION wins on
//     key collision (EPHEMERAL outranks SESSION — most transient scope wins;
//     ephemeral is never persisted per the plan, the builder just tolerates it)
//   - BuildXML: <project_memory> output under a char budget, priority fill
//     active-task → session → episodes → personal → project → organization,
//     truncation at item granularity so output is always valid XML
//
// DECOUPLING NOTE: this package does NOT import internal/store. The MCP and
// server layers map store.MemoryItem onto Item at the boundary (their call).
// This keeps the builder dependency-free and unit-testable. See ADR-006.
package context

import (
	"encoding/xml"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultBudgetChars is the default output cap (~1000 tokens, plan §1.6).
const DefaultBudgetChars = 4000

// Memory levels. The four persisted levels match the memory_items CHECK
// constraint (plan §1.1); ephemeral is turn-scoped scratch that is never
// stored but still resolves here for in-memory merges.
const (
	LevelOrganization = "organization"
	LevelProject      = "project"
	LevelPersonal     = "personal"
	LevelSession      = "session"
	LevelEphemeral    = "ephemeral"
)

// Decay constants shared with the store recency curve (plan §1.7).
const (
	DecayBase       = 0.95
	DecayWindowDays = 30.0
)

// LevelRank orders levels for override resolution: higher wins.
// Unknown levels rank -1 so they lose to every known level.
func LevelRank(level string) int {
	switch level {
	case LevelEphemeral:
		return 4
	case LevelSession:
		return 3
	case LevelPersonal:
		return 2
	case LevelProject:
		return 1
	case LevelOrganization:
		return 0
	default:
		return -1
	}
}

// Item is one memory fact as rendered into XML. Confidence is the stored
// base confidence; BuildXML displays the decayed effective value.
type Item struct {
	Key        string
	Content    string
	Level      string
	Scope      string
	Confidence float64
	DecidedBy  string
	Date       string
	LastUsedAt time.Time
}

// Episode is a condensed bug/incident/feature arc (plan §1.6
// <recent_episodes>, full schema owned by issue #11).
type Episode struct {
	Type    string
	Title   string
	Status  string
	Date    string
	Summary string
}

// ActiveTask is the agent's current work item — always included first.
type ActiveTask struct {
	Title       string
	Status      string
	Assigned    string
	Description string
}

// ContextInput groups everything BuildXML may render. Project memories
// should arrive pre-sorted (best first); order is preserved. Now anchors
// decay math — leave zero for time.Now (tests pin it).
type ContextInput struct {
	ProjectName  string
	Branch       string
	Updated      string // default: today (YYYY-MM-DD)
	PersonalUser string
	SessionTitle string

	Organization []Item
	Project      []Item
	Personal     []Item
	Session      []Item
	Episodes     []Episode
	Task         *ActiveTask
	Now          time.Time
}

// BuildStats reports what BuildXML did with the budget.
type BuildStats struct {
	BudgetChars       int
	CharsUsed         int
	BudgetRemaining   int
	ItemsIncluded     int      // memory items + episodes placed (+1 when Task present)
	ItemsDropped      int      // bodies that did not fit the budget
	TruncatedSections []string // sections partially or fully dropped for budget
}

// Decay is plan §1.7: effective = base * 0.95^(days/30).
// Anchor points: 0d ≈ base, 90d ≈ 86% of base, 180d ≈ 74% of base.
// Negative days clamp to 0; output clamps to [0, 1].
func Decay(baseConfidence, daysSince float64) float64 {
	if daysSince < 0 {
		daysSince = 0
	}
	v := baseConfidence * math.Pow(DecayBase, daysSince/DecayWindowDays)
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// DaysSince returns whole-fraction days from t to now; zero/negative clamp
// to 0 (zero t = unknown age → no decay, never a boost).
func DaysSince(t, now time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	d := now.Sub(t).Hours() / 24
	if d < 0 {
		return 0
	}
	return d
}

// EffectiveConfidence applies Decay to a stored base confidence given the
// last-use timestamp. Zero lastUsed = unknown age → base unchanged.
func EffectiveConfidence(baseConfidence float64, lastUsed, now time.Time) float64 {
	if lastUsed.IsZero() {
		if baseConfidence < 0 {
			return 0
		}
		if baseConfidence > 1 {
			return 1
		}
		return baseConfidence
	}
	return Decay(baseConfidence, DaysSince(lastUsed, now))
}

// ResolveOverride dedupes by Key: the highest-ranked level wins; ties break
// toward higher base confidence, then first-seen (stable). Items with an
// empty key carry no identity and pass through untouched. Output is sorted
// by Key for determinism — callers that need ranked order should use the
// result as a winner lookup (as BuildXML does), not as an ordering.
func ResolveOverride(items []Item) []Item {
	best := make(map[string]Item, len(items))
	var keyless []Item
	for _, it := range items {
		if it.Key == "" {
			keyless = append(keyless, it)
			continue
		}
		cur, ok := best[it.Key]
		if !ok || overrideWins(it, cur) {
			best[it.Key] = it
		}
	}
	out := make([]Item, 0, len(best)+len(keyless))
	for _, it := range best {
		out = append(out, it)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Key < out[b].Key })
	return append(out, keyless...)
}

func overrideWins(a, b Item) bool {
	ra, rb := LevelRank(a.Level), LevelRank(b.Level)
	if ra != rb {
		return ra > rb
	}
	return a.Confidence > b.Confidence
}

func itemEqual(a, b Item) bool {
	return a.Key == b.Key &&
		a.Content == b.Content &&
		a.Level == b.Level &&
		a.Scope == b.Scope &&
		a.Confidence == b.Confidence &&
		a.DecidedBy == b.DecidedBy &&
		a.Date == b.Date &&
		a.LastUsedAt.Equal(b.LastUsedAt)
}

// esc XML-escapes text and attribute values (encoding/xml escapes
// <, >, &, ', " — safe for both positions).
func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func attr(name, val string) string {
	if val == "" {
		return ""
	}
	return " " + name + `="` + esc(val) + `"`
}

func itemXML(it Item, now time.Time) string {
	var b strings.Builder
	b.WriteString("  <item")
	b.WriteString(attr("key", it.Key))
	b.WriteString(` confidence="` +
		strconv.FormatFloat(EffectiveConfidence(it.Confidence, it.LastUsedAt, now), 'f', 2, 64) + `"`)
	b.WriteString(attr("scope", it.Scope))
	b.WriteString(attr("decided_by", it.DecidedBy))
	b.WriteString(attr("date", it.Date))
	b.WriteString(">")
	b.WriteString(esc(it.Content))
	b.WriteString("</item>\n")
	return b.String()
}

func episodeXML(ep Episode) string {
	var b strings.Builder
	b.WriteString("  <episode")
	b.WriteString(attr("type", ep.Type))
	b.WriteString(attr("title", ep.Title))
	b.WriteString(attr("status", ep.Status))
	b.WriteString(attr("date", ep.Date))
	b.WriteString(">")
	b.WriteString(esc(ep.Summary))
	b.WriteString("</episode>\n")
	return b.String()
}

func taskXML(t ActiveTask) string {
	var b strings.Builder
	b.WriteString("  <active_task")
	b.WriteString(attr("title", t.Title))
	b.WriteString(attr("status", t.Status))
	b.WriteString(attr("assigned", t.Assigned))
	b.WriteString(">")
	b.WriteString(esc(t.Description))
	b.WriteString("</active_task>\n")
	return b.String()
}

// BuildXML assembles the plan §1.6 <project_memory> document:
//
//  1. Cross-level override: every item is re-tagged with its section level
//     (sections are authoritative), ResolveOverride picks the winner per
//     key, and each section keeps only its winners in original order — so a
//     session item always shadows the same project key.
//  2. Budget fill in priority order active-task → session → episodes →
//     personal → project → organization, at whole-item granularity: an item
//     that does not fit is skipped (counted dropped) and smaller later
//     items may still fit. The active task is always included even if it
//     alone exceeds the budget.
//  3. Emission in document order (organization … session, recent_episodes,
//     active_task) regardless of fill order.
//
// budgetChars <= 0 selects DefaultBudgetChars. Output is always well-formed
// XML; len(out) <= budgetChars holds except when the fixed header/footer
// (or the always-included task) exceed the budget — reported via Stats.
func BuildXML(in ContextInput, budgetChars int) (string, BuildStats) {
	if budgetChars <= 0 {
		budgetChars = DefaultBudgetChars
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	updated := in.Updated
	if updated == "" {
		updated = now.Format("2006-01-02")
	}
	stats := BuildStats{BudgetChars: budgetChars}

	// 1. Cross-level override with section-authoritative levels.
	type tagged struct {
		level string
		items []Item
	}
	sections := []tagged{
		{LevelOrganization, in.Organization},
		{LevelProject, in.Project},
		{LevelPersonal, in.Personal},
		{LevelSession, in.Session},
	}
	pooled := make([]Item, 0)
	for _, s := range sections {
		for _, it := range s.items {
			if it.Key == "" {
				continue // keyless items skip override; handled per-section below
			}
			it.Level = s.level
			pooled = append(pooled, it)
		}
	}
	resolved := ResolveOverride(pooled)
	byKey := make(map[string]Item, len(resolved))
	for _, it := range resolved {
		byKey[it.Key] = it
	}
	// pick returns the section's surviving winners in caller order.
	pick := func(level string, items []Item) []Item {
		var out []Item
		seen := make(map[string]bool, len(items))
		for _, it := range items {
			if it.Key == "" {
				it.Level = level
				out = append(out, it)
				continue
			}
			if seen[it.Key] {
				continue
			}
			seen[it.Key] = true
			w, ok := byKey[it.Key]
			if !ok {
				continue
			}
			if w.Level == level {
				out = append(out, w)
			} else if LevelRank(w.Level) < 0 {
				// Unreachable with section tagging, kept for
				// forward-compat if levels ever pass through raw.
				c := it
				c.Level = level
				out = append(out, c)
			}
			// else: shadowed by a higher level — dropped by override.
		}
		return out
	}

	org := pick(LevelOrganization, in.Organization)
	proj := pick(LevelProject, in.Project)
	pers := pick(LevelPersonal, in.Personal)
	sess := pick(LevelSession, in.Session)

	// 2. Budget fill in priority order (task first — always included).
	header := "<project_memory" + attr("project", in.ProjectName) +
		attr("branch", in.Branch) + attr("updated", updated) + ">\n"
	footer := "</project_memory>\n"
	remaining := budgetChars - len(header) - len(footer)

	taskBody := ""
	if in.Task != nil {
		taskBody = taskXML(*in.Task)
		remaining -= len(taskBody)
		stats.ItemsIncluded++
	}

	bodies := func(items []Item) []string {
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, itemXML(it, now))
		}
		return out
	}
	epBodies := make([]string, 0, len(in.Episodes))
	for _, ep := range in.Episodes {
		epBodies = append(epBodies, episodeXML(ep))
	}

	fillSection := func(name, open, close string, parts []string) string {
		if len(parts) == 0 {
			return "" // empty sections are omitted, never truncated
		}
		overhead := len(open) + len(close)
		if remaining < overhead {
			stats.TruncatedSections = append(stats.TruncatedSections, name)
			stats.ItemsDropped += len(parts)
			return ""
		}
		var b strings.Builder
		b.WriteString(open)
		room := remaining - overhead
		incl := 0
		for _, p := range parts {
			if len(p) <= room {
				b.WriteString(p)
				room -= len(p)
				incl++
			}
		}
		if incl == 0 {
			stats.TruncatedSections = append(stats.TruncatedSections, name)
			stats.ItemsDropped += len(parts)
			return ""
		}
		b.WriteString(close)
		remaining = room
		stats.ItemsIncluded += incl
		if dropped := len(parts) - incl; dropped > 0 {
			stats.ItemsDropped += dropped
			stats.TruncatedSections = append(stats.TruncatedSections, name)
		}
		return b.String()
	}

	// Fill priority: session → episodes → personal → project → org.
	sessBody := fillSection("session", "<session"+attr("title", in.SessionTitle)+">\n", "</session>\n", bodies(sess))
	epBody := fillSection("recent_episodes", "<recent_episodes>\n", "</recent_episodes>\n", epBodies)
	persBody := fillSection("personal", "<personal"+attr("user", in.PersonalUser)+">\n", "</personal>\n", bodies(pers))
	projBody := fillSection("project", "<project>\n", "</project>\n", bodies(proj))
	orgBody := fillSection("organization", "<organization>\n", "</organization>\n", bodies(org))

	// 3. Emit in document order.
	var out strings.Builder
	out.WriteString(header)
	out.WriteString(orgBody)
	out.WriteString(projBody)
	out.WriteString(persBody)
	out.WriteString(sessBody)
	out.WriteString(epBody)
	out.WriteString(taskBody)
	out.WriteString(footer)
	s := out.String()
	stats.CharsUsed = len(s)
	stats.BudgetRemaining = budgetChars - len(s)
	return s, stats
}
