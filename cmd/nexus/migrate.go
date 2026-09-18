package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"central-memory/internal/migrate"
)

type migrateOptions struct {
	Vault  string
	DryRun bool
	JSON   bool
}

// defaultVaultDir returns the legacy vault default
// (%USERPROFILE%\.central-memory on Windows, ~/.central-memory elsewhere).
func defaultVaultDir() string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".central-memory")
	}
	return filepath.Join(".central-memory")
}

// parseMigrateArgs parses `nexus migrate [--vault PATH] [--dry-run] [--json]`.
// Pure flag parsing: no I/O, no os.Exit.
func parseMigrateArgs(args []string) (migrateOptions, error) {
	var o migrateOptions
	fs := flag.NewFlagSet("nexus migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Vault, "vault", "", "legacy vault root (default %USERPROFILE%\\.central-memory)")
	fs.BoolVar(&o.DryRun, "dry-run", false, "parse, validate and count only; write nothing")
	fs.BoolVar(&o.JSON, "json", false, "emit JSON instead of tables")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if len(fs.Args()) > 0 {
		return o, fmt.Errorf("migrate takes no positional arguments, got %q", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(o.Vault) == "" {
		o.Vault = defaultVaultDir()
	}
	return o, nil
}

// runMigrate executes the Phase 1b legacy-vault import (Issue #83) via
// migrate.Run: parses memory/global/learnings.md,
// memory/projects/*/MEMORY.md and agents/*/normalized/sessions.jsonl,
// dedups, seeds projects via Fingerprint, and reports counts. With
// --dry-run nothing is written; without it the same counts describe what
// the store adapter must insert.
func runMigrate(_ context.Context, cfg Config, args []string, stdout io.Writer) error {
	o, err := parseMigrateArgs(args)
	if err != nil {
		return err
	}
	asJSON := cfg.JSON || o.JSON
	counts, err := migrate.Run(o.Vault, o.DryRun)
	if err != nil {
		return err
	}
	mode := "imported"
	if counts.DryRun {
		mode = "dry-run (nothing written)"
	}
	if asJSON {
		return printJSON(stdout, map[string]any{
			"vault":    o.Vault,
			"dry_run":  counts.DryRun,
			"mode":     mode,
			"memories": counts.Memories,
			"projects": counts.Projects,
			"events":   counts.Events,
			"skipped":  counts.Skipped,
		})
	}
	fmt.Fprintf(stdout, "nexus migrate — %s from %s\n", mode, o.Vault)
	printTable(stdout,
		[]string{"MEMORIES", "PROJECTS", "EVENTS", "SKIPPED"},
		[][]string{{fmt.Sprint(counts.Memories), fmt.Sprint(counts.Projects), fmt.Sprint(counts.Events), fmt.Sprint(counts.Skipped)}})
	return nil
}
