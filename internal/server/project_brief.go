package server

// project_brief.go — the per-project brief every agent gets (key
// project/brief) and file-change staleness for memories.
//
// The harvest worker calls the brief hook after every job, failed or not:
// under an OpenRouter rate limit no job finishes, and a hook that ran on
// success only left busy projects without a brief. When the job's LLM call
// failed the brief is built by the deterministic generator, and a later job
// with a working LLM upgrades it (briefUpgradeAge). GET on a project without
// a brief also schedules one in the background.
// The brief is rebuilt when it is missing, when at least briefMinNew
// memories were added since it was written, or when it is older than
// briefMaxAge and something changed. briefDebounce stops a busy project from
// re-checking on every job. Only project-visible project/organization rows
// feed it: personal and private memories never reach the brief.

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	memctx "central-memory/internal/context"
	"central-memory/internal/extract"
	"central-memory/internal/mcp"
	"central-memory/internal/store"
)

const (
	// briefMinNew memories added since the last brief trigger a rebuild.
	briefMinNew = 10
	// briefMaxAge: an older brief is rebuilt when anything changed.
	briefMaxAge = 24 * time.Hour
	// briefDebounce is the minimum gap between brief checks per project.
	briefDebounce = 10 * time.Minute
	// briefScan bounds how many memories one check reads.
	briefScan = 1000
	// briefInputMemories / briefInputSummaries bound the generator input.
	briefInputMemories  = 30
	briefInputSummaries = 5
	// briefCorpusChars bounds each row's text used for stack/topic detection.
	briefCorpusChars = 600
	// briefUpgradeAge: a deterministic brief written because the LLM failed
	// is rebuilt by the next job whose LLM works, once it is this old.
	briefUpgradeAge = time.Hour
	// briefBackgroundTimeout bounds a brief build scheduled by GET.
	briefBackgroundTimeout = 2 * time.Minute
)

// briefClockNow is the brief clock (tests may override Server.briefNow).
func (s *Server) briefClockNow() time.Time {
	if s != nil && s.briefNow != nil {
		return s.briefNow()
	}
	return time.Now().UTC()
}

// briefCheckAllowed applies the per-project debounce and records the check.
func (s *Server) briefCheckAllowed(projectID string, now time.Time) bool {
	s.briefMu.Lock()
	defer s.briefMu.Unlock()
	if s.briefChecked == nil {
		s.briefChecked = map[string]time.Time{}
	}
	if last, ok := s.briefChecked[projectID]; ok && now.Sub(last) < briefDebounce {
		return false
	}
	s.briefChecked[projectID] = now
	return true
}

// memoryKeyFinder is the exact-key lookup (MemStore and Postgres).
type memoryKeyFinder interface {
	FindMemoryByKey(ctx context.Context, projectID, key string) (*store.MemoryItem, error)
}

// loadProjectBrief finds the CONFIRMED brief row of projectID visible in ctx.
// It runs on every MCP initialize and project_knowledge call, so it reads
// by exact key without the embedding column.
func loadProjectBrief(ctx context.Context, st store.Store, projectID string) *store.MemoryItem {
	if st == nil || strings.TrimSpace(projectID) == "" {
		return nil
	}
	ctx = store.WithoutEmbeddings(ctx)
	if f, ok := st.(memoryKeyFinder); ok {
		it, err := f.FindMemoryByKey(ctx, projectID, store.ProjectBriefKey)
		if err != nil || it == nil || it.Status != store.StatusConfirmed {
			return nil
		}
		return it
	}
	items, err := st.SearchMemory(ctx, projectID, store.ProjectBriefKey, nil, 50)
	if err != nil {
		return nil
	}
	for _, it := range items {
		if it != nil && it.Key == store.ProjectBriefKey && it.ProjectID == projectID && it.Status == store.StatusConfirmed {
			return it
		}
	}
	return nil
}

// briefEligible reports whether m may feed the project-visible brief.
func briefEligible(m *store.MemoryItem) bool {
	if m == nil || m.Key == store.ProjectBriefKey {
		return false
	}
	if m.Status != store.StatusConfirmed && m.Status != store.StatusProposed {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(m.Level)) {
	case store.LevelProject, store.LevelOrganization:
	default:
		return false // personal, session, ephemeral
	}
	switch store.NormalizeVisibility(m.Visibility) {
	case store.VisibilityProject, store.VisibilityPublic:
	default:
		return false // private or shared with named people only
	}
	return true
}

