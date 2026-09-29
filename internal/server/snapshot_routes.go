package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"central-memory/internal/store"
)

func (s *Server) registerSnapshotRoutes() {
	s.Mux.HandleFunc("POST /sessions/{id}/snapshot", s.requireAuth(s.handleSnapshotPost))
	s.Mux.HandleFunc("GET /sessions/{id}/snapshot", s.requireAuth(s.handleSnapshotGet))
	s.Mux.HandleFunc("GET /sessions/{id}/snapshot/download", s.requireAuth(s.handleSnapshotDownload))
	s.Mux.HandleFunc("GET /projects/{id}/snapshots", s.requireAuth(s.handleProjectSnapshotsList))
}

type snapshotPostBody struct {
	ProjectID          string `json:"project_id"`
	Harness            string `json:"harness"`
	ConversationID     string `json:"conversation_id"`
	TurnCount          int    `json:"turn_count"`
	GitBranch          string `json:"git_branch"`
	GitCommit          string `json:"git_commit"`
	GitDirty           bool   `json:"git_dirty"`
	UncommittedDiffB64 string `json:"uncommitted_diff_b64"`
	DiffSizeBytes      int    `json:"diff_size_bytes"`
	DiffTruncated      bool   `json:"diff_truncated"`
	TranscriptB64      string `json:"transcript_payload_b64"`
	ArtifactsB64       string `json:"artifacts_bundle_b64"`
	SourceMachineID    string `json:"source_machine_id"`
}

func (s *Server) handleSnapshotPost(w http.ResponseWriter, r *http.Request) {
	ps := provenanceStore(s.Store)
	if ps == nil {
		writeError(w, http.StatusNotImplemented, "provenance store unavailable")
		return
	}
	sessionID := r.PathValue("id")
	var body snapshotPostBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.ProjectID) == "" {
		writeError(w, http.StatusBadRequest, "project_id required")
		return
	}
	if !s.authorizeProject(w, r, body.ProjectID) {
		return
	}
	decodeB64 := func(s string) ([]byte, error) {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, nil
		}
		return base64.StdEncoding.DecodeString(s)
	}
	diff, err := decodeB64(body.UncommittedDiffB64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad uncommitted_diff_b64")
		return
	}
	transcript, err := decodeB64(body.TranscriptB64)
	if err != nil || len(transcript) == 0 {
		writeError(w, http.StatusBadRequest, "transcript_payload_b64 required")
		return
	}
	artifacts, err := decodeB64(body.ArtifactsB64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad artifacts_bundle_b64")
		return
	}
	snap := &store.SessionSnapshot{
		SessionID:         sessionID,
		ProjectID:         body.ProjectID,
		Harness:           firstNonEmptyStr(body.Harness, "antigravity"),
		ConversationID:    firstNonEmptyStr(body.ConversationID, sessionID),
		TurnCount:         body.TurnCount,
		GitBranch:         body.GitBranch,
		GitCommit:         body.GitCommit,
		GitDirty:          body.GitDirty,
		UncommittedDiff:   diff,
		DiffSizeBytes:     body.DiffSizeBytes,
		DiffTruncated:     body.DiffTruncated,
		TranscriptPayload: transcript,
		ArtifactsBundle:   artifacts,
		SourceMachineID:   body.SourceMachineID,
	}
	if err := ps.UpsertSessionSnapshot(r.Context(), snap); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":               true,
		"session_id":       sessionID,
		"snapshot_version": snap.SnapshotVersion,
	})
}

func (s *Server) handleSnapshotGet(w http.ResponseWriter, r *http.Request) {
	ps := provenanceStore(s.Store)
	if ps == nil {
		writeError(w, http.StatusNotImplemented, "provenance store unavailable")
		return
	}
	snap, err := ps.GetLatestSnapshot(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "snapshot not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.authorizeProject(w, r, snap.ProjectID) {
		return
	}
	include := strings.ToLower(r.URL.Query().Get("include"))
	out := map[string]any{
		"id":                snap.ID,
		"session_id":        snap.SessionID,
		"snapshot_version":  snap.SnapshotVersion,
		"project_id":        snap.ProjectID,
		"harness":           snap.Harness,
		"conversation_id":   snap.ConversationID,
		"turn_count":        snap.TurnCount,
		"git_branch":        snap.GitBranch,
		"git_commit":        snap.GitCommit,
		"git_dirty":         snap.GitDirty,
		"diff_size_bytes":   snap.DiffSizeBytes,
		"diff_truncated":    snap.DiffTruncated,
		"source_machine_id": snap.SourceMachineID,
		"created_at":        snap.CreatedAt,
		"updated_at":        snap.UpdatedAt,
	}
	if strings.Contains(include, "transcript") {
		out["transcript_payload_b64"] = base64.StdEncoding.EncodeToString(snap.TranscriptPayload)
	}
	if strings.Contains(include, "diff") {
		out["uncommitted_diff_b64"] = base64.StdEncoding.EncodeToString(snap.UncommittedDiff)
	}
	if strings.Contains(include, "artifacts") {
		out["artifacts_bundle_b64"] = base64.StdEncoding.EncodeToString(snap.ArtifactsBundle)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSnapshotDownload(w http.ResponseWriter, r *http.Request) {
	ps := provenanceStore(s.Store)
	if ps == nil {
		writeError(w, http.StatusNotImplemented, "provenance store unavailable")
		return
	}
	snap, err := ps.GetLatestSnapshot(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "snapshot not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.authorizeProject(w, r, snap.ProjectID) {
		return
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	meta, _ := json.Marshal(map[string]any{
		"session_id": snap.SessionID, "harness": snap.Harness, "turn_count": snap.TurnCount,
		"git_branch": snap.GitBranch, "git_commit": snap.GitCommit, "conversation_id": snap.ConversationID,
	})
	_ = writeTarFile(tw, "meta.json", meta)
	_ = writeTarFile(tw, "transcript.gz", snap.TranscriptPayload)
	_ = writeTarFile(tw, "diff.gz", snap.UncommittedDiff)
	_ = writeTarFile(tw, "artifacts.tar.gz", snap.ArtifactsBundle)
	_ = tw.Close()
	_ = gz.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+snap.SessionID+`.nexus-session.tar.gz"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleProjectSnapshotsList(w http.ResponseWriter, r *http.Request) {
	ps := provenanceStore(s.Store)
	if ps == nil {
		writeError(w, http.StatusNotImplemented, "provenance store unavailable")
		return
	}
	projectID := r.PathValue("id")
	if !s.authorizeProject(w, r, projectID) {
		return
	}
	items, err := ps.ListSnapshotsForProject(r.Context(), projectID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type row struct {
		SessionID       string    `json:"session_id"`
		Harness         string    `json:"harness"`
		TurnCount       int       `json:"turn_count"`
		GitBranch       string    `json:"git_branch,omitempty"`
		SourceMachineID string    `json:"source_machine_id,omitempty"`
		UpdatedAt       time.Time `json:"updated_at"`
		AgeSeconds      int64     `json:"age_seconds"`
	}
	now := time.Now().UTC()
	out := make([]row, 0, len(items))
	for _, it := range items {
		out = append(out, row{
			SessionID: it.SessionID, Harness: it.Harness, TurnCount: it.TurnCount,
			GitBranch: it.GitBranch, SourceMachineID: it.SourceMachineID, UpdatedAt: it.UpdatedAt,
			AgeSeconds: int64(now.Sub(it.UpdatedAt).Seconds()),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "count": len(out)})
}

func writeTarFile(tw *tar.Writer, name string, data []byte) error {
	if data == nil {
		data = []byte{}
	}
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
