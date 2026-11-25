package server

import (
	"encoding/base64"
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func postPiece(t *testing.T, s *Server, tok, projectID, parent string, index, total int, body []byte) int {
	t.Helper()
	rec := doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/chunks", tok, map[string]any{
		"project_id": projectID, "index": index, "total": total,
		"body_b64": base64.StdEncoding.EncodeToString(body), "chunk_sha256": shaHex(body),
	})
	return rec.Code
}

// Assembly stores the declared purpose: a transcript is not counted against
// the plan's storage (as with a plain PUT of one); a file is. Pieces are
// freed once assembled.
func TestBlobAssemblePurposeAndFreesPieces(t *testing.T) {
	s := newBlobServer(t)
	projectID := claimFolder(t, s, "blob-purpose", "owner")
	tok := loginAs(t, s, "owner")
	mem := s.Store.(*store.MemStore)
	// A plan with no room left for files.
	if err := mem.SetStorageUsage(t.Context(), "user:owner", 1, 1); err != nil {
		t.Fatal(err)
	}
	part1, part2 := []byte("transcript line one\n"), []byte("transcript line two\n")
	whole := append(append([]byte{}, part1...), part2...)
	parent := shaHex(whole)
	for i, p := range [][]byte{part1, part2} {
		if code := postPiece(t, s, tok, projectID, parent, i, 2, p); code != http.StatusCreated {
			t.Fatalf("piece %d: %d", i, code)
		}
	}
	rec := doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/assemble", tok, map[string]any{"project_id": projectID})
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("file assembly on a full plan: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/assemble", tok, map[string]any{"project_id": projectID, "purpose": "bogus"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad purpose: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/assemble", tok, map[string]any{"project_id": projectID, "purpose": "transcript"})
	if rec.Code != http.StatusOK {
		t.Fatalf("transcript assembly: %d %s", rec.Code, rec.Body.String())
	}
	if got, err := mem.ReadBlob(t.Context(), projectID, parent); err != nil || string(got) != string(whole) {
		t.Fatalf("stored blob = %q, %v", got, err)
	}
	if used, _, _ := mem.StorageUsage(t.Context(), "user:owner"); used != 1 {
		t.Fatalf("transcript counted against storage: used = %d", used)
	}
	if len(store.LoadBlobChunks(projectID, parent)) != 0 {
		t.Fatal("pieces kept after assembly")
	}
	if total, _ := store.BlobChunkBytes("owner"); total != 0 {
		t.Fatalf("%d piece bytes still held", total)
	}
}

func TestBlobChunkMemoryLimitsReturn413And429(t *testing.T) {
	s := newBlobServer(t)
	oldUser, oldTotal := store.MaxChunkBytesPerUser, store.MaxChunkBytesTotal
	store.MaxChunkBytesPerUser, store.MaxChunkBytesTotal = 64, 1<<20
	t.Cleanup(func() { store.MaxChunkBytesPerUser, store.MaxChunkBytesTotal = oldUser, oldTotal })
	projectID := claimFolder(t, s, "blob-limits", "owner")
	tok := loginAs(t, s, "owner")

	big := make([]byte, 65)
	if code := postPiece(t, s, tok, projectID, shaHex(big), 0, 1, big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("upload over the limit on its own: %d, want 413", code)
	}
	first := make([]byte, 40)
	if code := postPiece(t, s, tok, projectID, shaHex([]byte("a")), 0, 2, first); code != http.StatusCreated {
		t.Fatalf("first upload: %d", code)
	}
	second := []byte("0123456789012345678901234567890123456789")
	rec := doJSON(t, s, http.MethodPost, "/v1/blobs/"+shaHex(second)+"/chunks", tok, map[string]any{
		"project_id": projectID, "index": 0, "total": 1,
		"body_b64": base64.StdEncoding.EncodeToString(second), "chunk_sha256": shaHex(second),
	})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("second upload while the first holds the quota: %d, want 429 with Retry-After", rec.Code)
	}
}

// Assembled size is capped: an upload declared over the cap is refused at
// its first piece, before anything is decoded into memory or stored, and
// assembly refuses pieces that add up to more.
func TestBlobAssembledSizeCap(t *testing.T) {
	s := newBlobServer(t)
	old := maxAssembledBytes
	maxAssembledBytes = 100
	t.Cleanup(func() { maxAssembledBytes = old })
	projectID := claimFolder(t, s, "blob-size-cap", "owner")
	tok := loginAs(t, s, "owner")

	piece := make([]byte, 40)
	// 4 pieces of 40 bytes declared: over 100 before the first is stored.
	if code := postPiece(t, s, tok, projectID, shaHex([]byte("big")), 0, 4, piece); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared over the cap: %d, want 413", code)
	}
	if total, _ := store.BlobChunkBytes("owner"); total != 0 {
		t.Fatalf("%d bytes stored for a refused upload", total)
	}
	// Uneven pieces that only add up to too much: refused as they arrive.
	parent := shaHex([]byte("uneven"))
	if code := postPiece(t, s, tok, projectID, parent, 2, 3, make([]byte, 60)); code != http.StatusCreated {
		t.Fatalf("last piece: %d", code)
	}
	if code := postPiece(t, s, tok, projectID, parent, 1, 3, make([]byte, 10)); code != http.StatusCreated {
		t.Fatalf("small piece: %d", code)
	}
	if code := postPiece(t, s, tok, projectID, parent, 0, 3, make([]byte, 10)); code != http.StatusCreated {
		t.Fatalf("small piece: %d", code)
	}
	if code := postPiece(t, s, tok, projectID, shaHex([]byte("x")), 1, 2, make([]byte, 101)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("one piece over the cap: %d, want 413", code)
	}
	// Assembly checks the sha256 of the joined pieces.
	rec := doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/assemble", tok, map[string]any{"project_id": projectID, "purpose": "transcript"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("pieces not matching the sha: %d %s", rec.Code, rec.Body.String())
	}
}

