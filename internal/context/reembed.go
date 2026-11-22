// Re-embed backfill (local embeddings sidecar).
//
// Reembed walks memory rows whose vector is missing or tagged with another
// model (memory_items.embedding_model: legacy HashEmbed rows, writes made
// while the sidecar was down, or an older model) and replaces them using
// the provider's strict path, so a failing provider stops the run instead of
// writing hash vectors. It is rate limited, resumable (re-running picks up
// what is still stale), and idempotent.
package context

import (
	"context"
	"errors"
	"time"

	"central-memory/internal/store"
)

// ReembedStore is the store surface the backfill needs (PostgresStore and
// MemStore both satisfy it).
type ReembedStore interface {
	ListMemoryForReembed(ctx context.Context, model, afterID string, limit int) ([]store.ReembedCandidate, error)
	SetMemoryEmbedding(ctx context.Context, id string, vec []float32, model string) error
}

// ReembedOptions tunes one backfill run.
type ReembedOptions struct {
	BatchSize int     // rows listed per page (default 50)
	PerSecond float64 // max embedding calls per second (default 5; <0 = unlimited)
	Max       int     // stop after this many rows embedded (0 = all)
	Logf      func(format string, args ...any)
}

// ReembedStats reports one run.
type ReembedStats struct {
	Embedded int // rows written with the provider's vector
	Skipped  int // rows that vanished or failed to persist
}

// Reembed runs the backfill until no stale rows remain, Max is reached, the
// context ends, or the provider fails (returned as the error; rows already
// written stay written).
func Reembed(ctx context.Context, p *Provider, st ReembedStore, opt ReembedOptions) (ReembedStats, error) {
	var stats ReembedStats
	if p == nil || st == nil {
		return stats, errors.New("context: reembed needs a provider and a store")
	}
	if opt.BatchSize <= 0 {
		opt.BatchSize = 50
	}
	if opt.PerSecond == 0 {
		opt.PerSecond = 5
	}
	logf := opt.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var tick <-chan time.Time
	if opt.PerSecond > 0 {
		t := time.NewTicker(time.Duration(float64(time.Second) / opt.PerSecond))
		defer t.Stop()
		tick = t.C
	}
	model := p.Model()
	// Rate-limited strict embedder: never substitutes HashEmbed.
	emb := func(ctx context.Context, text string) ([]float32, error) {
		if tick != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-tick:
			}
		}
		return p.EmbedStrict(ctx, text)
	}

	after := ""
	for {
		if opt.Max > 0 && stats.Embedded >= opt.Max {
			return stats, nil
		}
		limit := opt.BatchSize
		if opt.Max > 0 && opt.Max-stats.Embedded < limit {
			limit = opt.Max - stats.Embedded
		}
		batch, err := st.ListMemoryForReembed(ctx, model, after, limit)
		if err != nil {
			return stats, err
		}
		if len(batch) == 0 {
			return stats, nil
		}
		after = batch[len(batch)-1].ID
		items := make([]*store.MemoryItem, 0, len(batch))
		for _, c := range batch {
			items = append(items, &store.MemoryItem{ID: c.ID, Key: c.Key, Content: c.Content})
		}
		_, embErr := BackfillEmbeddings(ctx, emb, items)
		for _, m := range items {
			if len(m.Embedding) != EmbedDims {
				continue // not reached (provider failed) or empty text
			}
			if err := st.SetMemoryEmbedding(ctx, m.ID, m.Embedding, model); err != nil {
				stats.Skipped++
				logf("reembed: %s not saved: %v", m.ID, err)
				continue
			}
			stats.Embedded++
		}
		logf("reembed: %d rows embedded with %s so far", stats.Embedded, model)
		if embErr != nil {
			return stats, embErr
		}
	}
}
