package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

func piece(project, parent, owner string, index, total int, body []byte) BlobChunk {
	sum := sha256.Sum256(body)
	return BlobChunk{ProjectID: project, ParentSHA256: parent, Owner: owner, Index: index, Total: total,
		ChunkSHA256: hex.EncodeToString(sum[:]), Body: body}
}

// withChunkLimits sets small limits and a controllable clock for one test.
func withChunkLimits(t *testing.T, perUser, total int64) *time.Time {
	t.Helper()
	ResetBlobChunks()
	oldUser, oldTotal, oldTTL, oldNow := MaxChunkBytesPerUser, MaxChunkBytesTotal, ChunkSetTTL, chunkNow
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	MaxChunkBytesPerUser, MaxChunkBytesTotal, ChunkSetTTL = perUser, total, time.Hour
	chunkNow = func() time.Time { return now }
	t.Cleanup(func() {
		MaxChunkBytesPerUser, MaxChunkBytesTotal, ChunkSetTTL, chunkNow = oldUser, oldTotal, oldTTL, oldNow
		ResetBlobChunks()
	})
	return &now
}

func TestBlobChunksFreedAfterDeleteAndReplaceCountsOnce(t *testing.T) {
	withChunkLimits(t, 100, 1000)
	for i := 0; i < 3; i++ { // the same piece sent again replaces it
		if err := PutBlobChunk(piece("p", "a", "u1", 0, 2, make([]byte, 40))); err != nil {
			t.Fatal(err)
		}
	}
	if total, mine := BlobChunkBytes("u1"); total != 40 || mine != 40 {
		t.Fatalf("held = %d/%d, want 40/40", total, mine)
	}
	if got := LoadBlobChunks("p", "a"); len(got) != 1 {
		t.Fatalf("pieces = %d", len(got))
	}
	DeleteBlobChunks("p", "a")
	if total, mine := BlobChunkBytes("u1"); total != 0 || mine != 0 || len(LoadBlobChunks("p", "a")) != 0 {
		t.Fatalf("after delete: %d/%d bytes held", total, mine)
	}
}

func TestBlobChunkLimits(t *testing.T) {
	withChunkLimits(t, 100, 150)
	if err := PutBlobChunk(piece("p", "big", "u1", 0, 1, make([]byte, 101))); !errors.Is(err, ErrChunkSetTooLarge) {
		t.Fatalf("one upload over the per-user limit: %v", err)
	}
	if err := PutBlobChunk(piece("p", "a", "u1", 0, 2, make([]byte, 60))); err != nil {
		t.Fatal(err)
	}
	// u1's second upload would put u1 over its limit.
	if err := PutBlobChunk(piece("p", "b", "u1", 0, 1, make([]byte, 50))); !errors.Is(err, ErrChunkQuota) {
		t.Fatalf("user limit: %v", err)
	}
	// Another user fits under their own limit but not under the total.
	if err := PutBlobChunk(piece("p", "c", "u2", 0, 1, make([]byte, 80))); err != nil {
		t.Fatal(err)
	}
	if err := PutBlobChunk(piece("p", "d", "u2", 0, 1, make([]byte, 20))); !errors.Is(err, ErrChunkQuota) {
		t.Fatalf("global limit: %v", err)
	}
	// A piece of an existing upload counts toward the upload's owner.
	if err := PutBlobChunk(piece("p", "a", "u2", 1, 2, make([]byte, 10))); err != nil {
		t.Fatal(err)
	}
	if _, mine := BlobChunkBytes("u1"); mine != 70 {
		t.Fatalf("u1 holds %d, want 70", mine)
	}
}

func TestAbandonedBlobChunksExpire(t *testing.T) {
	now := withChunkLimits(t, 100, 1000)
	if err := PutBlobChunk(piece("p", "old", "u1", 0, 2, make([]byte, 90))); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(30 * time.Minute)
	if err := PutBlobChunk(piece("p", "new", "u1", 0, 1, make([]byte, 20))); !errors.Is(err, ErrChunkQuota) {
		t.Fatalf("live upload still holds the quota: %v", err)
	}
	*now = now.Add(31 * time.Minute) // "old" untouched for over an hour
	if err := PutBlobChunk(piece("p", "new", "u1", 0, 1, make([]byte, 20))); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	if len(LoadBlobChunks("p", "old")) != 0 {
		t.Fatal("abandoned upload not expired")
	}
	if total, _ := BlobChunkBytes("u1"); total != 20 {
		t.Fatalf("held = %d, want 20", total)
	}
}

func TestAssembleBlobChunksChecksSizeAndSHA(t *testing.T) {
	withChunkLimits(t, 1<<20, 1<<20)
	a, b := []byte("hello "), []byte("world")
	whole := append(append([]byte{}, a...), b...)
	sum := sha256.Sum256(whole)
	parent := hex.EncodeToString(sum[:])
	if _, _, err := AssembleBlobChunks("p", parent, 100); !errors.Is(err, ErrAssembleMissing) {
		t.Fatalf("no pieces: %v", err)
	}
	if err := PutBlobChunk(piece("p", parent, "u", 0, 2, a)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AssembleBlobChunks("p", parent, 100); !errors.Is(err, ErrAssembleMissing) {
		t.Fatalf("one of two pieces: %v", err)
	}
	if err := PutBlobChunk(piece("p", parent, "u", 1, 2, b)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AssembleBlobChunks("p", parent, 10); !errors.Is(err, ErrAssembleTooLarge) {
		t.Fatalf("over the size limit: %v", err)
	}
	got, n, err := AssembleBlobChunks("p", parent, 100)
	if err != nil || string(got) != "hello world" || n != 2 || cap(got) != len(got) {
		t.Fatalf("assembled %q (%d pieces, cap %d): %v", got, n, cap(got), err)
	}
	// Pieces that do not add up to the declared sha256.
	if err := PutBlobChunk(piece("p", "bad", "u", 0, 1, a)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AssembleBlobChunks("p", "bad", 100); !errors.Is(err, ErrAssembleSHA) {
		t.Fatalf("wrong sha: %v", err)
	}
}