func TestBlobAssembleConcurrencyIsBounded(t *testing.T) {
	s := newBlobServer(t)
	projectID := claimFolder(t, s, "blob-slots", "owner")
	tok := loginAs(t, s, "owner")
	for i := 0; i < cap(assembleSlots); i++ {
		assembleSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for len(assembleSlots) > 0 {
			<-assembleSlots
		}
	})
	body := []byte("one piece")
	parent := shaHex(body)
	if code := postPiece(t, s, tok, projectID, parent, 0, 1, body); code != http.StatusCreated {
		t.Fatalf("piece: %d", code)
	}
	rec := doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/assemble", tok, map[string]any{"project_id": projectID})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("assembly with every slot busy: %d, want 429", rec.Code)
	}
}

// Legacy snapshot and operations posts are size-capped (413), not decoded
// without limit.
func TestSnapshotAndOperationsBodyLimits(t *testing.T) {
	s := newBlobServer(t)
	old := maxSnapshotPostBytes
	maxSnapshotPostBytes = 1 << 10
	t.Cleanup(func() { maxSnapshotPostBytes = old })
	projectID := claimFolder(t, s, "body-limits", "owner")
	tok := loginAs(t, s, "owner")
	const id = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	big := base64.StdEncoding.EncodeToString(make([]byte, 2<<10))
	rec := doJSON(t, s, http.MethodPost, "/sessions/"+id+"/snapshot", tok, map[string]any{
		"project_id": projectID, "harness": "claude", "transcript_payload_b64": big,
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized snapshot post: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/sessions/"+id+"/snapshot", tok, map[string]any{
		"project_id": projectID, "harness": "claude", "transcript_payload_b64": "eA==",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("small snapshot post: %d %s", rec.Code, rec.Body.String())
	}
	ops := make([]map[string]any, 0, 30000)
	for i := 0; i < 30000; i++ {
		ops = append(ops, map[string]any{"tool_name": "edit", "file_path": "some/long/path/to/a/file.go", "op_type": "edit"})
	}
	rec = doJSON(t, s, http.MethodPost, "/sessions/"+id+"/operations", tok, map[string]any{
		"project_id": projectID, "harness": "claude", "file_operations": ops,
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized operations post: %d", rec.Code)
	}
}

func TestChunkRequestCapAndConcurrency(t *testing.T) {
	s := newBlobServer(t)
	projectID := claimFolder(t, s, "chunk-limits", "owner")
	tok := loginAs(t, s, "owner")
	if maxChunkRequestBytes != 8<<20 {
		t.Fatalf("maxChunkRequestBytes = %d, want 8 MiB", maxChunkRequestBytes)
	}
	big := make([]byte, 7<<20) // base64 ~9.3 MiB: over the request cap
	if code := postPiece(t, s, tok, projectID, shaHex(big), 0, 1, big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("piece request over 8 MiB: %d, want 413", code)
	}
	for i := 0; i < cap(chunkSlots); i++ {
		chunkSlots <- struct{}{}
	}
	small := []byte("piece")
	code := postPiece(t, s, tok, projectID, shaHex(small), 0, 1, small)
	for len(chunkSlots) > 0 {
		<-chunkSlots
	}
	if code != http.StatusTooManyRequests {
		t.Fatalf("piece with every upload slot busy: %d, want 429", code)
	}
}

// A piece sent again replaces the stored one; near the size cap it must not
// be counted twice.
func TestResentPieceIsNotCountedTwice(t *testing.T) {
	s := newBlobServer(t)
	old := maxAssembledBytes
	maxAssembledBytes = 100
	t.Cleanup(func() { maxAssembledBytes = old })
	projectID := claimFolder(t, s, "chunk-resend", "owner")
	tok := loginAs(t, s, "owner")
	a, b := make([]byte, 50), make([]byte, 40)
	b[0] = 1
	whole := append(append([]byte{}, a...), b...)
	parent := shaHex(whole)
	for _, step := range []struct {
		index int
		body  []byte
	}{{0, a}, {1, b}, {1, b}, {0, a}} {
		if code := postPiece(t, s, tok, projectID, parent, step.index, 2, step.body); code != http.StatusCreated {
			t.Fatalf("piece %d (re-sent): %d", step.index, code)
		}
	}
	rec := doJSON(t, s, http.MethodPost, "/v1/blobs/"+parent+"/assemble", tok, map[string]any{"project_id": projectID, "purpose": "transcript"})
	if rec.Code != http.StatusOK {
		t.Fatalf("assemble after re-sends: %d %s", rec.Code, rec.Body.String())
	}
}
