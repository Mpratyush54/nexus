package main

// reembed.go — `central-server reembed`: the one-off, rate-limited backfill
// that replaces HashEmbed (or older-model) vectors with the configured
// embedding model, plus the opt-in background variant
// (CENTRAL_EMBEDDING_BACKFILL_ON_START=1), which runs after boot and then
// every CENTRAL_EMBEDDING_BACKFILL_INTERVAL to heal rows written while the
// provider was rate limited. Both are resumable: they only touch rows whose
// embedding_model differs from the live model, in batched requests.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	memctx "central-memory/internal/context"
	"central-memory/internal/store"
)

func runReembed(args []string) error {
	fs := flag.NewFlagSet("reembed", flag.ContinueOnError)
	rpm := fs.Float64("rpm", memctx.DefaultBackfillRPM, "max provider requests per minute (each embeds a batch of rows; <0 = unlimited)")
	maxRows := fs.Int("max", 0, "stop after N rows (0 = all)")
	dry := fs.Bool("dry-run", false, "only count the rows that would be re-embedded")
	allowHash := fs.Bool("allow-hash", false, "allow running with the hash provider")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	p := memctx.ProviderFromEnv()
	if p.IsHash() && !*allowHash {
		return errors.New("CENTRAL_EMBEDDING_PROVIDER is hash; set the real provider (e.g. openrouter) or pass -allow-hash")
	}
	cfg := resolveConfig()
	if cfg.databaseURL == "" {
		return errors.New("no DATABASE_URL/DB_* configured")
	}
	pg, err := store.NewPostgresStore(ctx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pg.Close()

	pending, err := pg.CountMemoryForReembed(ctx, p.Model())
	if err != nil {
		return fmt.Errorf("count (is migration 043 applied?): %w", err)
	}
	log.Printf("reembed: %d rows need %s", pending, p.Model())
	if *dry || pending == 0 {
		return nil
	}
	// Probe once so a missing key or down provider fails fast.
	if _, err := p.EmbedStrict(ctx, "reembed probe"); err != nil {
		return fmt.Errorf("embedding provider %s not reachable: %w", p.Model(), err)
	}
	start := time.Now()
	stats, err := memctx.Reembed(ctx, p, pg, memctx.ReembedOptions{
		RequestsPerMinute: *rpm, Max: *maxRows, Logf: log.Printf,
	})
	log.Printf("reembed: done in %s: %d embedded, %d skipped, %d requests", time.Since(start).Round(time.Second), stats.Embedded, stats.Skipped, stats.Requests)
	return err
}

// startupReembed runs the backfill in the background after boot and then
// every CENTRAL_EMBEDDING_BACKFILL_INTERVAL (default 15m; "0" = once) at
// CENTRAL_EMBEDDING_BACKFILL_RPM requests/minute. A pass that hits a
// provider error just ends; the next pass retries.
func startupReembed(ctx context.Context, p *memctx.Provider, st memctx.ReembedStore) {
	rpm := float64(memctx.DefaultBackfillRPM)
	if raw := strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_BACKFILL_RPM")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
			rpm = v
		}
	}
	interval := 15 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_BACKFILL_INTERVAL")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d >= 0 {
			interval = d
		}
	}
	delay := 30 * time.Second // let the API settle before the first pass
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		stats, err := memctx.Reembed(ctx, p, st, memctx.ReembedOptions{RequestsPerMinute: rpm})
		if err != nil && ctx.Err() == nil {
			log.Printf("server: background reembed stopped after %d rows: %v", stats.Embedded, err)
		} else if stats.Embedded > 0 {
			log.Printf("server: background reembed: %d rows now %s (%d requests)", stats.Embedded, p.Model(), stats.Requests)
		}
		if interval == 0 {
			return
		}
		delay = interval
	}
}
