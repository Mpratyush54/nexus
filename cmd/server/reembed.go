package main

// reembed.go — `central-server reembed`: the one-off, rate-limited backfill
// that replaces HashEmbed (or older-model) vectors with the configured
// embedding model, plus the opt-in startup variant
// (CENTRAL_EMBEDDING_BACKFILL_ON_START=1). Both are resumable: they only
// touch rows whose embedding_model differs from the live model.

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
	rate := fs.Float64("rate", 5, "max embedding calls per second (<0 = unlimited)")
	batch := fs.Int("batch", 50, "rows per page")
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
		return errors.New("CENTRAL_EMBEDDING_PROVIDER is hash; set the real provider (e.g. local) or pass -allow-hash")
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
	// Probe once so a down sidecar fails fast with a clear message.
	if _, err := p.EmbedStrict(ctx, "reembed probe"); err != nil {
		return fmt.Errorf("embedding provider %s not reachable: %w", p.Model(), err)
	}
	start := time.Now()
	stats, err := memctx.Reembed(ctx, p, pg, memctx.ReembedOptions{
		BatchSize: *batch, PerSecond: *rate, Max: *maxRows, Logf: log.Printf,
	})
	log.Printf("reembed: done in %s: %d embedded, %d skipped", time.Since(start).Round(time.Second), stats.Embedded, stats.Skipped)
	return err
}

// startupReembed runs the backfill in the background after boot, at the
// configured rate. Provider failures end the run quietly (next boot retries).
func startupReembed(ctx context.Context, p *memctx.Provider, st memctx.ReembedStore) {
	rate := 5.0
	if raw := strings.TrimSpace(os.Getenv("CENTRAL_EMBEDDING_BACKFILL_RATE")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
			rate = v
		}
	}
	// Give the sidecar time to load its model before the first call.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	stats, err := memctx.Reembed(ctx, p, st, memctx.ReembedOptions{PerSecond: rate})
	if err != nil && ctx.Err() == nil {
		log.Printf("server: startup reembed stopped after %d rows: %v", stats.Embedded, err)
		return
	}
	if stats.Embedded > 0 {
		log.Printf("server: startup reembed finished: %d rows now %s", stats.Embedded, p.Model())
	}
}
