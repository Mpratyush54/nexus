package server

import (
	"net/http"
	"strings"

	memctx "central-memory/internal/context"
	"central-memory/internal/extract"
	"central-memory/internal/store"
)

// extractRequest is the daemon → server harvest payload.
type extractRequest struct {
	ProjectID string          `json:"project_id"`
	Turns     []extract.Turn  `json:"turns"`
	Source    string          `json:"source,omitempty"`
	Existing  []extract.Existing `json:"existing,omitempty"`
}

func (s *Server) resolveExtractor() *extract.Service {
	if s != nil && s.Extractor != nil {
		return s.Extractor
	}
	return extract.NewService(extract.ConfigFromEnv())
}

// handleMemoryExtract runs server-side OpenRouter (or heuristic) extraction
// and persists PROPOSED memories. The OpenRouter key never leaves the server.
func (s *Server) handleMemoryExtract(w http.ResponseWriter, r *http.Request) {
	var req extractRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if !s.authorizePermission(w, r, projectID, store.PermMemoryWrite) {
		return
	}
	turns := extract.CapTurns(req.Turns)
	if len(turns) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"items":    []*store.MemoryItem{},
			"count":    0,
			"provider": extract.ProviderHeuristic,
		})
		return
	}

	totalChars := 0
	for _, t := range turns {
		totalChars += len(t.Content)
	}
	ownerType, ownerID := s.billingOwnerForProject(r.Context(), projectID)
	if !s.enforcePlanDimension(w, r, ownerType, ownerID, "memories") {
		return
	}
	if !s.eventAllowed("memory-extract:" + projectID) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}
	if ok, reason := s.quotaAllowed(totalChars); !ok {
		writeError(w, http.StatusTooManyRequests, "quota exceeded: "+reason)
		return
	}

	existing := req.Existing
	if len(existing) == 0 {
		existing = s.extractExistingHints(r, projectID)
	}

	svc := s.resolveExtractor()
	result, err := svc.Extract(r.Context(), projectID, turns, existing)
	if err != nil {
		writeError(w, http.StatusBadGateway, "extraction failed: "+err.Error())
		return
	}
	if result.LLMError != "" {
		s.Log.Printf("memory extract: openrouter failed for %s: %s (falling back to heuristic)", projectID, result.LLMError)
	}

	srcHint := strings.TrimSpace(req.Source)
	created := make([]*store.MemoryItem, 0, len(result.Proposals))
	for _, p := range result.Proposals {
		item, cerr := s.persistExtractProposal(r, projectID, p, srcHint, result.Provider)
		if cerr != nil {
			continue
		}
		created = append(created, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     created,
		"count":     len(created),
		"provider":  result.Provider,
		"llm_error": result.LLMError,
	})
}

func (s *Server) extractExistingHints(r *http.Request, projectID string) []extract.Existing {
	items, err := s.Store.SearchMemory(r.Context(), projectID, "", nil, 30)
	if err != nil || len(items) == 0 {
		return nil
	}
	out := make([]extract.Existing, 0, len(items))
	for _, it := range items {
		if it == nil {
			continue
		}
		out = append(out, extract.Existing{
			Level:   it.Level,
			Scope:   it.Scope,
			Content: it.Content,
		})
	}
	return out
}

func (s *Server) persistExtractProposal(r *http.Request, projectID string, p extract.Proposal, srcHint, provider string) (*store.MemoryItem, error) {
	content := strings.TrimSpace(p.Content)
	if err := store.ValidateMemoryContent(content); err != nil {
		return nil, err
	}
	level := strings.ToLower(strings.TrimSpace(p.Level))
	switch level {
	case "organization", "project", "personal":
	default:
		// Harvested chats have no portal session_id — avoid session rows.
		level = "project"
	}
	scope := strings.ToLower(strings.TrimSpace(p.Scope))
	if scope == "" {
		scope = "fact"
	}
	source := strings.TrimSpace(p.Source)
	if source == "" {
		if provider == extract.ProviderOpenRouter {
			source = "processor:openrouter"
		} else {
			source = "processor:heuristic"
		}
	}
	if srcHint != "" {
		source = source + ":" + srcHint
	}
	key := strings.TrimSpace(p.Key)
	if key == "" {
		key = extract.KeyFromContent(content)
	}
	conf := float32(p.Confidence)
	if conf <= 0 {
		conf = 0.7
	}
	item := &store.MemoryItem{
		ProjectID:  projectID,
		Key:        key,
		Content:    content,
		Level:      level,
		Scope:      scope,
		Confidence: conf,
		Status:     store.StatusProposed,
		Source:     source,
		ProposedBy: authSubject(r),
	}
	if level == "personal" {
		item.UserID = authSubject(r)
	}
	item.Embedding = s.embedText(r.Context(), memctx.EmbedTextForItem(item.Key, item.Content))
	if err := s.Store.CreateMemoryItem(r.Context(), item); err != nil {
		return nil, err
	}
	s.notifyProjectActivity(projectID, "MEMORY_PROPOSED", "/app/memory")
	s.publishMemoryLifecycle(item, "MEMORY_PROPOSED", "proposed")
	return item, nil
}