func isSessionSummary(m *store.MemoryItem) bool {
	return m.Scope == "episode_summary" || strings.HasPrefix(m.Key, "session/")
}

// memorySessionID is the chat a memory came from: its "session:<id>" tag,
// else its session_id column.
func memorySessionID(m *store.MemoryItem) string {
	for _, t := range m.Tags {
		t = strings.TrimSpace(t)
		if strings.HasPrefix(t, "session:") && len(t) > len("session:") {
			return strings.TrimPrefix(t, "session:")
		}
	}
	return strings.TrimSpace(m.SessionID)
}

func hasTag(m *store.MemoryItem, tag string) bool {
	for _, t := range m.Tags {
		if strings.EqualFold(strings.TrimSpace(t), tag) {
			return true
		}
	}
	return false
}

// briefCandidates lists the project-visible rows that may feed the brief.
// Callers (harvest worker, refresh route) read without a viewer, so private
// and shared rows are already left out; briefEligible re-checks level and
// visibility so a viewer-scoped ctx could never leak them either.
func (s *Server) briefCandidates(ctx context.Context, projectID string) ([]*store.MemoryItem, error) {
	all, err := s.Store.SearchMemory(store.WithoutEmbeddings(ctx), projectID, "", nil, briefScan)
	if err != nil {
		return nil, err
	}
	out := make([]*store.MemoryItem, 0, len(all))
	for _, m := range all {
		if briefEligible(m) {
			out = append(out, m)
		}
	}
	return out, nil
}

// briefDue reports whether the brief should be rebuilt now.
func briefDue(brief *store.MemoryItem, candidates []*store.MemoryItem, now time.Time) bool {
	if brief == nil {
		return len(candidates) > 0
	}
	since := brief.UpdatedAt
	added, changed := 0, false
	for _, m := range candidates {
		if m.CreatedAt.After(since) {
			added++
			changed = true
		} else if m.UpdatedAt.After(since) {
			changed = true
		}
	}
	if added >= briefMinNew {
		return true
	}
	return changed && now.Sub(since) >= briefMaxAge
}

// briefUpgradeDue reports a deterministic brief written because the LLM
// failed (not because no key is configured) that is old enough to retry.
func briefUpgradeDue(brief *store.MemoryItem, now time.Time) bool {
	if brief == nil {
		return false
	}
	reason := briefTagValue(brief, briefFallbackTag)
	return reason != "" && reason != extract.BriefFallbackNoKey && now.Sub(brief.UpdatedAt) >= briefUpgradeAge
}

// maybeRefreshProjectBrief is the brief hook with the LLM path enabled.
func (s *Server) maybeRefreshProjectBrief(ctx context.Context, projectID string) {
	s.maybeRefreshProjectBriefWith(ctx, projectID, "")
}

// maybeRefreshProjectBriefWith is the harvest worker hook. It never fails
// the job: errors are logged and the next check retries. A non-empty
// heuristicReason (the job's LLM call just failed) skips the LLM and builds
// the deterministic brief tagged with that reason.
func (s *Server) maybeRefreshProjectBriefWith(ctx context.Context, projectID, heuristicReason string) {
	if s == nil || s.Store == nil || strings.TrimSpace(projectID) == "" {
		return
	}
	if !s.briefCheckAllowed(projectID, s.briefClockNow()) {
		return
	}
	candidates, err := s.briefCandidates(ctx, projectID)
	if err != nil {
		s.logBrief("brief: list project=%s: %v", projectID, err)
		return
	}
	brief := loadProjectBrief(ctx, s.Store, projectID)
	now := s.briefClockNow()
	if !briefDue(brief, candidates, now) && (heuristicReason != "" || !briefUpgradeDue(brief, now)) {
		return
	}
	bctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if _, err := s.rebuildProjectBriefWith(bctx, projectID, candidates, brief, heuristicReason); err != nil {
		s.logBrief("brief: rebuild project=%s: %v", projectID, err)
	}
}

func (s *Server) logBrief(format string, args ...any) {
	if s != nil && s.Log != nil {
		s.Log.Printf(format, args...)
	}
}

