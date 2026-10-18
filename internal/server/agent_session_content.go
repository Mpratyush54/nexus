package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"central-memory/internal/store"
)

// The mirrored agent session and the historical capture originally used
// different IDs. Read both until old provenance has been migrated. New capture
// writes provenance under the agent-session ID.
func sessionContentIDs(row *store.AgentSession) []string {
	ids := []string{row.ID}
	if row.NativeID != "" && row.NativeID != row.ID {
		ids = append(ids, row.NativeID)
	}
	return ids
}

func uuidLikeSessionID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil
}

func (s *Server) sessionOperations(r *http.Request, row *store.AgentSession) ([]store.FileOperation, []store.ToolExecution, error) {
	ps := provenanceStore(s.Store)
	if ps == nil {
		return nil, nil, nil
	}
	files := []store.FileOperation{}
	tools := []store.ToolExecution{}
	for _, id := range sessionContentIDs(row) {
		if !uuidLikeSessionID(id) {
			continue
		}
		ops, err := ps.ListFileOperations(r.Context(), id, store.FileOpListOpts{})
		if err != nil {
			return nil, nil, err
		}
		for _, op := range ops {
			if op.ProjectID == row.ProjectID {
				files = append(files, op)
			}
		}
		exec, err := ps.ListToolExecutions(r.Context(), id)
		if err != nil {
			return nil, nil, err
		}
		for _, item := range exec {
			if item.ProjectID == row.ProjectID {
				tools = append(tools, item)
			}
		}
	}
	return files, tools, nil
}

func (s *Server) handleAgentSessionFiles(w http.ResponseWriter, r *http.Request) {
	row, ok := s.requireAgentRead(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	fileOps, _, err := s.sessionOperations(r, row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load session files")
		return
	}
	byPath := map[string]map[string]bool{}
	for _, op := range fileOps {
		path := strings.TrimSpace(op.FilePath)
		if path == "" {
			continue
		}
		if byPath[path] == nil {
			byPath[path] = map[string]bool{}
		}
		byPath[path][op.OpType] = true
	}
	if len(byPath) == 0 {
		for _, call := range s.sessionToolCalls(r, row) {
			path := toolCallValue(call, "file_path", "target_file", "path")
			if path == "" {
				continue
			}
			if byPath[path] == nil {
				byPath[path] = map[string]bool{}
			}
			byPath[path][toolCallValue(call, "name")] = true
		}
	}
	type fileRow struct {
		Path       string   `json:"path"`
		Operations []string `json:"operations"`
	}
	items := make([]fileRow, 0, len(byPath))
	for path, kinds := range byPath {
		operations := make([]string, 0, len(kinds))
		for kind := range kinds {
			if kind != "" {
				operations = append(operations, kind)
			}
		}
		sort.Strings(operations)
		items = append(items, fileRow{Path: path, Operations: operations})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleAgentSessionOperations(w http.ResponseWriter, r *http.Request) {
	row, ok := s.requireAgentRead(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	_, tools, err := s.sessionOperations(r, row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load session commands")
		return
	}
	type commandRow struct {
		Tool      string `json:"tool"`
		Command   string `json:"command"`
		Directory string `json:"directory,omitempty"`
	}
	items := make([]commandRow, 0, len(tools))
	for _, item := range tools {
		if item.CommandLine != "" {
			items = append(items, commandRow{Tool: item.ToolName, Command: item.CommandLine, Directory: item.WorkingDirectory})
		}
	}
	if len(items) == 0 {
		for _, call := range s.sessionToolCalls(r, row) {
			if command := toolCallValue(call, "command", "cmd"); command != "" {
				items = append(items, commandRow{Tool: toolCallValue(call, "name"), Command: command})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) sessionToolCalls(r *http.Request, row *store.AgentSession) []map[string]any {
	cs, ok := s.agentCloud()
	if !ok {
		return nil
	}
	turns, err := cs.ListSessionTurns(r.Context(), row.ID)
	if err != nil {
		return nil
	}
	out := []map[string]any{}
	for _, turn := range turns {
		var calls []map[string]any
		if json.Unmarshal(turn.ToolCalls, &calls) == nil {
			out = append(out, calls...)
		}
		// Some historical imports serialized the turn envelope into the chat
		// preview instead of populating session_turns.tool_calls. Recover only
		// structured calls from a valid JSON envelope; never infer from prose.
		if len(calls) == 0 && strings.HasPrefix(strings.TrimSpace(turn.TextPreview), "{") {
			var envelope struct {
				ToolCalls []map[string]any `json:"tool_calls"`
			}
			if json.Unmarshal([]byte(turn.TextPreview), &envelope) == nil {
				out = append(out, envelope.ToolCalls...)
			}
		}
	}
	return out
}

func toolCallValue(call map[string]any, keys ...string) string {
	for _, m := range []map[string]any{call, nestedToolMap(call, "args"), nestedToolMap(call, "input")} {
		for _, key := range keys {
			if value, ok := m[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func nestedToolMap(call map[string]any, key string) map[string]any {
	if m, ok := call[key].(map[string]any); ok {
		return m
	}
	return nil
}

func (s *Server) handleAgentSessionMemories(w http.ResponseWriter, r *http.Request) {
	row, ok := s.requireAgentRead(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	ids := sessionContentIDs(row)
	viewerCtx := store.WithViewer(r.Context(), authSubject(r))
	// The direct query preserves visibility rules without truncating a session
	// behind thousands of unrelated project memories.
	lister, ok := s.Store.(interface {
		ListSessionMemories(context.Context, string, []string) ([]*store.MemoryItem, error)
	})
	var found []*store.MemoryItem
	var err error
	if ok {
		found, err = lister.ListSessionMemories(viewerCtx, row.ProjectID, ids)
	} else {
		found, err = s.Store.SearchMemory(viewerCtx, row.ProjectID, "", nil, 10000)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load session memories")
		return
	}
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	items := []knowledgeItem{}
	for _, item := range found {
		if item == nil || !storeMemoryBelongsToSession(item, idSet) {
			continue
		}
		items = append(items, knowledgeItem{ID: item.ID, Key: item.Key, Content: item.Content, Level: item.Level, Scope: item.Scope, Status: publicMemoryStatus(item.Status), Confidence: item.Confidence, Tags: item.Tags, Source: item.Source})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func storeMemoryBelongsToSession(item *store.MemoryItem, ids map[string]bool) bool {
	if item == nil {
		return false
	}
	if ids[item.SessionID] {
		return true
	}
	for id := range ids {
		if item.Key == "session/"+id {
			return true
		}
	}
	for _, tag := range item.Tags {
		if strings.HasPrefix(tag, "session:") && ids[strings.TrimPrefix(tag, "session:")] {
			return true
		}
	}
	return false
}
