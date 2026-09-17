// Embedding generation pipeline (nexus issue #76).
//
// The schema carries vector(1536) columns with IVFFlat indexes and the store
// accepts precomputed vectors (ProposedInput.Embedding, FormatEmbedding),
// but no component generated them: memory_write stored Embedding = nil, so
// SearchMemoryVector always fell back to keyword match. This file provides
// the generation seam:
//
//   - Embedder: the replaceable backend signature. Production passes an LLM
//     embedder (OpenAI text-embedding-3-small / Ollama via net/http);
//     HashEmbed is the deterministic stdlib interim with the same 1536-dim,
//     L2-normalized contract, so pgvector cosine ranking works today and the
//     LLM swap is a one-line substitution.
//   - BackfillEmbeddings: fills missing vectors for already-stored rows.
//     Persistence stays with the caller (store writes), so pgvector remains
//     the only retrieval path — this package never invents a second index.
//
// Embeddings cover key + content: retrieval matches on identity and payload.
package context

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"math"
	"strings"

	"central-memory/internal/store"
)

// EmbedDims is the vector width every backend must produce: it aliases the
// store's vector(1536) contract (issue #105) so HashEmbed and LLM rows share
// one index and can never drift apart.
const EmbedDims = store.EmbeddingDim

// Embedder generates one L2-normalized vector per text. Implementations must
// return exactly EmbedDims components; errors fail the write/backfill item,
// never silently store a short vector.
type Embedder func(ctx context.Context, text string) ([]float32, error)

// EmbedFunc is the synchronous backend shape (HashEmbed and most LLM
// wrappers); AsEmbedder adapts it to Embedder.
type EmbedFunc func(text string) []float32

// AsEmbedder adapts a synchronous backend to the Embedder seam.
func AsEmbedder(fn EmbedFunc) Embedder {
	return func(ctx context.Context, text string) ([]float32, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return fn(text), nil
	}
}

// HashEmbed is the deterministic stdlib interim backend: a hashed
// bag-of-words over unigrams + bigrams, L2-normalized for cosine ranking.
// Same text always yields the same vector on every platform; unknown/empty
// text yields the zero vector (NeedsEmbedding treats empty as missing).
func HashEmbed(text string) []float32 {
	vec := make([]float32, EmbedDims)
	tokens := tokenizeEmbed(text)
	if len(tokens) == 0 {
		return vec
	}
	for i, tok := range tokens {
		h := fnv.New64a()
		_, _ = h.Write([]byte(tok))
		vec[h.Sum64()%EmbedDims] += 1
		if i > 0 {
			bh := fnv.New64a()
			_, _ = bh.Write([]byte(tokens[i-1] + "\x00" + tok))
			vec[bh.Sum64()%EmbedDims] += 0.5
		}
	}
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	if sum == 0 {
		return vec
	}
	norm := float32(math.Sqrt(sum))
	for i, v := range vec {
		vec[i] = v / norm
	}
	return vec
}

// tokenizeEmbed lowercases text into alphanumeric tokens (length >= 2 so
// noise single letters do not dominate the hash space).
func tokenizeEmbed(text string) []string {
	var out []string
	for _, f := range strings.Fields(strings.ToLower(text)) {
		var b strings.Builder
		for _, r := range f {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		if b.Len() >= 2 {
			out = append(out, b.String())
		}
	}
	return out
}

// EmbedFingerprint renders a short stable hash of a vector for test
// comparisons without dumping 1536 floats.
func EmbedFingerprint(vec []float32) string {
	h := fnv.New64a()
	var b [4]byte
	for _, v := range vec {
		binary.LittleEndian.PutUint32(b[:], math.Float32bits(v))
		_, _ = h.Write(b[:])
	}
	sum := h.Sum64()
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 16)
	for i := 0; i < 8; i++ {
		c := byte(sum >> (56 - 8*i))
		out = append(out, hexdigits[c>>4], hexdigits[c&0xf])
	}
	return string(out)
}

// EmbedTextForItem renders the text an item is embedded under: key + content
// so retrieval matches on identity and payload.
func EmbedTextForItem(key, content string) string {
	key = strings.TrimSpace(key)
	content = strings.TrimSpace(content)
	if key == "" {
		return content
	}
	if content == "" {
		return key
	}
	return key + "\n" + content
}

// NeedsEmbedding reports whether a stored row still needs a vector: nil or
// wrong-width embeddings are (re)generated; exact-width rows are kept.
func NeedsEmbedding(m *store.MemoryItem) bool {
	return m == nil || len(m.Embedding) != EmbedDims
}

// BackfillEmbeddings generates vectors for every item missing one,
// writing the result back onto the item in place. It returns the count
// filled. Persistence is the caller's job (e.g. store writes carrying
// ProposedInput.Embedding); failures abort with the count filled so far
// and the error.
func BackfillEmbeddings(ctx context.Context, emb Embedder, items []*store.MemoryItem) (int, error) {
	if emb == nil {
		emb = AsEmbedder(HashEmbed)
	}
	filled := 0
	for _, m := range items {
		if m == nil {
			continue
		}
		if !NeedsEmbedding(m) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return filled, err
		}
		vec, err := emb(ctx, EmbedTextForItem(m.Key, m.Content))
		if err != nil {
			return filled, err
		}
		if len(vec) != EmbedDims {
			continue // never store a short vector; leave for the next pass
		}
		m.Embedding = vec
		filled++
	}
	return filled, nil
}