// errNoBriefInput means the project has no memories a brief could use.
var errNoBriefInput = errors.New("no project memories to build a brief from")

// briefInput turns candidates into the generator input: summaries newest
// first, other rows by pin, confidence, and use.
func (s *Server) briefInput(ctx context.Context, projectID string, candidates []*store.MemoryItem) extract.BriefInput {
	in := extract.BriefInput{Project: projectID}
	if p, err := s.Store.GetProject(ctx, projectID); err == nil && p != nil {
		in.Project = firstNonEmpty(p.DisplayName, p.FolderName, projectID)
	}
	var mems, sums []*store.MemoryItem
	for _, m := range candidates {
		if isSessionSummary(m) {
			sums = append(sums, m)
		} else {
			mems = append(mems, m)
		}
	}
	sort.SliceStable(sums, func(i, j int) bool { return sums[i].UpdatedAt.After(sums[j].UpdatedAt) })
	if len(sums) > briefInputSummaries {
		sums = sums[:briefInputSummaries]
	}
	sort.SliceStable(mems, func(i, j int) bool {
		pi, pj := hasTag(mems[i], "pinned"), hasTag(mems[j], "pinned")
		if pi != pj {
			return pi
		}
		if mems[i].Confidence != mems[j].Confidence {
			return mems[i].Confidence > mems[j].Confidence
		}
		return mems[i].UseCount > mems[j].UseCount
	})
	if len(mems) > briefInputMemories {
		mems = mems[:briefInputMemories]
	}
	conv := func(m *store.MemoryItem) extract.BriefMemory {
		return extract.BriefMemory{
			Key: m.Key, Content: m.Content, Category: m.Category, Scope: m.Scope,
			Confidence: float64(m.Confidence), UseCount: m.UseCount,
			Pinned: hasTag(m, "pinned"), Confirmed: m.Status == store.StatusConfirmed,
			SessionID: memorySessionID(m), FilesAffected: m.FilesAffected,
		}
	}
	// Every eligible row feeds stack and topic detection (bounded text).
	in.Corpus = make([]extract.BriefMemory, 0, len(candidates))
	for _, m := range candidates {
		c := conv(m)
		if r := []rune(c.Content); len(r) > briefCorpusChars {
			c.Content = string(r[:briefCorpusChars])
		}
		in.Corpus = append(in.Corpus, c)
	}
	for _, m := range mems {
		in.Memories = append(in.Memories, conv(m))
	}
	for _, m := range sums {
		in.Summaries = append(in.Summaries, conv(m))
	}
	return in
}

// Brief row tags recording how the brief was produced.
const (
	briefProviderTag = "brief-provider:"
	briefFallbackTag = "brief-fallback:"
)

// briefTags are the brief row's tags: pinned, the brief marker, the
// provider, the fallback reason (deterministic briefs only), and one
// brief-source tag per cited session.
func briefTags(res extract.BriefResult) []string {
	tags := []string{"pinned", store.ProjectBriefTag, briefProviderTag + res.Provider}
	if res.FallbackReason != "" {
		tags = append(tags, briefFallbackTag+res.FallbackReason)
	}
	for _, sid := range res.Sources {
		tags = append(tags, store.ProjectBriefSourceTag+sid)
	}
	return tags
}

// briefTagValue reads one prefixed tag value from the brief row.
func briefTagValue(m *store.MemoryItem, prefix string) string {
	if m == nil {
		return ""
	}
	for _, t := range m.Tags {
		if strings.HasPrefix(t, prefix) {
			return strings.TrimPrefix(t, prefix)
		}
	}
	return ""
}

// warnBriefFallback logs a WARN once per project and reason. The error
// detail goes to the log only, never into the brief.
func (s *Server) warnBriefFallback(projectID string, res extract.BriefResult) {
	s.briefMu.Lock()
	if s.briefWarned == nil {
		s.briefWarned = map[string]string{}
	}
	prev := s.briefWarned[projectID]
	s.briefWarned[projectID] = res.FallbackReason
	s.briefMu.Unlock()
	if res.FallbackReason == "" || res.FallbackReason == prev {
		return
	}
	detail := res.LLMError
	if detail == "" {
		detail = "-"
	}
	s.logBrief("WARN brief: project=%s used the deterministic brief (reason=%s): %s", projectID, res.FallbackReason, detail)
}

