package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"central-memory/internal/secrets"
	"central-memory/internal/store"
)

var blobChunkRoutes sync.Map

// maxChunkRequestBytes fits one piece of up to ~6 MiB after base64
// expansion, plus JSON. Clients send 4 MiB (daemon) and 1 MiB (teleport)
// pieces; a request this size costs about three times it while decoded.
const maxChunkRequestBytes = 8 << 20

// chunkSlots bounds concurrent piece uploads (each held while decoded).
var chunkSlots = make(chan struct{}, 4)

// maxAssembledBytes caps a file assembled from pieces. Assembly holds the
// file in memory and stores it as one row, so this bounds what one upload
// can cost the server. A variable so tests can use a small value.
var maxAssembledBytes int64 = 32 << 20

// assembleSlots bounds concurrent assemblies (each holds up to
// maxAssembledBytes).
var assembleSlots = make(chan struct{}, 2)

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
	select {
	case chunkSlots <- struct{}{}:
		defer func() { <-chunkSlots }()
	default:
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusTooManyRequests, "too many uploads in progress; retry shortly")
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
	// Refuse an upload that cannot be assembled before decoding or storing
	// it: every piece but the last is full size, so total pieces of this
	// size is the file's size (within one piece).
	pieceBytes := int64(base64.StdEncoding.DecodedLen(len(body.BodyB64)))
	if int64(body.Total) > maxAssembledBytes ||
		(body.Index < body.Total-1 && pieceBytes*int64(body.Total-1) > maxAssembledBytes) ||
		store.BlobChunkSetBytesExcept(strings.TrimSpace(body.ProjectID), parent, body.Index)+pieceBytes > maxAssembledBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file is larger than the upload limit")
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
		Owner:        authSubject(r),
		Index:        body.Index,
		Total:        body.Total,
		ChunkSHA256:  chunkSHA,
		Body:         raw,
	}); err != nil {
		switch {
		case errors.Is(err, store.ErrChunkSetTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		case errors.Is(err, store.ErrChunkQuota):
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
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
		// Purpose is what the assembled blob is: "file" (the default,
		// counted against the plan's storage) or "transcript" (a session
		// version's transcript, stored like a plain PUT of one).
		Purpose string `json:"purpose"`
	}
	if !decodeBlobJSON(w, r, &body, 64<<10) {
		return
	}
	purpose := strings.TrimSpace(body.Purpose)
	if purpose == "" {
		purpose = "file"
	}
	if purpose != "file" && purpose != "transcript" {
		writeError(w, http.StatusBadRequest, "purpose must be file or transcript")
		return
	}
	projectID := strings.TrimSpace(body.ProjectID)
	if !s.authorizeProject(w, r, projectID) {
		return
	}
	if s.rejectIfCaptureOff(w, r, projectID) {
		return
	}
	// One assembly at a time per slot: each holds the whole file once.
	select {
	case assembleSlots <- struct{}{}:
		defer func() { <-assembleSlots }()
	default:
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusTooManyRequests, "too many files being assembled; retry shortly")
		return
	}
	plain, total, err := store.AssembleBlobChunks(projectID, parent, maxAssembledBytes)
	switch {
	case errors.Is(err, store.ErrAssembleTooLarge):
		store.DeleteBlobChunks(projectID, parent)
		writeError(w, http.StatusRequestEntityTooLarge, "file is larger than the upload limit")
		return
	case errors.Is(err, store.ErrAssembleSHA):
		writeError(w, http.StatusBadRequest, "sha256 does not match the assembled file")
		return
	case err != nil:
		writeError(w, http.StatusConflict, "missing chunk")
		return
	}
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "blob storage unavailable")
		return
	}
	if err := cs.PutBlob(r.Context(), projectID, parent, "plain", purpose, plain); err != nil {
		if errors.Is(err, store.ErrStorageFull) {
			writeError(w, http.StatusPaymentRequired, "storage full")
			return
		}
		writeError(w, http.StatusBadRequest, "could not store assembled file: "+err.Error())
		return
	}
	// Stored: the pieces are no longer needed in memory.
	store.DeleteBlobChunks(projectID, parent)
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
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is larger than the limit")
			return false
		}
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

// decodeLimitedJSON decodes a request body of at most limit bytes: 413 over
// it, 400 for invalid JSON. For routes that do not use decodeJSON's 2 MB cap.
func decodeLimitedJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is larger than the limit")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}
