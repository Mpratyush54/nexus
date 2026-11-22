package mcp

import (
	"context"
	"strings"
)

// ProjectBriefKey mirrors store.ProjectBriefKey (this package never imports
// the store): the key of the pinned per-project brief memory.
const ProjectBriefKey = "project/brief"

// BriefProvider is implemented by stores that can return the project brief
// (the cloud adapter). Stores without it simply serve no brief.
type BriefProvider interface {
	ProjectBrief(ctx context.Context, projectID string) (string, error)
}

// FileMemorySearcher is the optional store fast path for memory_search's
// file argument. Stores without it fall back to filtering top memories.
type FileMemorySearcher interface {
	SearchMemoryByFile(ctx context.Context, projectID, path string, limit int) ([]*MemoryItem, error)
}

// InstructionsIntro opens the MCP initialize instructions.
const InstructionsIntro = "Nexus shared memory is available for this project. " +
	"Call memory_search before changing unfamiliar code (pass file=<path> for memories about a file), " +
	"project_knowledge for an overview, and memory_write to record decisions. " +
	"Items carry date and source (session:<id>, fetch it with session_summary_get); " +
	"items marked stale=\"true\" refer to files changed since: verify before relying on them."

// projectBrief returns the brief for projectID, or "" when the store has
// none or cannot provide one.
func (s *Server) projectBrief(ctx context.Context, projectID string) string {
	if s == nil || s.store == nil || strings.TrimSpace(projectID) == "" {
		return ""
	}
	bp, ok := s.store.(BriefProvider)
	if !ok {
		return ""
	}
	brief, err := bp.ProjectBrief(ctx, projectID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(brief)
}

// instructions is the initialize "instructions" text: the intro paragraph
// plus the project brief when there is one.
func (s *Server) instructions(ctx context.Context) string {
	out := InstructionsIntro
	if brief := s.projectBrief(ctx, s.cfg.ProjectID); brief != "" {
		out += "\n\nProject brief:\n" + brief
	}
	return out
}

// normalizeFilePath mirrors store.NormalizeFilePath.
func normalizeFilePath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, `\`, "/"))
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return strings.ToLower(strings.TrimSuffix(p, "/"))
}

// filePathMatches mirrors store.FilePathMatches: equal after normalising,
// or one is a suffix of the other at a path-segment boundary.
func filePathMatches(a, b string) bool {
	a, b = normalizeFilePath(a), normalizeFilePath(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) < len(b) {
		a, b = b, a
	}
	return strings.HasSuffix(a, "/"+b)
}

func memoryTouchesFile(it *MemoryItem, path string) bool {
	if it == nil {
		return false
	}
	for _, f := range it.FilesAffected {
		if filePathMatches(f, path) {
			return true
		}
	}
	return false
}

func memoryHasAnyTag(it *MemoryItem, tags []string) bool {
	if len(tags) == 0 {
		return true
	}
	for _, want := range tags {
		for _, t := range it.Tags {
			if t == want {
				return true
			}
		}
	}
	return false
}

// searchByFile returns memories whose files_affected match path.
func (s *Server) searchByFile(ctx context.Context, projectID, path string, tags []string, limit int) ([]*MemoryItem, error) {
	var found []*MemoryItem
	var err error
	if fs, ok := s.store.(FileMemorySearcher); ok {
		found, err = fs.SearchMemoryByFile(ctx, projectID, path, limit*2)
	} else {
		found, err = s.store.SearchMemory(ctx, projectID, "", tags, 200)
	}
	if err != nil {
		return nil, err
	}
	out := make([]*MemoryItem, 0, limit)
	for _, it := range found {
		if !memoryTouchesFile(it, path) || !memoryHasAnyTag(it, tags) {
			continue
		}
		out = append(out, it)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func memoryIdentity(it *MemoryItem) string {
	if it.ID != "" {
		return it.ID
	}
	return it.Key + "\x00" + it.Content
}

// mergeAhead returns first followed by rest, without duplicates, capped.
func mergeAhead(first, rest []*MemoryItem, limit int) []*MemoryItem {
	seen := map[string]bool{}
	out := make([]*MemoryItem, 0, len(first)+len(rest))
	for _, list := range [][]*MemoryItem{first, rest} {
		for _, it := range list {
			if it == nil || seen[memoryIdentity(it)] {
				continue
			}
			seen[memoryIdentity(it)] = true
			out = append(out, it)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// withoutKey drops items with key.
func withoutKey(items []*MemoryItem, key string) []*MemoryItem {
	out := items[:0:0]
	for _, it := range items {
		if it != nil && it.Key == key {
			continue
		}
		out = append(out, it)
	}
	return out
}
