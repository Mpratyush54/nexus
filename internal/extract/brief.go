package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxBriefChars caps the project brief (runes). Agents receive it on every
// MCP initialize, so it must stay short.
const MaxBriefChars = 1500

// maxBriefDecisions caps decision/convention lines in the brief.
const maxBriefDecisions = 10

// maxBriefRecent caps the "Recent work" lines.
const maxBriefRecent = 2

// maxBriefStack caps the technologies on the Stack line.
const maxBriefStack = 10

// briefRetryDelay is the pause before the one retry of a transient LLM
// failure (tests set it to 0).
var briefRetryDelay = 1500 * time.Millisecond

// Fallback reasons recorded when the brief is deterministic. They are short
// codes, safe to store and show; the error text only goes to the log.
const (
	BriefFallbackNoKey       = "no_api_key"
	BriefFallbackTimeout     = "timeout"
	BriefFallbackRateLimited = "rate_limited"
	BriefFallbackUpstream    = "upstream_error"
	BriefFallbackNetwork     = "network_error"
	BriefFallbackEmpty       = "empty_reply"
	BriefFallbackUnparseable = "unparseable_reply"
)

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
	Confirmed  bool
	// SessionID is the chat the memory came from ("" when unknown).
	SessionID     string
	FilesAffected []string
}

// BriefInput is everything the brief is built from.
type BriefInput struct {
	Project   string
	Memories  []BriefMemory // top memories by confidence and use
	Summaries []BriefMemory // recent session summaries, newest first
	// Corpus is every eligible row, used only to detect the stack and the
	// recurring topics. Nil means Memories plus Summaries.
	Corpus []BriefMemory
}

// BriefResult is a generated brief.
type BriefResult struct {
	Content  string
	Provider string // openrouter | heuristic
	LLMError string // detail for the server log only
	// FallbackReason is a short code (BriefFallback*) when the brief is
	// deterministic although an LLM brief was wanted; "" otherwise.
	FallbackReason string
	// Sources are the session IDs the brief cites, in first-cited order.
	Sources []string
}

var errBriefUnparseable = errors.New("extract: brief reply is not a usable brief")

// GenerateBrief builds the brief with OpenRouter when a key is configured,
// falling back to the deterministic DeterministicBrief on any LLM failure.
// A transient failure (timeout, 429, 5xx, empty reply) is retried once.
func (s *Service) GenerateBrief(ctx context.Context, in BriefInput) BriefResult {
	if len(in.Memories) == 0 && len(in.Summaries) == 0 {
		return BriefResult{Provider: ProviderHeuristic}
	}
	if !s.HasAPIKey() {
		out := DeterministicBrief(in)
		out.FallbackReason = BriefFallbackNoKey
		return out
	}
	prompt := BuildBriefPrompt(in)
	content, err := s.client().CompleteBrief(ctx, prompt)
	if err != nil && briefTransient(ctx, err) {
		if waitBriefRetry(ctx) {
			content, err = s.client().CompleteBrief(ctx, prompt)
		}
	}
	if err == nil {
		content = ClampBrief(content)
		return BriefResult{Content: content, Provider: ProviderOpenRouter, Sources: BriefSources(content)}
	}
	out := DeterministicBrief(in)
	out.LLMError = err.Error()
	out.FallbackReason = briefFallbackReason(err)
	return out
}

