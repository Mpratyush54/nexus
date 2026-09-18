package server

// Tests for the GET /memory/search vector path (issue #37): ?embedding=
// parsing plus handler routing (vector store / unsupported store / bad
// input). The pgvector cosine itself lives in the store package and is
// covered there; these tests pin the HTTP contract.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

// vec1536Raw builds a valid 1536-dim ?embedding= value (issue #105: the
// schema is vector(1536), so short fixtures are now 400s).
func vec1536Raw() string {
	return strings.Repeat("0.1,", store.EmbeddingDim-1) + "0.1"
}

func TestParseEmbeddingParam(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []float32
		wantErr bool
	}{
		{"absent", "", nil, false},
		{"garbage", "abc", nil, true},
		{"mixed garbage", "0.1,nope", nil, true},
		{"nan rejected", "NaN", nil, true},
		{"inf rejected", "Inf", nil, true},
		{"empty brackets", "[]", nil, true},
		// Wrong width is a 400 even when every component parses (issue #105).
		{"short literal", "[0.1,0.2,0.3]", nil, true},
		{"short csv", "1,0,-2.5", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseEmbeddingParam(c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("raw %q: expected error, got %v", c.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("raw %q: unexpected error %v", c.raw, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("raw %q: got %v, want %v", c.raw, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("raw %q: got %v, want %v", c.raw, got, c.want)
				}
			}
		})
	}
}

// vectorStub is a Store with pgvector support: it records the vector call.
type vectorStub struct {
	store.Store
	items []*store.MemoryItem
	vec   []float32
	calls int
}

func (v *vectorStub) SearchMemoryVector(_ context.Context, _ string, queryVec []float32, _ int) ([]*store.MemoryItem, error) {
	v.calls++
	v.vec = queryVec
	return v.items, nil
}

func TestSearchMemoryHandlerVectorPath(t *testing.T) {
	stub := &vectorStub{Store: store.NewMemStore(), items: []*store.MemoryItem{{ID: "m1", Key: "k"}}}
	s := NewServer(stub)
	tok := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodGet, "/memory/search?project_id=p1&embedding=["+vec1536Raw()+"]", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("vector search status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if stub.calls != 1 {
		t.Fatalf("vector path must call SearchMemoryVector once (calls=%d)", stub.calls)
	}
	if len(stub.vec) != store.EmbeddingDim {
		t.Fatalf("vector forwarded dims = %d, want %d", len(stub.vec), store.EmbeddingDim)
	}
}

func TestSearchMemoryHandlerVectorUnsupportedStore400(t *testing.T) {
	s := newTestServer() // MemStore: text only
	tok := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodGet, "/memory/search?project_id=p1&embedding=["+vec1536Raw()+"]", tok, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported vector store status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestSearchMemoryHandlerShortVector400(t *testing.T) {
	s := newTestServer()
	tok := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodGet, "/memory/search?project_id=p1&embedding=[0.1,0.2]", tok, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short vector status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestSearchMemoryHandlerBadEmbedding400(t *testing.T) {
	s := newTestServer()
	tok := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodGet, "/memory/search?project_id=p1&embedding=abc", tok, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad embedding status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}