// briefSources reads the cited session IDs back from the brief row's tags.
func briefSources(m *store.MemoryItem) []string {
	out := []string{}
	if m == nil {
		return out
	}
	for _, t := range m.Tags {
		if strings.HasPrefix(t, store.ProjectBriefSourceTag) {
			out = append(out, strings.TrimPrefix(t, store.ProjectBriefSourceTag))
		}
	}
	return out
}

// rebuildProjectBrief generates the brief and writes it: an in-place edit
// of the existing row (keeps one row and its history) or a new CONFIRMED
// row. candidates nil means "list them now".
func (s *Server) rebuildProjectBrief(ctx context.Context, projectID string, candidates []*store.MemoryItem, existing *store.MemoryItem) (*store.MemoryItem, error) {
	return s.rebuildProjectBriefWith(ctx, projectID, candidates, existing, "")
}

// rebuildProjectBriefWith is rebuildProjectBrief; a non-empty
// heuristicReason skips the LLM (see maybeRefreshProjectBriefWith).
func (s *Server) rebuildProjectBriefWith(ctx context.Context, projectID string, candidates []*store.MemoryItem, existing *store.MemoryItem, heuristicReason string) (*store.MemoryItem, error) {
	if candidates == nil {
		var err error
		candidates, err = s.briefCandidates(ctx, projectID)
		if err != nil {
			return nil, err
		}
		existing = loadProjectBrief(ctx, s.Store, projectID)
	}
	if len(candidates) == 0 {
		return nil, errNoBriefInput
	}
	in := s.briefInput(ctx, projectID, candidates)
	var res extract.BriefResult
	if heuristicReason != "" {
		res = extract.DeterministicBrief(in)
		res.FallbackReason = heuristicReason
	} else {
		res = s.resolveExtractor().GenerateBrief(ctx, in)
	}
	s.warnBriefFallback(projectID, res)
	content := extract.ClampBrief(res.Content)
	if err := store.ValidateMemoryContent(content); err != nil {
		return nil, errNoBriefInput
	}
	tags := briefTags(res)
	source := "brief:" + res.Provider
	if existing != nil {
		if es, ok := s.memoryEditStore(); ok {
			// Source is patched too: a brief first written by the heuristic
			// must not keep "brief:heuristic" after an LLM rebuild.
			item, err := es.UpdateMemory(ctx, existing.ID, store.MemoryPatch{Content: &content, Tags: &tags, Source: &source}, "")
			if err != nil {
				return nil, err
			}
			s.publishMemoryLifecycle(item, "MEMORY_CONFIRMED", "confirmed")
			return item, nil
		}
	}
	item := &store.MemoryItem{
		ProjectID:     projectID,
		Key:           store.ProjectBriefKey,
		Content:       content,
		Level:         store.LevelProject,
		Scope:         "fact",
		Confidence:    1.0,
		Status:        store.StatusConfirmed,
		Source:        source,
		Tags:          tags,
		Category:      "architecture",
		Outcome:       "active",
		Visibility:    store.VisibilityProject,
		FilesAffected: []string{},
		ToolsUsed:     []string{},
	}
	item.Embedding = s.embedText(ctx, memctx.EmbedTextForItem(item.Key, item.Content))
	if err := s.Store.CreateMemoryItem(ctx, item); err != nil {
		return nil, err
	}
	s.publishMemoryLifecycle(item, "MEMORY_CONFIRMED", "confirmed")
	return item, nil
}

// ---- HTTP ----

func (s *Server) registerProjectBriefRoutes() {
	s.Mux.HandleFunc("GET /v1/projects/{id}/brief", s.requireAuth(s.handleProjectBriefGet))
	s.Mux.HandleFunc("POST /v1/projects/{id}/brief/refresh", s.requireAuth(s.handleProjectBriefRefresh))
	s.Mux.HandleFunc("POST /v1/projects/{id}/files-changed", s.requireAuth(s.handleProjectFilesChanged))
}

