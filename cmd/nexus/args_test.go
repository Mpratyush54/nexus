package main

import (
	"testing"
)

func TestParseGlobalArgs(t *testing.T) {
	cfg := defaultConfig()
	cfg.ServerURL = "http://default:8080"
	rest, err := parseGlobalArgs([]string{"--server", "http://x:1", "--json", "-p", "proj1", "status"}, &cfg)
	if err != nil {
		t.Fatalf("parseGlobalArgs failed: %v", err)
	}
	if cfg.ServerURL != "http://x:1" || !cfg.JSON || cfg.ProjectID != "proj1" {
		t.Errorf("global flags not applied: %+v", cfg)
	}
	if len(rest) != 1 || rest[0] != "status" {
		t.Errorf("rest wrong: %v", rest)
	}
}

func TestParseGlobalArgsUnknownFlag(t *testing.T) {
	cfg := defaultConfig()
	if _, err := parseGlobalArgs([]string{"--nope"}, &cfg); err == nil {
		t.Error("expected error for unknown global flag")
	}
}

func TestParseMemorySearch(t *testing.T) {
	o, err := parseMemoryArgs([]string{"search", "-p", "proj1", "--level", "project", "--limit", "5", "redis caching"})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if o.Sub != "search" || o.Query != "redis caching" || o.Project != "proj1" || o.Level != "project" || o.Limit != 5 {
		t.Errorf("wrong parse: %+v", o)
	}
}

func TestParseMemorySearchMissingQuery(t *testing.T) {
	if _, err := parseMemoryArgs([]string{"search", "-p", "proj1"}); err == nil {
		t.Error("expected error for missing query")
	}
}

func TestParseMemoryPropose(t *testing.T) {
	o, err := parseMemoryArgs([]string{"propose", "-k", "testing/framework", "-p", "proj1", "The team uses pytest with fixtures for all integration tests here."})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if o.Key != "testing/framework" || o.Content == "" {
		t.Errorf("wrong parse: %+v", o)
	}
}

func TestParseMemoryProposeShortContent(t *testing.T) {
	if _, err := parseMemoryArgs([]string{"propose", "-k", "k", "too short"}); err == nil {
		t.Error("expected error for <20 char content (mirrors server CHECK)")
	}
}

func TestParseMemoryProposeMissingKey(t *testing.T) {
	if _, err := parseMemoryArgs([]string{"propose", "some fact content that is long enough to pass"}); err == nil {
		t.Error("expected error for missing -k")
	}
}

func TestParseMemoryConfirmReject(t *testing.T) {
	for _, sub := range []string{"confirm", "reject"} {
		o, err := parseMemoryArgs([]string{sub, "mem_123"})
		if err != nil {
			t.Fatalf("%s parse failed: %v", sub, err)
		}
		if o.ID != "mem_123" {
			t.Errorf("%s id wrong: %+v", sub, o)
		}
		if _, err := parseMemoryArgs([]string{sub}); err == nil {
			t.Errorf("%s without id should fail", sub)
		}
	}
}

func TestParseMemoryUnknownSub(t *testing.T) {
	if _, err := parseMemoryArgs([]string{"delete", "x"}); err == nil {
		t.Error("expected error for unknown memory subcommand")
	}
	if _, err := parseMemoryArgs(nil); err == nil {
		t.Error("expected error for missing memory subcommand")
	}
}

