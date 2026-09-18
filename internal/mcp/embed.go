// Package mcp — embed.go: embedding generation seam (nexus issues #76, #165).
//
// Memory writes previously stored no vector (Embedding = nil), so pgvector
// search always fell back to keyword match. The pipeline now generates at
// write time:
//
//   - Config.Embed selects a sync backend. Nil selects HashEmbed.
//   - Config.Embedder (preferred when set) is the async provider from
//     internal/context (openai / ollama / hash via CENTRAL_EMBEDDING_*);
//     failures fall back to HashEmbed so writes never hard-fail.
//   - handleMemoryWrite embeds key + content and carries the vector on
//     MemoryItem.Embedding for the store adapter to persist.
//   - handleMemorySearch embeds the query and prefers SearchMemoryVector
//     when the store supports it; keyword SearchMemory is the fallback.
//
// pgvector stays the only retrieval path: this file generates vectors, it
// never invents a second index.
package mcp

import (
	"context"

	memctx "central-memory/internal/context"
)

// EmbedDims is the vector width every backend must produce (schema
// vector(1536)); re-exported so callers need not import internal/context.
const EmbedDims = memctx.EmbedDims

// EmbedFunc generates one L2-normalized EmbedDims vector per text.
type EmbedFunc func(text string) []float32

// HashEmbed is the default interim backend: deterministic, stdlib-only,
// schema-compatible. See internal/context.HashEmbed.
func HashEmbed(text string) []float32 {
	return memctx.HashEmbed(text)
}

// embedText renders the text a memory is embedded under (key + content).
func embedText(key, content string) string {
	return memctx.EmbedTextForItem(key, content)
}

// resolveEmbedder returns the configured Embedder: Config.Embedder when set,
// else Config.Embed adapted, else HashEmbed. Always fallback-safe.
func (s *Server) resolveEmbedder() memctx.Embedder {
	if s != nil && s.cfg.Embedder != nil {
		return s.cfg.Embedder
	}
	if s != nil && s.cfg.Embed != nil {
		return memctx.AsEmbedder(memctx.EmbedFunc(s.cfg.Embed))
	}
	return memctx.AsEmbedder(memctx.HashEmbed)
}

// embedForWrite resolves the configured backend (default HashEmbed) and
// embeds one write. Provider failures fall back to HashEmbed; a panicking
// sync backend degrades to nil (text-searchable row) rather than failing
// the write.
func (s *Server) embedForWrite(key, content string) (vec []float32) {
	defer func() {
		if recover() != nil {
			vec = nil
		}
	}()
	emb := s.resolveEmbedder()
	out, err := emb(context.Background(), embedText(key, content))
	if err != nil || len(out) != EmbedDims {
		out = HashEmbed(embedText(key, content))
	}
	if len(out) != EmbedDims {
		return nil
	}
	return out
}

// embedQuery embeds a search query for vector retrieval. Failures fall back
// to HashEmbed so keyword-compatible vectors still rank HashEmbed rows.
func (s *Server) embedQuery(ctx context.Context, query string) []float32 {
	emb := s.resolveEmbedder()
	vec, err := emb(ctx, query)
	if err != nil || len(vec) != EmbedDims {
		return HashEmbed(query)
	}
	return vec
}