func waitBriefRetry(ctx context.Context) bool {
	if briefRetryDelay <= 0 {
		return ctx.Err() == nil
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < briefRetryDelay+10*time.Second {
		return false // not enough time left for a second call
	}
	t := time.NewTimer(briefRetryDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// briefTransient reports whether err is worth one retry.
func briefTransient(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.Status == 408 || se.Status == 429 || se.Status >= 500
	}
	switch briefFallbackReason(err) {
	case BriefFallbackTimeout, BriefFallbackNetwork, BriefFallbackEmpty:
		return true
	}
	return false
}

// briefFallbackReason maps an LLM error to a short, storable code.
func briefFallbackReason(err error) string {
	var se *StatusError
	var ne net.Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &se):
		if se.Status == 429 {
			return BriefFallbackRateLimited
		}
		if se.Status == 408 {
			return BriefFallbackTimeout
		}
		return BriefFallbackUpstream
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return BriefFallbackTimeout
	case errors.Is(err, errBriefUnparseable):
		return BriefFallbackUnparseable
	case errors.As(err, &ne), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return BriefFallbackNetwork
	}
	msg := err.Error()
	if strings.Contains(msg, "empty choices") || strings.Contains(msg, "empty message content") {
		return BriefFallbackEmpty
	}
	if strings.Contains(msg, "decode response") {
		return BriefFallbackUpstream
	}
	return BriefFallbackNetwork
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
		pi, pj := out[i].Pinned && out[i].Confirmed, out[j].Pinned && out[j].Confirmed
		if pi != pj {
			return pi
		}
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		if out[i].Confirmed != out[j].Confirmed {
			return out[i].Confirmed
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

// isBriefEpisode reports whether m is a session summary / episode, which
// belongs in "Recent work", never in the decisions.
func isBriefEpisode(m BriefMemory) bool {
	scope := strings.ToLower(strings.TrimSpace(m.Scope))
	key := strings.ToLower(strings.TrimSpace(m.Key))
	return scope == "episode_summary" || scope == "episode" ||
		strings.HasPrefix(key, "session/") || strings.HasPrefix(key, "episode/")
}

// briefOverviewKeyRe matches keys of memories that say what the project is.
var briefOverviewKeyRe = regexp.MustCompile(`(?i)(^|[/_.\-])(overview|readme|purpose|about|project[/_-]summary|description|what[-_]?it[-_]?is|architecture[/_\-]stack|stack[/_\-]overview|project[/_\-]goal)($|[/_.\-])`)

// briefOverview finds a memory describing the whole project.
func briefOverview(ms []BriefMemory) (BriefMemory, bool) {
	for _, m := range briefRank(ms) {
		if isBriefEpisode(m) || strings.TrimSpace(m.Content) == "" {
			continue
		}
		if briefOverviewKeyRe.MatchString(m.Key) {
			return m, true
		}
	}
	return BriefMemory{}, false
}

func (in BriefInput) corpus() []BriefMemory {
	if in.Corpus != nil {
		return in.Corpus
	}
	return append(append([]BriefMemory{}, in.Memories...), in.Summaries...)
}

// DeterministicBrief builds the brief without an LLM: what the project is,
// its detected stack, up to 10 deduplicated decisions with their source
// chats, and the two newest session summaries. The same input always yields
// the same brief.
func DeterministicBrief(in BriefInput) BriefResult {
	project := oneLine(strings.TrimSpace(in.Project), 80)
	if project == "" {
		project = "This project"
	}
	corpus := in.corpus()
	stack := DetectStack(corpus)
	overview, hasOverview := briefOverview(append(append([]BriefMemory{}, in.Memories...), corpus...))

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Project brief: %s\n", project)
	if hasOverview {
		fmt.Fprintf(&sb, "What it is: %s\n", briefLine(overview, 220))
	} else {
		fmt.Fprintf(&sb, "What it is: %s\n", briefWhatItIs(project, stack, briefTopics(corpus, stack)))
	}
	if len(stack) > 0 {
		fmt.Fprintf(&sb, "Stack: %s\n", strings.Join(stack, ", "))
	}
	header := sb.String()

	var recent strings.Builder
	seenRecent := map[string]bool{}
	n := 0
	for _, m := range in.Summaries {
		if n >= maxBriefRecent {
			break
		}
		norm := briefNorm(briefSentence(m.Content, 140))
		if norm == "" || seenRecent[norm] {
			continue
		}
		seenRecent[norm] = true
		if n == 0 {
			recent.WriteString("\n## Recent work\n")
		}
		recent.WriteString("- " + briefLine(m, 140) + "\n")
		n++
	}

	decisions := briefDecisions(in.Memories, overview.Key)
	content := header
	if len(decisions) > 0 {
		section := "\n## Key decisions and conventions\n"
		for _, line := range decisions {
			next := section + line + "\n"
			if utf8.RuneCountInString(header+next+recent.String()) > MaxBriefChars {
				break
			}
			section = next
		}
		if strings.Contains(section, "\n- ") {
			content += section
		}
	}
	content += recent.String()
	content = ClampBrief(content)
	return BriefResult{Content: content, Provider: ProviderHeuristic, Sources: BriefSources(content)}
}

// briefDecisions picks up to maxBriefDecisions lines: one per key, no
// episodes, no repeated text, best ranked first, grouped by category.
func briefDecisions(ms []BriefMemory, skipKey string) []string {
	seenKey := map[string]bool{}
	seenText := map[string]bool{}
	var picked []BriefMemory
	for _, m := range briefRank(ms) {
		if len(picked) >= maxBriefDecisions {
			break
		}
		key := strings.ToLower(strings.TrimSpace(m.Key))
		if isBriefEpisode(m) || (skipKey != "" && m.Key == skipKey) || seenKey[key] {
			continue
		}
		text := briefSentence(m.Content, 110)
		norm := briefNorm(text)
		if len(norm) < 8 || seenText[norm] {
			continue
		}
		// Near-duplicates: same opening words.
		prefix := norm
		if len(prefix) > 60 {
			prefix = prefix[:60]
		}
		if seenText["prefix:"+prefix] {
			continue
		}
		if key != "" {
			seenKey[key] = true
		}
		seenText[norm] = true
		seenText["prefix:"+prefix] = true
		picked = append(picked, m)
	}
	groups := map[string][]BriefMemory{}
	for _, m := range picked {
		groups[briefCategory(m)] = append(groups[briefCategory(m)], m)
	}
	var lines []string
	for _, cat := range append(append([]string{}, briefCategoryOrder...), "general") {
		for _, m := range groups[cat] {
			lines = append(lines, "- "+briefCategoryTitle[cat]+": "+briefLine(m, 110))
		}
	}
	return lines
}

func briefCategory(m BriefMemory) string {
	cat := strings.ToLower(strings.TrimSpace(m.Category))
	if _, ok := briefCategoryTitle[cat]; !ok || cat == "" {
		return "general"
	}
	return cat
}

// briefNorm normalises text for duplicate detection.
func briefNorm(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space && b.Len() > 0 {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// briefWebTech marks technologies that make a project a web app.
var briefWebTech = map[string]bool{
	"React": true, "Next.js": true, "Vue": true, "Nuxt": true, "Angular": true, "Svelte": true,
	"Express": true, "NestJS": true, "Fastify": true, "Django": true, "FastAPI": true, "Flask": true,
	"Rails": true, "Laravel": true, "Spring": true, "Tailwind": true, "Vercel": true, "Netlify": true,
}

// briefWhatItIs derives the one-line description from the project name,
// its stack and its most frequent topics.
func briefWhatItIs(project string, stack, topics []string) string {
	kind := "software project"
	for _, t := range stack {
		if briefWebTech[t] {
			kind = "web app"
			break
		}
	}
	var lead []string
	for _, t := range stack {
		if briefTechKind[t] == techFramework || briefTechKind[t] == techLanguage {
			lead = append(lead, t)
		}
		if len(lead) == 2 {
			break
		}
	}
	desc := project + " is a "
	if len(lead) > 0 {
		desc += strings.Join(lead, "/") + " "
	}
	desc += kind
	if len(topics) > 0 {
		desc += "; recurring topics: " + strings.Join(topics, ", ")
	}
	return desc + "."
}

// briefTopicStop are generic key words that say nothing about the domain.
var briefTopicStop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`api apis arch architecture auth conventions convention infra infrastructure
		dependencies dependency deps decision decisions fix fixes fixed fixing bug bugs update updates updated
		add added adds use uses using new project projects session sessions general pattern patterns note notes
		config configuration the and for with from into todo test tests testing change changes changed remove
		removed refactor feature features issue issues support file files code page pages error errors data
		default make made set setup run runs build builds dev prod production local main app apps fact facts
		preference preferences constraint constraints episode summary summaries memory memories item items list
		via not all any per now one two get put post status work task tasks handle handling logic flow
		conv pin pinned misc other temp tmp wip controller service component module handler route routes model
		util utils helper helpers middleware endpoint endpoints schema type types`) {
		briefTopicStop[w] = true
	}
}

var briefTopicSplitRe = regexp.MustCompile(`[^A-Za-z0-9]+|([a-z])([A-Z])`)

// briefTopics returns up to four frequent domain words from memory keys.
func briefTopics(ms []BriefMemory, stack []string) []string {
	tech := map[string]bool{}
	for _, t := range stack {
		tech[strings.ToLower(t)] = true
	}
	for name := range briefTechKind {
		tech[strings.ToLower(name)] = true
	}
	counts := map[string]int{}
	for _, m := range ms {
		if isBriefEpisode(m) {
			continue
		}
		key := briefTopicSplitRe.ReplaceAllString(m.Key, "$1 $2")
		seen := map[string]bool{}
		for _, w := range strings.Fields(strings.ToLower(key)) {
			if len(w) > 4 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
				w = strings.TrimSuffix(w, "s")
			}
			if len(w) < 3 || briefTopicStop[w] || tech[w] || seen[w] || strings.IndexFunc(w, unicode.IsLetter) < 0 {
				continue
			}
			seen[w] = true
			counts[w]++
		}
	}
	type wc struct {
		w string
		c int
	}
	var all []wc
	for w, c := range counts {
		all = append(all, wc{w, c})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].c != all[j].c {
			return all[i].c > all[j].c
		}
		return all[i].w < all[j].w
	})
	var out []string
	for _, x := range all {
		if x.c < 2 || len(out) == 4 {
			break
		}
		out = append(out, x.w)
	}
	return out
}

