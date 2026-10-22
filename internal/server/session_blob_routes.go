package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"central-memory/internal/blobs"
	"central-memory/internal/store"
)

type scopedBlobReader interface {
	ReadBlob(context.Context, string, string) ([]byte, error)
}

// handleSessionVersionBlob serves only file blobs named by a complete
// version the caller can see. A project membership alone cannot download a
// private session's code.
func (s *Server) handleSessionVersionBlob(w http.ResponseWriter, r *http.Request) {
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "version must be a positive integer")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	row, ok := s.requireAgentRead(w, r, id)
	if !ok {
		return
	}
	ver, ok := s.requireVisibleVersion(w, r, id, version)
	if !ok {
		return
	}
	hash := blobs.NormalizeSHA256(r.PathValue("sha256"))
	if len(hash) != 64 || ver.State != "complete" {
		writeError(w, http.StatusNotFound, "file not found in a complete version")
		return
	}
	var manifest struct {
		Files []struct {
			Blob string `json:"blob"`
		} `json:"files"`
	}
	if json.Unmarshal(ver.Manifest, &manifest) != nil {
		writeError(w, http.StatusBadRequest, "invalid version manifest")
		return
	}
	allowed := false
	for _, file := range manifest.Files {
		if blobs.NormalizeSHA256(file.Blob) == hash {
			allowed = true
			break
		}
	}
	if !allowed {
		writeError(w, http.StatusNotFound, "file not in this version")
		return
	}
	reader, ok := s.Store.(scopedBlobReader)
	if !ok {
		writeError(w, http.StatusNotImplemented, "file download unavailable")
		return
	}
	body, err := reader.ReadBlob(r.Context(), row.ProjectID, hash)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "file blob unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read file blob")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}
