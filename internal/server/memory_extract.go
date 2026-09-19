package server

import (
	"net/http"
	"strings"

	memctx "central-memory/internal/context"
	"central-memory/internal/extract"
	"central-memory/internal/store"
)

// extractRequest is the daemon → server harvest payload (legacy sync path).
type extractRequest struct {
	ProjectID string             `json:"project_id"`
	Turns     []extract.Turn     `json:"turns"`
	Source    string             `json:"source,omitempty"`
	Existing  []extract.Existing `json:"existing,omitempty"`
}

func (s *Server) resolveExtractor() *extract.Service {
	if s != nil && s.Extractor != nil {
		return s.Extractor
	}
	return extract.NewService(extract.ConfigFromEnv())
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
