package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"central-memory/internal/store"
)

func (s *Server) registerProvenanceRoutes() {
	s.Mux.HandleFunc("POST /sessions/{id}/operations", s.requireAuth(s.handleSessionOperationsPost))
	s.Mux.HandleFunc("GET /sessions/{id}/operations", s.requireAuth(s.handleSessionOperationsGet))
	s.Mux.HandleFunc("GET /sessions/{id}/files", s.requireAuth(s.handleSessionFilesGet))
}

func provenanceStore(st store.Store) store.ProvenanceStore {
	ps, _ := st.(store.ProvenanceStore)
	return ps
}

type operationsBatch struct {
	ProjectID      string                `json:"project_id"`
	Harness        string                `json:"harness"`
	FileOperations []store.FileOperation `json:"file_operations"`
	ToolExecutions []store.ToolExecution `json:"tool_executions"`
}

func (s *Server) handleSessionOperationsPost(w http.ResponseWriter, r *http.Request) {
	ps := provenanceStore(s.Store)
	if ps == nil {
		writeError(w, http.StatusNotImplemented, "provenance store unavailable")
		return
	}
	sessionID := r.PathValue("id")
	userID := authSubject(r)
	var body operationsBatch
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.ProjectID) == "" {
		writeError(w, http.StatusBadRequest, "project_id required")
		return
	}
	ok, err := s.Store.IsProjectMember(r.Context(), userID, body.ProjectID)
	if err != nil || !ok {
		writeError(w, http.StatusForbidden, "not a project member")
		return
	}
	if s.rejectIfCaptureOff(w, r, body.ProjectID) {
		return
	}
	if _, ok := s.claimSessionWrite(w, r, body.ProjectID, sessionID); !ok {
		return
	}
	harness := strings.TrimSpace(body.Harness)
	if harness == "" {
		harness = "unknown"
	}
	for i := range body.FileOperations {
		op := body.FileOperations[i]
		op.SessionID = sessionID
		op.ProjectID = body.ProjectID
		if op.Harness == "" {
			op.Harness = harness
		}
		if err := ps.InsertFileOperation(r.Context(), op); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	for i := range body.ToolExecutions {
		te := body.ToolExecutions[i]
		te.SessionID = sessionID
		te.ProjectID = body.ProjectID
		if te.Harness == "" {
			te.Harness = harness
		}
		if err := ps.InsertToolExecution(r.Context(), te); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":              true,
		"file_ops":        len(body.FileOperations),
		"tool_executions": len(body.ToolExecutions),
	})
}

func (s *Server) handleSessionOperationsGet(w http.ResponseWriter, r *http.Request) {
	ps := provenanceStore(s.Store)
	if ps == nil {
		writeError(w, http.StatusNotImplemented, "provenance store unavailable")
		return
	}
	sessionID := r.PathValue("id")
	if !s.authorizeProvenanceSession(w, r, sessionID) {
		return
	}
	opts := store.FileOpListOpts{
		OpType:   strings.TrimSpace(r.URL.Query().Get("op_type")),
		FilePath: strings.TrimSpace(r.URL.Query().Get("file_path")),
	}
	ops, err := ps.ListFileOperations(r.Context(), sessionID, opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tex, err := ps.ListToolExecutions(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"file_operations": ops,
		"tool_executions": tex,
	})
}

func (s *Server) handleSessionFilesGet(w http.ResponseWriter, r *http.Request) {
	ps := provenanceStore(s.Store)
	if ps == nil {
		writeError(w, http.StatusNotImplemented, "provenance store unavailable")
		return
	}
	if !s.authorizeProvenanceSession(w, r, r.PathValue("id")) {
		return
	}
	ops, err := ps.ListFileOperations(r.Context(), r.PathValue("id"), store.FileOpListOpts{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type fileRow struct {
		Path    string   `json:"path"`
		OpTypes []string `json:"op_types"`
	}
	byPath := map[string]map[string]bool{}
	for _, op := range ops {
		if byPath[op.FilePath] == nil {
			byPath[op.FilePath] = map[string]bool{}
		}
		byPath[op.FilePath][op.OpType] = true
	}
	files := make([]fileRow, 0, len(byPath))
	for path, types := range byPath {
		row := fileRow{Path: path}
		for t := range types {
			row.OpTypes = append(row.OpTypes, t)
		}
		files = append(files, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files, "count": len(files)})
}