type projectBriefResponse struct {
	Content   string     `json:"content"`
	UpdatedAt *time.Time `json:"updated_at"`
	Sources   []string   `json:"sources"`
	// Provider is how the brief was produced (openrouter | heuristic);
	// FallbackReason is a short code when it is deterministic although an
	// LLM brief was wanted (e.g. no_api_key, timeout, unparseable_reply).
	Provider       string `json:"provider,omitempty"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	// Building is true when the project has no brief yet and this request
	// scheduled one in the background (poll again shortly).
	Building bool `json:"building,omitempty"`
}

func briefResponse(m *store.MemoryItem) projectBriefResponse {
	if m == nil {
		return projectBriefResponse{Sources: []string{}}
	}
	at := m.UpdatedAt
	provider := briefTagValue(m, briefProviderTag)
	if provider == "" {
		provider = strings.TrimPrefix(m.Source, "brief:")
	}
	return projectBriefResponse{
		Content: m.Content, UpdatedAt: &at, Sources: briefSources(m),
		Provider: provider, FallbackReason: briefTagValue(m, briefFallbackTag),
	}
}

// handleProjectBriefGet returns the brief to project members. A project
// without one yet answers 200 with empty content and null updated_at, and
// schedules a build in the background (debounced per project) so projects
// whose harvest jobs never succeed still get a brief.
func (s *Server) handleProjectBriefGet(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeProject(w, r, projectID) {
		return
	}
	brief := loadProjectBrief(r.Context(), s.Store, projectID)
	resp := briefResponse(brief)
	if brief == nil {
		resp.Building = s.scheduleProjectBrief(projectID)
	}
	writeJSON(w, http.StatusOK, resp)
}

// scheduleProjectBrief starts a background brief check for a project
// without one. It reads without a viewer (project-visible rows only), like
// the harvest worker, and reports whether a check was started (false while
// the per-project debounce holds).
func (s *Server) scheduleProjectBrief(projectID string) bool {
	s.briefMu.Lock()
	last, seen := s.briefChecked[projectID]
	s.briefMu.Unlock()
	if seen && s.briefClockNow().Sub(last) < briefDebounce {
		return false
	}
	s.briefWG.Add(1)
	go func() {
		defer s.briefWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), briefBackgroundTimeout)
		defer cancel()
		s.maybeRefreshProjectBrief(ctx, projectID)
	}()
	return true
}

// authorizeProjectAdmin admits project OWNER/ADMIN and org owners/admins.
func (s *Server) authorizeProjectAdmin(w http.ResponseWriter, r *http.Request, projectID string) bool {
	if !s.authorizeProject(w, r, projectID) {
		return false
	}
	if rs, ok := s.Store.(projectRoleStore); ok {
		role, err := rs.GetMemberRole(r.Context(), authSubject(r), projectID)
		if err == nil && (role == store.RoleOwner || role == store.RoleAdmin) {
			return true
		}
	}
	if p, err := s.Store.GetProject(r.Context(), projectID); err == nil && p != nil && p.OrgID != "" {
		if os, ok := s.orgStore(); ok {
			role, rerr := os.GetOrgMemberRole(r.Context(), p.OrgID, authSubject(r))
			if rerr == nil && store.OrgManages(role) {
				return true
			}
		}
	}
	writeError(w, http.StatusForbidden, "project owner or admin required")
	return false
}

// handleProjectBriefRefresh rebuilds the brief now, ignoring the debounce.
func (s *Server) handleProjectBriefRefresh(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeProjectAdmin(w, r, projectID) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	item, err := s.rebuildProjectBrief(ctx, projectID, nil, nil)
	if errors.Is(err, errNoBriefInput) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "brief rebuild failed: "+err.Error())
		return
	}
	s.briefMu.Lock()
	if s.briefChecked == nil {
		s.briefChecked = map[string]time.Time{}
	}
	s.briefChecked[projectID] = s.briefClockNow()
	s.briefMu.Unlock()
	writeJSON(w, http.StatusOK, briefResponse(item))
}

// staleMarker is the store seam for files-changed (MemStore and Postgres).
type staleMarker interface {
	MarkMemoriesStaleForFiles(ctx context.Context, projectID string, paths []string, at time.Time) ([]string, error)
}

type filesChangedRequest struct {
	Paths []string `json:"paths"`
	// Hashes (optional) maps paths to their current sha256 content hash.
	// Memories linked to a file with a different observed hash are marked
	// stale; those linked with the same hash are known fresh and skip the
	// path-and-time rule.
	Hashes map[string]string `json:"hashes,omitempty"`
	// At is when the files changed (RFC 3339); empty means now. Only
	// memories created before it are marked.
	At string `json:"at,omitempty"`
}

// handleProjectFilesChanged marks CONFIRMED memories about the changed
// files, written before at, as stale. The desktop file watcher calls it.
func (s *Server) handleProjectFilesChanged(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizePermission(w, r, projectID, store.PermMemoryWrite) {
		return
	}
	sm, ok := s.Store.(staleMarker)
	if !ok {
		writeError(w, http.StatusNotImplemented, "staleness tracking unavailable")
		return
	}
	var req filesChangedRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Paths) == 0 && len(req.Hashes) > 0 {
		for p := range req.Hashes {
			req.Paths = append(req.Paths, p)
		}
		sort.Strings(req.Paths)
	}
	if len(req.Paths) == 0 {
		writeError(w, http.StatusBadRequest, "paths required")
		return
	}
	if len(req.Paths) > store.MaxStalePaths || len(req.Hashes) > store.MaxStalePaths {
		writeError(w, http.StatusBadRequest, "too many paths")
		return
	}
	now := time.Now().UTC()
	at := now
	if raw := strings.TrimSpace(req.At); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "at must be RFC 3339")
			return
		}
		at = parsed.UTC()
		if at.After(now) {
			at = now
		}
	}
	ctx := r.Context()
	var ids []string
	fresh := []string{}
	if cl, ok := s.codeLinkStore(); ok {
		// Hash rule first: record current hashes, mark memories whose link
		// saw another hash, and spare those whose link saw this one.
		except := map[string]bool{}
		if len(req.Hashes) > 0 {
			hashed := make([]string, 0, len(req.Hashes))
			for p, h := range req.Hashes {
				if strings.TrimSpace(h) == "" {
					continue
				}
				if _, err := cl.SetCodeEntityHash(ctx, projectID, p, h, at); err != nil {
					writeError(w, http.StatusInternalServerError, "record hashes failed: "+err.Error())
					return
				}
				hashed = append(hashed, p)
			}
			sort.Strings(hashed)
			marked, ok, err := cl.MarkStaleByCodeHash(ctx, projectID, hashed, at)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "mark stale failed: "+err.Error())
				return
			}
			ids = append(ids, marked...)
			fresh = ok
			for _, id := range marked {
				except[id] = true
			}
			for _, id := range ok {
				except[id] = true
			}
		}
		more, err := cl.MarkMemoriesStaleForFilesExcept(ctx, projectID, req.Paths, at, except)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "mark stale failed: "+err.Error())
			return
		}
		ids = append(ids, more...)
	} else {
		marked, err := sm.MarkMemoriesStaleForFiles(ctx, projectID, req.Paths, at)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "mark stale failed: "+err.Error())
			return
		}
		ids = marked
	}
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"marked": len(ids), "ids": ids, "fresh": len(fresh), "at": at})
}

// ---- cloud MCP adapter ----

// ProjectBrief gives cloud agents the brief (mcp.BriefProvider). It reads
// as the agent's user, so non-members get nothing.
func (a agentMCPStore) ProjectBrief(ctx context.Context, projectID string) (string, error) {
	brief := loadProjectBrief(a.viewerCtx(ctx), a.Store, projectID)
	if brief == nil {
		return "", nil
	}
	return brief.Content, nil
}

// SearchMemoryByFile is the cloud fast path for memory_search file=.
func (a agentMCPStore) SearchMemoryByFile(ctx context.Context, projectID, path string, limit int) ([]*mcp.MemoryItem, error) {
	fs, ok := a.Store.(interface {
		SearchMemoryByFile(ctx context.Context, projectID, path string, limit int) ([]*store.MemoryItem, error)
	})
	if !ok {
		items, err := a.Store.SearchMemory(a.viewerCtx(ctx), projectID, "", nil, 200)
		if err != nil {
			return nil, err
		}
		out := make([]*store.MemoryItem, 0, limit)
		for _, it := range items {
			if store.MemoryTouchesFile(it, path) {
				out = append(out, it)
			}
		}
		return storeToMCPMemories(out), nil
	}
	items, err := fs.SearchMemoryByFile(a.viewerCtx(ctx), projectID, path, limit)
	if err != nil {
		return nil, err
	}
	return storeToMCPMemories(items), nil
}