type techKind int

const (
	techLanguage techKind = iota + 1
	techFramework
	techData
	techInfra
	techTool
)

// briefTech is the small technology dictionary, in Stack display order.
// Ambiguous English words (React, Express, Go, Rust, Node, Spring...) are
// matched case-sensitively or through file/package evidence only.
var briefTech = []struct {
	Name string
	Kind techKind
	Re   *regexp.Regexp
}{
	{"TypeScript", techLanguage, regexp.MustCompile(`(?i)\btypescript\b|[\w\-]\.tsx?\b|\btsconfig\b`)},
	{"JavaScript", techLanguage, regexp.MustCompile(`(?i)\bjavascript\b|[\w\-]\.(m|c)?jsx?\b`)},
	{"Python", techLanguage, regexp.MustCompile(`(?i)\bpython3?\b|[\w\-]\.py\b|requirements\.txt|\bpyproject\.toml\b`)},
	{"Go", techLanguage, regexp.MustCompile(`[\w\-]\.go\b|\b[Gg]olang\b|\bgo\.mod\b|\bgofmt\b|\bGo (module|server|service|backend|code)`)},
	{"Rust", techLanguage, regexp.MustCompile(`\bRust\b|[\w\-]\.rs\b|\bCargo\.toml\b`)},
	{"Java", techLanguage, regexp.MustCompile(`\bJava\b|[\w\-]\.java\b`)},
	{"Kotlin", techLanguage, regexp.MustCompile(`(?i)\bkotlin\b|[\w\-]\.kts?\b`)},
	{"C#", techLanguage, regexp.MustCompile(`C#|[\w\-]\.cs\b|[\w\-]\.csproj\b`)},
	{"Ruby", techLanguage, regexp.MustCompile(`\bRuby\b|[\w\-]\.rb\b|\bGemfile\b`)},
	{"PHP", techLanguage, regexp.MustCompile(`\bPHP\b|[\w\-]\.php\b`)},
	{"Swift", techLanguage, regexp.MustCompile(`\bSwift\b|[\w\-]\.swift\b`)},
	{"Dart", techLanguage, regexp.MustCompile(`\bDart\b|[\w\-]\.dart\b`)},
	{"Node", techFramework, regexp.MustCompile(`(?i)\bnode\.?js\b|\bnpm\b|\bpackage\.json\b|\bpnpm\b|\byarn\b`)},
	{"Express", techFramework, regexp.MustCompile(`\bExpress(\.js)?\b|\bexpress\.(Router|json|static)\b|require\(['"]express['"]\)|from ['"]express['"]`)},
	{"NestJS", techFramework, regexp.MustCompile(`(?i)\bnest\.?js\b|@nestjs/`)},
	{"Fastify", techFramework, regexp.MustCompile(`(?i)\bfastify\b`)},
	{"Next.js", techFramework, regexp.MustCompile(`(?i)\bnext\.?js\b|\bnext\.config\b`)},
	{"React", techFramework, regexp.MustCompile(`\bReact\b|\breact-dom\b|[\w\-]\.jsx\b|[\w\-]\.tsx\b|\buse(State|Effect)\b`)},
	{"React Native", techFramework, regexp.MustCompile(`(?i)\breact[ \-]native\b`)},
	{"Vue", techFramework, regexp.MustCompile(`\bVue(\.js)?\b|[\w\-]\.vue\b`)},
	{"Nuxt", techFramework, regexp.MustCompile(`(?i)\bnuxt\b`)},
	{"Angular", techFramework, regexp.MustCompile(`\bAngular\b|@angular/`)},
	{"Svelte", techFramework, regexp.MustCompile(`(?i)\bsvelte(kit)?\b`)},
	{"Vite", techFramework, regexp.MustCompile(`\bVite\b|\bvite\.config\b`)},
	{"Tailwind", techFramework, regexp.MustCompile(`(?i)\btailwind(css)?\b`)},
	{"Django", techFramework, regexp.MustCompile(`(?i)\bdjango\b`)},
	{"FastAPI", techFramework, regexp.MustCompile(`(?i)\bfastapi\b`)},
	{"Flask", techFramework, regexp.MustCompile(`\bFlask\b`)},
	{"Rails", techFramework, regexp.MustCompile(`\bRails\b`)},
	{"Laravel", techFramework, regexp.MustCompile(`(?i)\blaravel\b`)},
	{"Spring", techFramework, regexp.MustCompile(`\bSpring( Boot)?\b`)},
	{".NET", techFramework, regexp.MustCompile(`\.NET\b|\bASP\.NET\b`)},
	{"WinUI", techFramework, regexp.MustCompile(`(?i)\bwinui\b`)},
	{"Electron", techFramework, regexp.MustCompile(`\bElectron\b`)},
	{"Flutter", techFramework, regexp.MustCompile(`(?i)\bflutter\b`)},
	{"GraphQL", techFramework, regexp.MustCompile(`(?i)\bgraphql\b`)},
	{"Postgres", techData, regexp.MustCompile(`(?i)\bpostgres(ql)?\b|\bpsql\b|\bpgx\b`)},
	{"pgvector", techData, regexp.MustCompile(`(?i)\bpgvector\b`)},
	{"MySQL", techData, regexp.MustCompile(`(?i)\bmysql\b|\bmariadb\b`)},
	{"MongoDB", techData, regexp.MustCompile(`(?i)\bmongo(db)?\b|\bmongoose\b`)},
	{"SQLite", techData, regexp.MustCompile(`(?i)\bsqlite3?\b`)},
	{"Redis", techData, regexp.MustCompile(`(?i)\bredis\b`)},
	{"Prisma", techData, regexp.MustCompile(`(?i)\bprisma\b`)},
	{"Sequelize", techData, regexp.MustCompile(`(?i)\bsequelize\b`)},
	{"Drizzle", techData, regexp.MustCompile(`(?i)\bdrizzle\b`)},
	{"Supabase", techData, regexp.MustCompile(`(?i)\bsupabase\b`)},
	{"Firebase", techData, regexp.MustCompile(`(?i)\bfirebase\b|\bfirestore\b`)},
	{"Docker", techInfra, regexp.MustCompile(`(?i)\bdocker(file)?\b|\bdocker-compose\b`)},
	{"Kubernetes", techInfra, regexp.MustCompile(`(?i)\bkubernetes\b|\bk8s\b|\bkubectl\b`)},
	{"Terraform", techInfra, regexp.MustCompile(`(?i)\bterraform\b|[\w\-]\.tf\b`)},
	{"AWS", techInfra, regexp.MustCompile(`\bAWS\b|\bECS\b|\bS3\b|\bLambda\b|\bCloudFront\b`)},
	{"GCP", techInfra, regexp.MustCompile(`\bGCP\b|(?i)\bgoogle cloud\b|\bcloud run\b`)},
	{"Azure", techInfra, regexp.MustCompile(`(?i)\bazure\b`)},
	{"Vercel", techInfra, regexp.MustCompile(`(?i)\bvercel\b`)},
	{"Netlify", techInfra, regexp.MustCompile(`(?i)\bnetlify\b`)},
	{"Heroku", techInfra, regexp.MustCompile(`(?i)\bheroku\b`)},
	{"Cloudflare", techInfra, regexp.MustCompile(`(?i)\bcloudflare\b`)},
	{"Nginx", techInfra, regexp.MustCompile(`(?i)\bnginx\b`)},
	{"GitHub Actions", techInfra, regexp.MustCompile(`(?i)\bgithub actions\b|\.github/workflows`)},
	{"Jest", techTool, regexp.MustCompile(`\bJest\b|\bjest\.config\b`)},
	{"Vitest", techTool, regexp.MustCompile(`(?i)\bvitest\b`)},
	{"Playwright", techTool, regexp.MustCompile(`(?i)\bplaywright\b`)},
	{"Cypress", techTool, regexp.MustCompile(`(?i)\bcypress\b`)},
	{"pytest", techTool, regexp.MustCompile(`(?i)\bpytest\b`)},
}

