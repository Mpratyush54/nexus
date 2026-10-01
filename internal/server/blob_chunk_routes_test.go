package server

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"testing"

	"central-memory/internal/secrets"
	"central-memory/internal/store"
)

func newBlobServer(t *testing.T) *Server {
	t.Helper()
	store.ResetBlobChunks()
	s := newTestServer()
	s.registerBlobChunkRoutes()
	return s
}

func claimFolder(t *testing.T, s *Server, folder, owner string) string {
	t.Helper()
	mem := s.Store.(*store.MemStore)
	proj, err := mem.ResolveProject(t.Context(), "", "", folder)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(t.Context(), proj.ID, owner); err != nil {
		t.Fatal(err)
	}
	return proj.ID
}

func TestBlobChunkUploadAssembleAuthAndCapture(t *testing.T) {
	s := newBlobServer(t)
	projectID := claimFolder(t, s, "blob-chunks", "owner")
	tok := loginAs(t, s, "owner")
	stranger := loginAs(t, s, "stranger")

	part1 := []byte("aaaa")
	part2 := []byte("bbbb")
	whole := append(append([]byte{}, part1...), part2...)
	parent := shaHex(whole)

	rec := doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/chunks", "", map[string]any{
		"project_id": projectID, "index": 0, "total": 2,
		"body_b64": base64.StdEncoding.EncodeToString(part1), "chunk_sha256": shaHex(part1),
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/chunks", stranger, map[string]any{
		"project_id": projectID, "index": 0, "total": 2,
		"body_b64": base64.StdEncoding.EncodeToString(part1), "chunk_sha256": shaHex(part1),
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("stranger: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/chunks", tok, map[string]any{
		"project_id": projectID, "index": 0, "total": 2,
		"body_b64": base64.StdEncoding.EncodeToString(part1), "chunk_sha256": shaHex([]byte("nope")),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad chunk hash: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/chunks", tok, map[string]any{
		"project_id": projectID, "index": 0, "total": 2,
		"body_b64": base64.StdEncoding.EncodeToString(part1), "chunk_sha256": shaHex(part1),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("chunk 0: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/assemble", tok, map[string]any{
		"project_id": projectID,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("missing index: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/chunks", tok, map[string]any{
		"project_id": projectID, "index": 1, "total": 2,
		"body_b64": base64.StdEncoding.EncodeToString(part2), "chunk_sha256": shaHex(part2),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("chunk 1: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/assemble", tok, map[string]any{
		"project_id": projectID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("assemble: %d %s", rec.Code, rec.Body.String())
	}
	var assembled struct {
		Size       int `json:"size"`
		ChunkCount int `json:"chunk_count"`
	}
	decodeBody(t, rec, &assembled)
	if assembled.Size != len(whole) || assembled.ChunkCount != 2 {
		t.Fatalf("assembled %+v", assembled)
	}

	only := []byte("only-chunk")
	onlySum := shaHex(only)
	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/"+shaHex([]byte("other"))+"/chunks", tok, map[string]any{
		"project_id": projectID, "index": 0, "total": 1,
		"body_b64": base64.StdEncoding.EncodeToString(only), "chunk_sha256": onlySum,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("single-chunk path mismatch: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/"+onlySum+"/chunks", tok, map[string]any{
		"project_id": projectID, "index": 0, "total": 1,
		"body_b64": base64.StdEncoding.EncodeToString(only), "chunk_sha256": onlySum,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("single chunk: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/"+onlySum+"/assemble", tok, map[string]any{
		"project_id": projectID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("single assemble: %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &assembled)
	if assembled.Size != len(only) || assembled.ChunkCount != 1 {
		t.Fatalf("single assembled %+v", assembled)
	}

	mem := s.Store.(*store.MemStore)
	if err := mem.SetCaptureEnabled(t.Context(), projectID, false, "owner"); err != nil {
		t.Fatal(err)
	}
	offParent := shaHex([]byte("capture-off-body"))
	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/"+offParent+"/chunks", tok, map[string]any{
		"project_id": projectID, "index": 0, "total": 1,
		"body_b64": base64.StdEncoding.EncodeToString([]byte("capture-off-body")), "chunk_sha256": offParent,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("capture off chunk: %d %s", rec.Code, rec.Body.String())
	}
	if got := store.LoadBlobChunks(projectID, offParent); len(got) != 0 {
		t.Fatalf("stored while capture off: %+v", got)
	}
}

func TestSecretEnvelopeStoresCiphertext(t *testing.T) {
	s := newBlobServer(t)
	projectID := claimFolder(t, s, "secret-envelope", "owner")
	tok := loginAs(t, s, "owner")

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	plain := []byte("STRIPE_KEY=super-secret-plaintext-value")
	sum := shaHex(plain)

	rec := doJSON(t, s, http.MethodPut, "/v1/blobs/"+sum+"/secret", tok, map[string]any{
		"project_id": projectID,
		"key_b64":    base64.StdEncoding.EncodeToString(key[:16]),
		"body_b64":   base64.StdEncoding.EncodeToString(plain),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short key: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPut, "/v1/blobs/"+sum+"/secret", tok, map[string]any{
		"project_id": projectID,
		"key_b64":    base64.StdEncoding.EncodeToString(key),
		"body_b64":   base64.StdEncoding.EncodeToString(plain),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("put secret: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Kind string `json:"kind"`
		Size int    `json:"size"`
	}
	decodeBody(t, rec, &created)
	if created.Kind != secrets.BlobKind {
		t.Fatalf("kind %+v", created)
	}
	if secrets.BlobKind != "secret_envelope" {
		t.Fatalf("blob kind %s", secrets.BlobKind)
	}
	if bytes.Contains(rec.Body.Bytes(), plain) {
		t.Fatal("response contains plaintext")
	}
	ct, kind, ok := store.LoadSecretEnvelope(projectID, sum)
	if !ok || kind != secrets.BlobKind {
		t.Fatalf("stored kind=%s ok=%v", kind, ok)
	}
	if bytes.Equal(ct, plain) || bytes.Contains(ct, plain) {
		t.Fatal("stored bytes are plaintext")
	}
	opened, err := secrets.Open(key, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, plain) {
		t.Fatalf("opened %q", opened)
	}
	if created.Size != len(ct) {
		t.Fatalf("size %d stored %d", created.Size, len(ct))
	}

	mem := s.Store.(*store.MemStore)
	if err := mem.SetCaptureEnabled(t.Context(), projectID, false, "owner"); err != nil {
		t.Fatal(err)
	}
	other := shaHex([]byte("another-secret-value"))
	rec = doJSON(t, s, http.MethodPut, "/v1/blobs/"+other+"/secret", tok, map[string]any{
		"project_id": projectID,
		"key_b64":    base64.StdEncoding.EncodeToString(key),
		"body_b64":   base64.StdEncoding.EncodeToString([]byte("another-secret-value")),
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("capture off secret: %d %s", rec.Code, rec.Body.String())
	}
	if _, _, ok := store.LoadSecretEnvelope(projectID, other); ok {
		t.Fatal("secret stored while capture is off")
	}
}
