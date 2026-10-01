package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Audit coverage for `nexus migrate` (Issue #83): flag parsing with
// --vault/--dry-run and wiring to migrate.Run.
func TestAuditMigrateParseDefaults(t *testing.T) {
	o, err := parseMigrateArgs(nil)
	if err != nil {
		t.Fatalf("parseMigrateArgs(nil): %v", err)
	}
	if o.DryRun {
		t.Error("default dry-run should be false")
	}
	if strings.TrimSpace(o.Vault) == "" {
		t.Error("default vault should be non-empty (.central-memory under home)")
	}
	if !strings.Contains(strings.ToLower(o.Vault), ".central-memory") {
		t.Errorf("default vault should point at .central-memory, got %q", o.Vault)
	}
}

func TestAuditMigrateParseFlags(t *testing.T) {
	o, err := parseMigrateArgs([]string{"--vault", filepath.Join("x", "vault"), "--dry-run"})
	if err != nil {
		t.Fatalf("parseMigrateArgs: %v", err)
	}
	if !o.DryRun || o.Vault != filepath.Join("x", "vault") {
		t.Fatalf("flags not parsed: %+v", o)
	}
	if _, err := parseMigrateArgs([]string{"positional"}); err == nil {
		t.Error("migrate with positional must fail")
	}
}

func TestAuditMigrateRunDryRunCounts(t *testing.T) {
	vault := t.TempDir()
	mustWrite := func(rel, content string) {
		t.Helper()
		p := filepath.Join(vault, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("memory/global/learnings.md", "# L\n\n## 2026-09-01 10:00\ntags: a\n\nGlobal memory number one is long enough for import.\n")
	var out bytes.Buffer
	cfg := Config{}
	if err := runMigrate(context.Background(), cfg, []string{"--vault", vault, "--dry-run"}, &out); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	if !strings.Contains(out.String(), "dry-run") {
		t.Errorf("dry-run output should mention dry-run:\n%s", out.String())
	}
	var jout bytes.Buffer
	if err := runMigrate(context.Background(), cfg, []string{"--vault", vault, "--dry-run", "--json"}, &jout); err != nil {
		t.Fatalf("runMigrate --json: %v", err)
	}
	for _, want := range []string{"dry_run", "memories", "vault"} {
		if !strings.Contains(jout.String(), want) {
			t.Errorf("json output missing %q:\n%s", want, jout.String())
		}
	}
	if err := runMigrate(context.Background(), cfg, []string{"--vault", filepath.Join(vault, "nope"), "--dry-run"}, &bytes.Buffer{}); err == nil {
		t.Error("missing vault must error")
	}
}

func TestAuditMigrateFirstrunImport(t *testing.T) {
	srcRoot := t.TempDir()
	cm := filepath.Join(srcRoot, ".central-memory")
	if err := os.MkdirAll(cm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cm, "offsets.json"), []byte(`{"f":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "run.txt")
	if err := os.WriteFile(fake, []byte("X=C:\\bin\\nexus-daemon.exe\nKeep=ok.exe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS_FIRSTRUN_AUTOSTART_FILE", fake)

	outbox := t.TempDir()
	var out bytes.Buffer
	args := []string{
		"firstrun",
		"--src", srcRoot,
		"--outbox", outbox,
		"--clear-autostart",
	}
	if err := runMigrate(context.Background(), Config{}, args, &out); err != nil {
		t.Fatalf("runMigrate firstrun: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "copied:") || !strings.Contains(s, "offsets.json") {
		t.Errorf("output missing import details:\n%s", s)
	}
	if !strings.Contains(s, ":7272") {
		t.Errorf("should note not to recommend :7272:\n%s", s)
	}
	copied := filepath.Join(outbox, "migrated-daemon", "offsets.json")
	if _, err := os.Stat(copied); err != nil {
		t.Fatalf("expected imported file at %s: %v", copied, err)
	}
	raw, _ := os.ReadFile(fake)
	if strings.Contains(strings.ToLower(string(raw)), "nexus-daemon") {
		t.Fatalf("autostart not cleared:\n%s", raw)
	}

	if err := os.WriteFile(fake, []byte("X=nexus-daemon.exe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var jout bytes.Buffer
	jsonArgs := []string{"firstrun", "--src", srcRoot, "--outbox", outbox, "--json"}
	if err := runMigrate(context.Background(), Config{}, jsonArgs, &jout); err != nil {
		t.Fatalf("firstrun --json: %v", err)
	}
	for _, want := range []string{"autostart", "import", "copied_files"} {
		if !strings.Contains(jout.String(), want) {
			t.Errorf("json missing %q:\n%s", want, jout.String())
		}
	}
}

func TestParseMigrateFirstrunArgs(t *testing.T) {
	o, err := parseMigrateFirstrunArgs([]string{"--src", "/tmp/v", "--outbox", "/tmp/o", "--clear-autostart"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Src != "/tmp/v" || o.Outbox != "/tmp/o" || !o.ClearAutostart {
		t.Fatalf("parse: %+v", o)
	}
	if _, err := parseMigrateFirstrunArgs([]string{"extra"}); err == nil {
		t.Error("positional must fail")
	}
}