var briefTechKind = func() map[string]techKind {
	m := map[string]techKind{}
	for _, t := range briefTech {
		m[t.Name] = t.Kind
	}
	return m
}()

// briefTechImplies adds the runtime a framework needs.
var briefTechImplies = map[string][]string{
	"Express": {"Node"}, "NestJS": {"Node"}, "Fastify": {"Node"}, "Next.js": {"React", "Node"},
	"Django": {"Python"}, "FastAPI": {"Python"}, "Flask": {"Python"}, "Rails": {"Ruby"},
	"Laravel": {"PHP"}, "pgvector": {"Postgres"}, "Nuxt": {"Vue"}, "Flutter": {"Dart"},
}

// IsBriefTech reports whether name is a technology in the brief dictionary.
func IsBriefTech(name string) bool {
	_, ok := briefTechKind[name]
	return ok
}

// DetectStack lists the technologies the memories mention (content and
// files_affected), deduplicated, most mentioned first (ties in dictionary
// order). A technology seen in only one memory is noise when the project is
// large or another technology is clearly established.
func DetectStack(ms []BriefMemory) []string {
	counts := map[string]int{}
	for _, m := range ms {
		text := m.Key + "\n" + m.Content + "\n" + strings.Join(m.FilesAffected, "\n")
		found := map[string]bool{}
		for _, t := range briefTech {
			if t.Re.MatchString(text) {
				found[t.Name] = true
			}
		}
		for name := range found {
			for _, dep := range briefTechImplies[name] {
				found[dep] = true
			}
		}
		for name := range found {
			counts[name]++
		}
	}
	top := 0
	for _, c := range counts {
		if c > top {
			top = c
		}
	}
	min := 1
	if len(ms) >= 20 || top >= 3 {
		min = 2
	}
	has := func(n string) bool { return counts[n] >= min }
	var out []string
	for _, t := range briefTech {
		if !has(t.Name) {
			continue
		}
		// JavaScript is implied by a JS runtime/framework; TypeScript stays.
		if t.Name == "JavaScript" && (has("Node") || has("React") || has("Vue") || has("TypeScript") || has("Angular") || has("Svelte")) {
			continue
		}
		out = append(out, t.Name)
	}
	sort.SliceStable(out, func(i, j int) bool { return counts[out[i]] > counts[out[j]] })
	if len(out) > maxBriefStack {
		out = out[:maxBriefStack]
	}
	return out
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

// BuildBriefPrompt is the compact brief prompt: the project name, detected
// stack and topics, top memories (with category and source chat), and
// recent session summaries.
func BuildBriefPrompt(in BriefInput) string {
	var sb strings.Builder
	project := oneLine(in.Project, 80)
	fmt.Fprintf(&sb, "Project: %s\n", project)
	corpus := in.corpus()
	stack := DetectStack(corpus)
	if len(stack) > 0 {
		fmt.Fprintf(&sb, "Detected stack (from files and mentions): %s\n", strings.Join(stack, ", "))
	}
	if topics := briefTopics(corpus, stack); len(topics) > 0 {
		fmt.Fprintf(&sb, "Recurring topics: %s\n", strings.Join(topics, ", "))
	}
	sb.WriteString("\nTop memories (category | content | source):\n")
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
	fmt.Fprintf(&sb, "\nWrite the project brief as plain markdown (no JSON, no code fence), under %d characters, in exactly this shape:\n"+
		"# Project brief: %s\n"+
		"What it is: <one sentence: what the product does and for whom>\n"+
		"Stack: <comma-separated technology names only, no sentences>\n\n"+
		"## Key decisions and conventions\n"+
		"- <5-10 bullets: the most important durable decisions and conventions, one per topic, no duplicates, no bug-fix diary>\n\n"+
		"## Recent work\n"+
		"- <at most 2 short bullets>\n\n"+
		"End each bullet with its source as (session:<id>) when the memory lists one; never invent sources.", MaxBriefChars, project)
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

// CompleteBrief asks the model for the brief and returns its markdown. It
// asks for plain markdown (no JSON mode: free models often break the JSON
// escaping of a multi-line string) and accepts JSON {"brief": ...} too.
func (c *Client) CompleteBrief(ctx context.Context, prompt string) (string, error) {
	content, err := c.chatMode(ctx, c.model(), defaultOpenRouterFallbacks,
		"You write a short onboarding brief for coding agents joining a software project, using only the memories given. "+
			"Be concrete; keep names, files, and tools. No preamble. Reply with the markdown brief only.",
		prompt, false)
	if err != nil {
		return "", err
	}
	return ParseBriefReply(content)
}

var (
	briefThinkRe    = regexp.MustCompile(`(?is)<think>.*?</think>`)
	briefFenceRe    = regexp.MustCompile("(?s)```[A-Za-z0-9_-]*[ \t]*\r?\n(.*?)\r?\n?```")
	briefPreambleRe = regexp.MustCompile(`(?i)^(here('s| is| are)|sure|certainly|below is)\b[^\n]*:\s*\n`)
	briefJSONStrRe  = regexp.MustCompile(`(?s)"(brief|markdown|content|text)"\s*:\s*"(.*)"\s*}\s*$`)
)

// ParseBriefReply turns a model reply into brief markdown. It accepts a JSON
// object {"brief": "..."} (raw or fenced, including JSON with unescaped
// newlines) or plain markdown (raw or fenced), strips <think> blocks and a
// "Here is..." preamble, and rejects replies that do not look like a brief.
func ParseBriefReply(reply string) (string, error) {
	s := strings.TrimSpace(briefThinkRe.ReplaceAllString(reply, ""))
	if m := briefFenceRe.FindStringSubmatch(s); m != nil {
		before := strings.TrimSpace(s[:strings.Index(s, "```")])
		if utf8.RuneCountInString(before) <= 200 {
			s = strings.TrimSpace(m[1])
		}
	} else if strings.HasPrefix(s, "```") {
		// Unclosed fence: drop the opening line.
		if i := strings.Index(s, "\n"); i >= 0 {
			s = strings.TrimSpace(s[i+1:])
		}
	}
	s = strings.TrimSpace(briefPreambleRe.ReplaceAllString(s, ""))
	if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		brief, ok := briefFromJSON(s)
		if !ok {
			return "", fmt.Errorf("%w: JSON without a brief", errBriefUnparseable)
		}
		s = strings.TrimSpace(brief)
	}
	if !looksLikeBrief(s) {
		return "", fmt.Errorf("%w: %d chars", errBriefUnparseable, utf8.RuneCountInString(s))
	}
	return s, nil
}

func briefFromJSON(s string) (string, bool) {
	var obj map[string]any
	if err := json.Unmarshal(extractJSONPayload([]byte(s)), &obj); err == nil {
		for _, k := range []string{"brief", "markdown", "content", "text"} {
			if v, ok := obj[k].(string); ok && strings.TrimSpace(v) != "" {
				return v, true
			}
		}
		return "", false
	}
	// Invalid JSON, typically raw newlines inside the string: re-escape them.
	m := briefJSONStrRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	inner := strings.ReplaceAll(m[2], "\r", "")
	inner = strings.ReplaceAll(inner, "\n", `\n`)
	inner = strings.ReplaceAll(inner, "\t", `\t`)
	var out string
	if err := json.Unmarshal([]byte(`"`+inner+`"`), &out); err != nil {
		return "", false
	}
	return out, strings.TrimSpace(out) != ""
}

// looksLikeBrief rejects refusals, one-liners, and leftover JSON.
func looksLikeBrief(s string) bool {
	if utf8.RuneCountInString(s) < 40 || strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		return false
	}
	lines := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			lines++
		}
	}
	return lines >= 2
}