func TestParseSessionArgs(t *testing.T) {
	o, err := parseSessionArgs([]string{"list", "-p", "proj1"})
	if err != nil || o.Sub != "list" || o.Project != "proj1" {
		t.Errorf("list parse wrong: %+v %v", o, err)
	}
	o, err = parseSessionArgs([]string{"create", "-p", "proj1", "Auth refactor"})
	if err != nil || o.Title != "Auth refactor" {
		t.Errorf("create parse wrong: %+v %v", o, err)
	}
	if _, err := parseSessionArgs([]string{"create"}); err == nil {
		t.Error("create without title should fail")
	}
	o, err = parseSessionArgs([]string{"join", "sess_1"})
	if err != nil || o.ID != "sess_1" {
		t.Errorf("join parse wrong: %+v %v", o, err)
	}
	if _, err := parseSessionArgs([]string{"join"}); err == nil {
		t.Error("join without id should fail")
	}
	if _, err := parseSessionArgs([]string{"bogus"}); err == nil {
		t.Error("unknown session subcommand should fail")
	}
}

func TestParseBranchArgs(t *testing.T) {
	o, err := parseBranchArgs([]string{"fork", "-p", "proj1", "--from", "main", "bob-exp"})
	if err != nil || o.Name != "bob-exp" || o.From != "main" {
		t.Errorf("fork parse wrong: %+v %v", o, err)
	}
	o, err = parseBranchArgs([]string{"merge", "bob-exp", "main"})
	if err != nil || o.Name != "bob-exp" || o.Extra != "main" {
		t.Errorf("merge parse wrong: %+v %v", o, err)
	}
	o, err = parseBranchArgs([]string{"merge", "bob-exp"})
	if err != nil || o.Name != "bob-exp" || o.Extra != "" {
		t.Errorf("merge without target wrong: %+v %v", o, err)
	}
	o, err = parseBranchArgs([]string{"diff"})
	if err != nil || o.Sub != "diff" || o.Name != "" {
		t.Errorf("bare diff wrong: %+v %v", o, err)
	}
	o, err = parseBranchArgs([]string{"checkout", "main"})
	if err != nil || o.Name != "main" {
		t.Errorf("checkout parse wrong: %+v %v", o, err)
	}
	if _, err := parseBranchArgs([]string{"fork"}); err == nil {
		t.Error("fork without name should fail")
	}
	if _, err := parseBranchArgs([]string{"rebase", "x"}); err == nil {
		t.Error("unknown branch subcommand should fail")
	}
}

func TestParseEpisodeArgs(t *testing.T) {
	o, err := parseEpisodeArgs([]string{"list", "-p", "proj1", "--limit", "10"})
	if err != nil || o.Sub != "list" || o.Limit != 10 {
		t.Errorf("list parse wrong: %+v %v", o, err)
	}
	o, err = parseEpisodeArgs([]string{"search", "-p", "proj1", "ConnectionTimeout"})
	if err != nil || o.Query != "ConnectionTimeout" || o.ErrorPattern != "ConnectionTimeout" {
		t.Errorf("search parse wrong: %+v %v", o, err)
	}
	o, err = parseEpisodeArgs([]string{"search", "--error-pattern", "pgx.*exhausted", "timeout"})
	if err != nil || o.ErrorPattern != "pgx.*exhausted" || o.Query != "timeout" {
		t.Errorf("search --error-pattern wrong: %+v %v", o, err)
	}
	if _, err := parseEpisodeArgs([]string{"search"}); err == nil {
		t.Error("search without query should fail")
	}
	if _, err := parseEpisodeArgs([]string{"close", "x"}); err == nil {
		t.Error("unknown episode subcommand should fail")
	}
}

func TestParseStatusAndDoctorArgs(t *testing.T) {
	o, err := parseStatusArgs([]string{"-p", "proj1", "--json"})
	if err != nil || o.Project != "proj1" || !o.JSON {
		t.Errorf("status parse wrong: %+v %v", o, err)
	}
	if _, err := parseStatusArgs([]string{"extra"}); err == nil {
		t.Error("status with positional should fail")
	}
	j, err := parseDoctorArgs([]string{"--json"})
	if err != nil || !j {
		t.Errorf("doctor parse wrong: %v %v", j, err)
	}
	if _, err := parseDoctorArgs([]string{"extra"}); err == nil {
		t.Error("doctor with positional should fail")
	}
}
