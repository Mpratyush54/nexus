package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"central-memory/internal/blobs"
	"central-memory/internal/secrets"
	"central-memory/internal/store"
)

var blobChunkRoutes sync.Map

// maxChunkRequestBytes fits one 16 MiB chunk after base64 expansion, plus JSON.
const maxChunkRequestBytes = 32 << 20

// maxChunkCount caps a manifest so a hostile total cannot allocate a huge index set.
// 10 GiB / 16 MiB is 640 chunks; this is well above that.
const maxChunkCount = 65536

// registerBlobChunkRoutes wires chunk upload, assembly, and secret envelopes
// (D19, D8). Parent sha256 in the URL is the assembly id. For a single-chunk
// object it must equal sha256 of the body. For multiple chunks the client
// sets it to sha256 of the concatenated plaintext; each request still carries
// chunk_sha256 and the server checks sha256(body) == chunk_sha256. The parent
// id is stored alongside the chunk.
func (s *Server) registerBlobChunkRoutes() {
	if s == nil || s.Mux == nil {
		return
	}
	if _, loaded := blobChunkRoutes.LoadOrStore(s.Mux, struct{}{}); loaded {
		return
	}
	s.Mux.HandleFunc("POST /v1/blobs/{sha256}/chunks", s.requireAuth(s.handleBlobChunkPost))
	s.Mux.HandleFunc("POST /v1/blobs/{sha256}/assemble", s.requireAuth(s.handleBlobAssemble))
	s.Mux.HandleFunc("PUT /v1/blobs/{sha256}/secret", s.requireAuth(s.handleSecretEnvelopePut))
}

func (s *Server) handleBlobChunkPost(w http.ResponseWriter, r *http.Request) {
	parent, ok := normalizeSHA256(r.PathValue("sha256"))
	if !ok {
		writeError(w, http.StatusBadRequest, "sha256 must be 64 hex characters")
		return
	}
	var body struct {
		ProjectID   string `json:"project_id"`
		Index       int    `json:"index"`
		Total       int    `json:"total"`
		BodyB64     string `json:"body_b64"`
		ChunkSHA256 string `json:"chunk_sha256"`
	}
	if !decodeBlobJSON(w, r, &body, maxChunkRequestBytes) {
		return
	}
	projectID := strings.TrimSpace(body.ProjectID)
	if !s.authorizeProject(w, r, projectID) {
		return
	}
	if s.rejectIfCaptureOff(w, r, projectID) {
		return
	}
	if body.Total < 1 || body.Total > maxChunkCount || body.Index < 0 || body.Index >= body.Total {
		writeError(w, http.StatusBadRequest, "chunk index out of range")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(body.BodyB64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad body_b64")
		return
	}
	sum := sha256.Sum256(raw)
	got := hex.EncodeToString(sum[:])
	chunkSHA, ok := normalizeSHA256(body.ChunkSHA256)
	if !ok || chunkSHA != got {
		writeError(w, http.StatusBadRequest, "chunk_sha256 does not match body")
		return
	}
	// A one-piece object is addressed by the hash of its only body.
	if body.Total == 1 && parent != got {
		writeError(w, http.StatusBadRequest, "sha256 does not match body")
		return
	}
	if err := store.PutBlobChunk(store.BlobChunk{
		ProjectID:    projectID,
		ParentSHA256: parent,
		Index:        body.Index,
		Total:        body.Total,
		ChunkSHA256:  chunkSHA,
		Body:         raw,
	}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"sha256":       parent,
		"index":        body.Index,
		"total":        body.Total,
		"chunk_sha256": chunkSHA,
		"size":         len(raw),
	})
}

func (s *Server) handleBlobAssemble(w http.ResponseWriter, r *http.Request) {
	parent, ok := normalizeSHA256(r.PathValue("sha256"))
	if !ok {
		writeError(w, http.StatusBadRequest, "sha256 must be 64 hex characters")
		return
	}
	var body struct {
		ProjectID string `json:"project_id"`
	}
	if !decodeBlobJSON(w, r, &body, maxChunkRequestBytes) {
		return
	}
	projectID := strings.TrimSpace(body.ProjectID)
	if !s.authorizeProject(w, r, projectID) {
		return
	}
	if s.rejectIfCaptureOff(w, r, projectID) {
		return
	}
	loaded := store.LoadBlobChunks(projectID, parent)
	if len(loaded) == 0 {
		writeError(w, http.StatusConflict, "missing chunk")
		return
	}
	total := loaded[0].Total
	if total < 1 || total > maxChunkCount {
		writeError(w, http.StatusConflict, "missing chunk")
		return
	}
	seen := make([]bool, total)
	for _, c := range loaded {
		if c.Total != total || c.Index < 0 || c.Index >= total || seen[c.Index] {
			writeError(w, http.StatusConflict, "missing chunk")
			return
		}
		seen[c.Index] = true
	}
	for _, ok := range seen {
		if !ok {
			writeError(w, http.StatusConflict, "missing chunk")
			return
		}
	}
	parts := make([]blobs.Chunk, len(loaded))
	for i, c := range loaded {
		parts[i] = blobs.Chunk{Index: c.Index, Total: c.Total, SHA256: c.ChunkSHA256, Body: c.Body}
	}
	plain, err := blobs.Assemble(parts)
	if err != nil {
		writeError(w, http.StatusConflict, "missing chunk")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sha256":      parent,
		"size":        len(plain),
		"chunk_count": total,
	})
}

func (s *Server) handleSecretEnvelopePut(w http.ResponseWriter, r *http.Request) {
	parent, ok := normalizeSHA256(r.PathValue("sha256"))
	if !ok {
		writeError(w, http.StatusBadRequest, "sha256 must be 64 hex characters")
		return
	}
	var body struct {
		ProjectID string `json:"project_id"`
		KeyB64    string `json:"key_b64"`
		BodyB64   string `json:"body_b64"`
	}
	if !decodeBlobJSON(w, r, &body, maxChunkRequestBytes) {
		return
	}
	projectID := strings.TrimSpace(body.ProjectID)
	if !s.authorizeProject(w, r, projectID) {
		return
	}
	if s.rejectIfCaptureOff(w, r, projectID) {
		return
	}
	key, err := base64.StdEncoding.DecodeString(body.KeyB64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad key_b64")
		return
	}
	if len(key) != 32 {
		writeError(w, http.StatusBadRequest, "key must be 32 bytes")
		return
	}
	plain, err := base64.StdEncoding.DecodeString(body.BodyB64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad body_b64")
		return
	}
	// Plaintext is encrypted before it is stored and is not logged.
	ct, err := secrets.Encrypt(key, plain)
	for i := range plain {
		plain[i] = 0
	}
	for i := range key {
		key[i] = 0
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not encrypt secret")
		return
	}
	store.PutSecretEnvelope(projectID, parent, secrets.BlobKind, ct)
	writeJSON(w, http.StatusCreated, map[string]any{
		"sha256": parent,
		"kind":   secrets.BlobKind,
		"size":   len(ct),
	})
}

func normalizeSHA256(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", false
	}
	return s, true
}

func decodeBlobJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "request body is required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: trailing data after JSON value")
		return false
	}
	return true
}
