package blobs

import (
	"bytes"
	"errors"
	"testing"
)

func TestChunkConstantsAndSizeLimit(t *testing.T) {
	if DefaultChunkSize != 16<<20 || ChunkThreshold != 64<<20 || MaxFileBytes != 10<<30 {
		t.Fatalf("sizes chunk=%d threshold=%d max=%d", DefaultChunkSize, ChunkThreshold, MaxFileBytes)
	}
	if err := CheckSize(MaxFileBytes); err != nil {
		t.Fatal(err)
	}
	if err := CheckSize(MaxFileBytes + 1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over 10 GiB: %v", err)
	}
	n, err := ChunkCount(ChunkThreshold, 0)
	if err != nil || n != 1 {
		t.Fatalf("at threshold: n=%d err=%v", n, err)
	}
	n, err = ChunkCount(ChunkThreshold+1, 0)
	if err != nil || n != 5 {
		t.Fatalf("just over threshold: n=%d err=%v", n, err)
	}
	n, err = ChunkCount(MaxFileBytes, 0)
	if err != nil || n != 640 {
		t.Fatalf("10 GiB: n=%d err=%v", n, err)
	}
	if _, err := ChunkCount(MaxFileBytes+1, 0); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ChunkCount over limit: %v", err)
	}
}

func TestChunkSplitAssembleInjectedSize(t *testing.T) {
	data := []byte("abcdefghij")
	parts, err := Split(data, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 || parts[0].Total != 3 {
		t.Fatalf("parts: %+v", parts)
	}
	if string(parts[0].Body) != "abcd" || string(parts[1].Body) != "efgh" || string(parts[2].Body) != "ij" {
		t.Fatalf("bodies: %q %q %q", parts[0].Body, parts[1].Body, parts[2].Body)
	}
	for _, p := range parts {
		again := shaOf(p.Body)
		if p.SHA256 != again {
			t.Fatalf("hash %s want %s", p.SHA256, again)
		}
	}
	got, err := Assemble([]Chunk{parts[2], parts[0], parts[1]})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("assembled %q", got)
	}

	whole, err := Split(data, 0)
	if err != nil || len(whole) != 1 || !bytes.Equal(whole[0].Body, data) {
		t.Fatalf("default size should keep a small file whole: %+v %v", whole, err)
	}
	whole, err = Split(data, DefaultChunkSize)
	if err != nil || len(whole) != 1 {
		t.Fatalf("explicit default size: %+v %v", whole, err)
	}

	bad := append([]Chunk(nil), parts...)
	bad[1].Body = []byte("XXXX")
	if _, err := Assemble(bad); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("hash mismatch: %v", err)
	}
	badHash := append([]Chunk(nil), parts...)
	badHash[0].SHA256 = parts[1].SHA256
	if _, err := Assemble(badHash); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("swapped hash: %v", err)
	}
	if _, err := Assemble([]Chunk{parts[0], parts[2]}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("gap: %v", err)
	}
	gapped := []Chunk{parts[0], parts[2]}
	gapped[0].Total = 3
	gapped[1].Total = 3
	if _, err := Assemble(gapped); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("explicit gap: %v", err)
	}
	if _, err := Assemble(nil); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("empty: %v", err)
	}
}

func shaOf(b []byte) string {
	c := newChunk(0, 1, b)
	return c.SHA256
}
