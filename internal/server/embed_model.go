package server

// embed_model.go — model-aware embedding helpers (OpenRouter / local providers).
//
// Writes always get a vector (HashEmbed when the provider is down) tagged
// with the model that made it, so `central-server reembed` can later redo
// the hash rows. Search never embeds a query with HashEmbed while a real
// model is configured: a hash vector is meaningless against model vectors,
// so a provider failure sends the query to keyword search instead.

import (
	"context"
	"log"

	memctx "central-memory/internal/context"
	"central-memory/internal/store"
)

// memoryEmbeddingStore is the re-embed seam (PostgresStore, MemStore).
type memoryEmbeddingStore interface {
	SetMemoryEmbedding(ctx context.Context, id string, vec []float32, model string) error
}

// embedItem sets item.Embedding and item.EmbeddingModel from key+content.
func (s *Server) embedItem(ctx context.Context, item *store.MemoryItem) {
	if item == nil {
		return
	}
	text := memctx.EmbedTextForItem(item.Key, item.Content)
	if s.EmbedProvider != nil {
		item.Embedding, item.EmbeddingModel = s.EmbedProvider.Embed(ctx, text)
		return
	}
	item.Embedding = s.embedText(ctx, text)
	item.EmbeddingModel = ""
}

// tagEmbeddingModel fills EmbeddingModel for a vector made elsewhere with
// s.resolveEmbedder() (cloud MCP writes).
func (s *Server) tagEmbeddingModel(item *store.MemoryItem) {
	if item == nil || s.EmbedProvider == nil || item.EmbeddingModel != "" {
		return
	}
	item.EmbeddingModel = s.EmbedProvider.ModelOf(item.Embedding, memctx.EmbedTextForItem(item.Key, item.Content))
}

// embedQuery embeds a search query. ok=false means "use keyword search":
// the configured model is unavailable (never a hash stand-in).
func (s *Server) embedQuery(ctx context.Context, query string) ([]float32, bool) {
	if s.EmbedProvider != nil && !s.EmbedProvider.IsHash() {
		vec, err := s.EmbedProvider.EmbedQueryStrict(ctx, query)
		if err != nil || len(vec) != memctx.EmbedDims {
			return nil, false
		}
		return vec, true
	}
	vec := s.embedText(ctx, query)
	return vec, len(vec) == memctx.EmbedDims
}

// vectorQueryFor pairs a query vector made by provider p with p's model (so
// only same-model rows are compared) and p's similarity floor. A nil
// provider (tests, legacy wiring) gives an unfiltered query at
// store.MinVectorSimilarity.
func vectorQueryFor(p *memctx.Provider, vec []float32) store.VectorQuery {
	q := store.VectorQuery{Vec: vec}
	if p != nil {
		q.Model = p.Model()
		q.MinSimilarity = p.MinSimilarity()
	}
	return q
}

type vectorSearchFunc func(ctx context.Context, projectID string, vec []float32, limit int) ([]*store.MemoryItem, error)

// modelVectorSearchFor returns st's model-filtered vector search for vectors
// made by p, falling back to the unfiltered SearchMemoryVector for stores
// without the model variant; nil when st has no vector path at all.
func modelVectorSearchFor(st any, p *memctx.Provider) vectorSearchFunc {
	if ms, ok := st.(interface {
		SearchMemoryVectorModel(ctx context.Context, projectID string, q store.VectorQuery, limit int) ([]*store.MemoryItem, error)
	}); ok {
		return func(ctx context.Context, projectID string, vec []float32, limit int) ([]*store.MemoryItem, error) {
			return ms.SearchMemoryVectorModel(ctx, projectID, vectorQueryFor(p, vec), limit)
		}
	}
	if vs, ok := st.(interface {
		SearchMemoryVector(ctx context.Context, projectID string, queryVec []float32, limit int) ([]*store.MemoryItem, error)
	}); ok {
		return vs.SearchMemoryVector
	}
	return nil
}

// modelVectorSearch is modelVectorSearchFor over the server's store and
// embedding provider.
func (s *Server) modelVectorSearch() vectorSearchFunc {
	return modelVectorSearchFor(s.Store, s.EmbedProvider)
}

// vectorPageFunc is a vectorSearchFunc with an offset over visible ranked
// rows and an optional level filter.
type vectorPageFunc func(ctx context.Context, projectID string, vec []float32, limit, offset int, level string) ([]*store.MemoryItem, error)

// modelVectorSearchPage is modelVectorSearch with paging and a level filter.
// Stores without SearchMemoryVectorModel use searchMemoryVector (unfiltered
// by model), which refuses offset/level it cannot honour; nil when the store
// has no vector path at all.
func (s *Server) modelVectorSearchPage() vectorPageFunc {
	if ms, ok := s.Store.(interface {
		SearchMemoryVectorModel(ctx context.Context, projectID string, q store.VectorQuery, limit int) ([]*store.MemoryItem, error)
	}); ok {
		p := s.EmbedProvider
		return func(ctx context.Context, projectID string, vec []float32, limit, offset int, level string) ([]*store.MemoryItem, error) {
			q := vectorQueryFor(p, vec)
			q.Offset = offset
			q.Level = level
			return ms.SearchMemoryVectorModel(ctx, projectID, q, limit)
		}
	}
	if _, ok := s.Store.(memoryVectorPager); !ok {
		if _, ok := s.Store.(memoryVectorSearcher); !ok {
			return nil
		}
	}
	return func(ctx context.Context, projectID string, vec []float32, limit, offset int, level string) ([]*store.MemoryItem, error) {
		items, _, err := s.searchMemoryVector(ctx, projectID, vec, limit, offset, level)
		return items, err
	}
}

// queryEmbedder is the MCP query seam: the provider's query-prefixed,
// fallback-safe embedder, or nil (MCP then uses Config.Embedder).
func (s *Server) queryEmbedder() memctx.Embedder {
	if s == nil || s.EmbedProvider == nil {
		return nil
	}
	return s.EmbedProvider.QueryEmbedder()
}

// vectorSearchUsable reports whether a precomputed query vector can be
// trusted against stored rows (false while the provider is failing, since
// the MCP query embedder then falls back to HashEmbed).
func (s *Server) vectorSearchUsable() bool {
	return s.EmbedProvider == nil || !s.EmbedProvider.Degraded()
}

// reembedAfterEdit refreshes an edited row's vector so search matches the
// new text. Best effort: the edit already committed, so a failure is only
// logged (the row stays tagged stale and the reembed backfill fixes it).
func (s *Server) reembedAfterEdit(ctx context.Context, item *store.MemoryItem) {
	if item == nil || item.ID == "" {
		return
	}
	es, ok := s.Store.(memoryEmbeddingStore)
	if !ok {
		return
	}
	probe := &store.MemoryItem{Key: item.Key, Content: item.Content}
	s.embedItem(ctx, probe)
	if len(probe.Embedding) != memctx.EmbedDims {
		return
	}
	if err := es.SetMemoryEmbedding(ctx, item.ID, probe.Embedding, probe.EmbeddingModel); err != nil {
		log.Printf("server: re-embed after edit %s: %v", item.ID, err)
		return
	}
	item.Embedding = probe.Embedding
	item.EmbeddingModel = probe.EmbeddingModel
}
