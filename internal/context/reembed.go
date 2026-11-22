// Re-embed backfill.
//
// Reembed walks memory rows whose vector is missing or tagged with another
// model (memory_items.embedding_model: legacy HashEmbed rows, writes made
// while the provider was down or rate limited, or an older model) and
// replaces them using the provider's strict batch path, so a failing
// provider never writes hash vectors. Requests are rate limited per minute
// (each request carries up to Provider.MaxBatch rows), 429s are retried
// after Retry-After with exponential backoff, and the run is resumable and
// idempotent (re-running only touches rows that are still stale).
package context

import (
	"context"
	"errors"
	"fmt"
	"time"

	"central-memory/internal/store"
)

// ReembedStore is the store surface the backfill needs (PostgresStore and
// MemStore both satisfy it).
type ReembedStore interface {
	ListMemoryForReembed(ctx context.Context, model, afterID string, limit int) ([]store.ReembedCandidate, error)
	SetMemoryEmbedding(ctx context.Context, id string, vec []float32, model string) error
}

// DefaultBackfillRPM keeps the backfill at half of OpenRouter's free-model
// limit (20 requests/minute), leaving room for live writes and searches.
const DefaultBackfillRPM = 10

// ReembedOptions tunes one backfill run.
type ReembedOptions struct {
	// RequestsPerMinute caps provider requests (default DefaultBackfillRPM;
	// <0 = unlimited). Each request embeds up to Provider.MaxBatch rows.
	RequestsPerMinute float64
	Max               int // stop after this many rows embedded (0 = all)
	// MaxRetries bounds retries of one batch on 429/cooldown (default 5).
	MaxRetries int
	// MaxWait stops the run instead of sleeping longer than this for one
	// retry (e.g. a daily limit hours away; default 10m).
	MaxWait time.Duration
	Logf    func(format string, args ...any)
	// sleep is replaceable in tests.
	sleep func(ctx context.Context, d time.Duration) error
}

// ReembedStats reports one run.
type ReembedStats struct {
	Embedded int // rows written with the provider's vector
	Skipped  int // rows that vanished or failed to persist
	Requests int // provider requests made (including retries)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Reembed runs the backfill until no stale rows remain, Max is reached, the
// context ends, or the provider keeps failing (returned as the error; rows
// already written stay written).
func Reembed(ctx context.Context, p *Provider, st ReembedStore, opt ReembedOptions) (ReembedStats, error) {
	var stats ReembedStats
	if p == nil || st == nil {
		return stats, errors.New("context: reembed needs a provider and a store")
	}
	if opt.RequestsPerMinute == 0 {
		opt.RequestsPerMinute = DefaultBackfillRPM
	}
	if opt.MaxRetries <= 0 {
		opt.MaxRetries = 5
	}
	if opt.MaxWait <= 0 {
		opt.MaxWait = 10 * time.Minute
	}
	if opt.sleep == nil {
		opt.sleep = sleepCtx
	}
	logf := opt.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var gap time.Duration
	if opt.RequestsPerMinute > 0 {
		gap = time.Duration(float64(time.Minute) / opt.RequestsPerMinute)
	}
	model := p.Model()
	batchSize := p.MaxBatch()
	var last time.Time

	after := ""
	for {
		if opt.Max > 0 && stats.Embedded >= opt.Max {
			return stats, nil
		}
		limit := batchSize
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
		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = EmbedTextForItem(c.Key, c.Content)
		}

		var vecs [][]float32
		for attempt := 0; ; attempt++ {
			if gap > 0 && !last.IsZero() {
				if wait := gap - time.Since(last); wait > 0 {
					if err := opt.sleep(ctx, wait); err != nil {
						return stats, err
					}
				}
			}
			last = time.Now()
			stats.Requests++
			vecs, err = p.EmbedBatchStrict(ctx, texts)
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				return stats, ctx.Err()
			}
			var rl *RateLimitError
			retryable := errors.As(err, &rl) || errors.Is(err, ErrProviderUnavailable)
			if !retryable || attempt >= opt.MaxRetries {
				return stats, err
			}
			// Exponential backoff (5s, 10s, 20s, ...), but never sooner than
			// the provider's own cooldown / Retry-After.
			wait := (5 * time.Second) << attempt
			if until := time.Until(p.RetryAt()); until > wait {
				wait = until
			}
			if wait > opt.MaxWait {
				return stats, fmt.Errorf("context: reembed paused: provider asks to wait %s (daily limit?): %w", wait.Round(time.Second), err)
			}
			logf("reembed: rate limited, retrying in %s (attempt %d/%d)", wait.Round(time.Second), attempt+1, opt.MaxRetries)
			if err := opt.sleep(ctx, wait); err != nil {
				return stats, err
			}
			p.clearCooldown()
		}
		for i, c := range batch {
			if i >= len(vecs) || len(vecs[i]) != EmbedDims {
				stats.Skipped++
				continue
			}
			if err := st.SetMemoryEmbedding(ctx, c.ID, vecs[i], model); err != nil {
				stats.Skipped++
				logf("reembed: %s not saved: %v", c.ID, err)
				continue
			}
			stats.Embedded++
		}
		logf("reembed: %d rows embedded with %s so far (%d requests)", stats.Embedded, model, stats.Requests)
	}
}
