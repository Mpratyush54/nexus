// Package mcp — embed.go: embedding generation seam (nexus issue #76).
//
// Memory writes previously stored no vector (Embedding = nil), so pgvector
// search always fell back to keyword match. The pipeline now generates at
// write time:
//
//   - Config.Embed selects the backend. It defaults to HashEmbed (the
//     deterministic stdlib interim from internal/context: 1536-dim,
//     L2-normalized, schema-compatible); production substitutes an LLM
//     embedder (OpenAI text-embedding-3-small / Ollama) with the same
//     shape, so the swap never touches the tool handlers.
//   - handleMemoryWrite embeds key + content and carries the vector on
//     MemoryItem.Embedding for the store adapter to persist.
//   - Already-stored rows without vectors are covered by
//     context.BackfillEmbeddings (same backend, same dims); MCP DTOs need
//     no separate backfill because every write embeds.
//
// pgvector stays the only retrieval path: this file generates vectors, it
// never invents a second index.
package mcp

import (
	"central-memory/internal/context"
)

// EmbedDims is the vector width every backend must produce (schema
// vector(1536)); re-exported so callers need not import internal/context.
const EmbedDims = context.EmbedDims

// EmbedFunc generates one L2-normalized EmbedDims vector per text.
type EmbedFunc func(text string) []float32

// HashEmbed is the default interim backend: deterministic, stdlib-only,
// schema-compatible. See internal/context.HashEmbed.
func HashEmbed(text string) []float32 {
	return context.HashEmbed(text)
}

// embedText renders the text a memory is embedded under (key + content).
func embedText(key, content string) string {
	return context.EmbedTextForItem(key, content)
}

// embedForWrite resolves the configured backend (default HashEmbed) and
// embeds one write. A nil Server or panicking backend degrades to nil
// (text-searchable row) rather than failing the write.
func (s *Server) embedForWrite(key, content string) (vec []float32) {
	fn := HashEmbed
	if s != nil && s.cfg.Embed != nil {
		fn = s.cfg.Embed
	}
	defer func() {
		_ = recover()
	}()
	vec = fn(embedText(key, content))
	if len(vec) != EmbedDims {
		return nil
	}
	return vec
}
