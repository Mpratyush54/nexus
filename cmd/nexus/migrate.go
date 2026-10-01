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
	"central-memory/internal/migrate/firstrun"
)

type migrateOptions struct {
	Vault  string
	DryRun bool
	JSON   bool
}

type migrateFirstrunOptions struct {
	Src            string
	Outbox         string
	ClearAutostart bool
	JSON           bool
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
// Pure flag parsing: no I/O, no os.Exit. Positional args are rejected here;
// the firstrun subcommand is dispatched before this runs.
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
		return o, fmt.Errorf("migrate takes no positional arguments, got %q (did you mean: nexus migrate firstrun?)", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(o.Vault) == "" {
		o.Vault = defaultVaultDir()
	}
	return o, nil
}

func parseMigrateFirstrunArgs(args []string) (migrateFirstrunOptions, error) {
	var o migrateFirstrunOptions
	fs := flag.NewFlagSet("nexus migrate firstrun", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Src, "src", "", "legacy .central-memory path or workspace root containing it")
	fs.StringVar(&o.Outbox, "outbox", "", "outbox directory to receive migrated daemon state")
	fs.BoolVar(&o.ClearAutostart, "clear-autostart", false, "remove HKCU Run / fake autostart entries mentioning nexus-daemon")
	fs.BoolVar(&o.JSON, "json", false, "emit JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if len(fs.Args()) > 0 {
		return o, fmt.Errorf("migrate firstrun takes no positional arguments, got %q", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(o.Src) == "" {
		o.Src = firstrun.DefaultSource()
	}
	if strings.TrimSpace(o.Outbox) == "" {
		o.Outbox = filepath.Join(os.TempDir(), "nexus-firstrun-outbox")
	}
	return o, nil
}

// runMigrate dispatches vault import or first-run daemon migration.
//
//	nexus migrate [--vault PATH] [--dry-run] [--json]
//	nexus migrate firstrun [--src PATH] [--outbox DIR] [--clear-autostart] [--json]
func runMigrate(ctx context.Context, cfg Config, args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "firstrun" {
		return runMigrateFirstrun(ctx, cfg, args[1:], stdout)
	}
	return runMigrateVault(ctx, cfg, args, stdout)
}

func runMigrateFirstrun(_ context.Context, cfg Config, args []string, stdout io.Writer) error {
	o, err := parseMigrateFirstrunArgs(args)
	if err != nil {
		return err
	}
	asJSON := cfg.JSON || o.JSON
	res, err := firstrun.Run(o.Src, o.Outbox, o.ClearAutostart)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(stdout, res)
	}
	fmt.Fprintln(stdout, "nexus migrate firstrun — best-effort daemon → outbox migration")
	fmt.Fprintf(stdout, "  source:   %s\n", res.Import.Source)
	fmt.Fprintf(stdout, "  outbox:   %s\n", res.Import.OutboxDir)
	fmt.Fprintf(stdout, "  copied:   %d file(s)\n", len(res.Import.CopiedFiles))
	if len(res.Import.CopiedFiles) > 0 {
		fmt.Fprintf(stdout, "            %s\n", strings.Join(res.Import.CopiedFiles, ", "))
	}
	fmt.Fprintf(stdout, "  skipped:  %d\n", len(res.Import.Skipped))
	fmt.Fprintf(stdout, "  autostart hits: %d\n", len(res.Autostart))
	for _, h := range res.Autostart {
		fmt.Fprintf(stdout, "    - %s = %s\n", h.Name, h.Value)
	}
	if o.ClearAutostart {
		fmt.Fprintf(stdout, "  autostart cleared: %d\n", res.AutostartCleared)
	}
	for _, n := range res.Notes {
		fmt.Fprintf(stdout, "  note: %s\n", n)
	}
	return nil
}

// runMigrateVault executes the Phase 1b legacy-vault import (Issue #83) via
// migrate.Run: parses memory/global/learnings.md,
// memory/projects/*/MEMORY.md and agents/*/normalized/sessions.jsonl,
// dedups, seeds projects via Fingerprint, and reports counts. With
// --dry-run nothing is written; without it the same counts describe what
// the store adapter must insert.
func runMigrateVault(_ context.Context, cfg Config, args []string, stdout io.Writer) error {
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
