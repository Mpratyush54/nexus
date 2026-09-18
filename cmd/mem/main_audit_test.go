package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"central-memory/internal/mcp"
	"central-memory/internal/store"
)

// Audit coverage for cmd/mem/main.go: DTO fidelity, usage(), ResolveProject
// folder-only wiring, and the mcpStore adapter echo semantics.

func auditCaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func TestAuditMemUsageOutput(t *testing.T) {
	out := auditCaptureStdout(t, usage)
	for _, want := range []string{"mem status", "mem projects", "mem mcp", "nexus"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage() missing %q:\n%s", want, out)
		}
	}
}

// FIXED (#116 struct-clobber): Create must copy back only store-minted
// fields (ID/status/defaults), preserving caller-supplied content verbatim.
func TestAuditMemCreatePreservesCallerFields(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemStore()
	p, err := mem.ResolveProject(ctx, "", "", "clobber-proj")
	if err != nil {
		t.Fatalf("ResolveProject: %v", err)
	}
	s := mcpStore{mem: mem}
	item := &mcp.MemoryItem{
		Key: "testing/framework", Content: "The team uses pytest with fixtures for all integration tests here.",
		Level: "project", ProjectID: p.ID, Tags: []string{"testing"}, Source: "cli",
	}
	origKey, origContent := item.Key, item.Content
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatalf("CreateMemoryItem: %v", err)
	}
	if item.Key != origKey || item.Content != origContent {
		t.Fatalf("clobber: caller fields overwritten: %+v", item)
	}
	if item.ID == "" || item.Status != "PROPOSED" {
		t.Fatalf("minted fields not echoed: %+v", item)
	}
}

func TestAuditMemMemoryDTOFieldFidelity(t *testing.T) {
	src := &store.MemoryItem{
		ID: "mem_1", ProjectID: "proj_1", Key: "testing/framework",
		Content:        "The team uses pytest with fixtures for all integration tests here.",
		ContextSnippet: "ctx", Level: "project", Scope: "decision",
		Tags: []string{"testing"}, Confidence: 0.9, Status: "PROPOSED", Source: "cli",
	}
	got := memoryDTO(src)
	if got.ID != src.ID || got.ProjectID != src.ProjectID || got.Key != src.Key ||
		got.Content != src.Content || got.ContextSnippet != src.ContextSnippet ||
		got.Level != src.Level || got.Scope != src.Scope || got.Status != src.Status ||
		got.Source != src.Source || got.Confidence != src.Confidence {
		t.Fatalf("memoryDTO dropped a mapped field: %+v", got)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "testing" {
		t.Fatalf("memoryDTO tags wrong: %+v", got.Tags)
	}
}

// TODO(dto-drop): memoryDTO silently drops SessionID and UseCount (plus
// UserID/OrgID/Embedding/ProposedBy/ConfirmedBy/...). Two store rows that
// differ ONLY in dropped fields map to identical MCP DTOs — data loss at
// the adapter boundary. Either widen mcp.MemoryItem or document the drop.
func TestAuditMemMemoryDTODropsSessionIDUseCount(t *testing.T) {
	base := &store.MemoryItem{
		ID: "mem_x", ProjectID: "p", Key: "k",
		Content: "The team uses pytest with fixtures for all integration tests here.",
		Level:   "project", Status: "PROPOSED",
	}
	a := *base
	a.SessionID = "sess_A"
	a.UseCount = 3
	b := *base
	b.SessionID = "sess_B"
	b.UseCount = 99
	da, db := memoryDTO(&a), memoryDTO(&b)
	if !reflect.DeepEqual(da, db) {
		t.Fatalf("expected identical DTOs when only dropped fields differ:\n%+v\n%+v", da, db)
	}
	var _ = mcp.MemoryItem{}.ID // pin the DTO type under test
}

func TestAuditMemEpisodeDTOFieldFidelity(t *testing.T) {
	src := &store.Episode{
		ID: "ep_1", ProjectID: "proj_1", Title: "Auth timeout",
		EpisodeType: "bug_fix", Trigger: "ConnectionTimeout",
		RootCause: "deadline", Resolution: "raise deadline",
		Verification: "go test ./...", Tags: []string{"auth"},
		FilesInvolved: []string{"ws.go"}, ErrorPatterns: []string{"Timeout"},
		Status: "OPEN",
	}
	got := episodeDTO(src)
	if got.ID != src.ID || got.ProjectID != src.ProjectID || got.Title != src.Title ||
		got.EpisodeType != src.EpisodeType || got.Trigger != src.Trigger ||
		got.RootCause != src.RootCause || got.Resolution != src.Resolution ||
		got.Verification != src.Verification || got.Status != src.Status {
		t.Fatalf("episodeDTO dropped a mapped field: %+v", got)
	}
}

// TODO(dto-drop): episodeDTO silently drops Investigation and SessionID
// (plus CreatedBy/ResolvedBy/OpenedAt/ResolvedAt). Same data-loss shape as
// memoryDTO — lock in current behavior until the DTO is widened.
func TestAuditMemEpisodeDTODropsInvestigationSessionID(t *testing.T) {
	base := &store.Episode{
		ID: "ep_x", ProjectID: "p", Title: "Auth timeout on WebSocket upgrade",
		EpisodeType: "bug_fix", Status: "OPEN",
	}
	a := *base
	a.Investigation = "traced to handshake deadline"
	a.SessionID = "sess_A"
	b := *base
	b.Investigation = "entirely different notes"
	b.SessionID = "sess_B"
	da, db := episodeDTO(&a), episodeDTO(&b)
	if !reflect.DeepEqual(da, db) {
		t.Fatalf("expected identical DTOs when only dropped fields differ:\n%+v\n%+v", da, db)
	}
}

// ResolveProject folder-only path: cmdMCP resolves with ("", "", basedir).
func TestAuditMemResolveProjectFolderOnly(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemStore()
	p, err := mem.ResolveProject(ctx, "", "", "audit-folder-only")
	if err != nil {
		t.Fatalf("ResolveProject: %v", err)
	}
	if p.ID == "" || p.FolderName != "audit-folder-only" {
		t.Fatalf("unexpected project: %+v", p)
	}
	again, err := mem.ResolveProject(ctx, "", "", "audit-folder-only")
	if err != nil {
		t.Fatalf("ResolveProject again: %v", err)
	}
	if again.ID != p.ID {
		t.Fatalf("folder-only resolve not idempotent: %q vs %q", again.ID, p.ID)
	}
}

// mcpStore adapter echo: Create assigns server-side ID/status back onto the
// caller's item (write-back semantics, not copy-out).
func TestAuditMemMcpStoreCreateEcho(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemStore()
	if _, err := mem.ResolveProject(ctx, "", "", "echo-proj"); err != nil {
		t.Fatalf("ResolveProject: %v", err)
	}
	s := mcpStore{mem: mem}
	item := &mcp.MemoryItem{
		Key: "testing/framework", Content: "The team uses pytest with fixtures for all integration tests here.",
		Level: "project",
	}
	// Resolve the project ID the MemStore actually uses.
	p, _ := mem.ResolveProject(ctx, "", "", "echo-proj")
	item.ProjectID = p.ID
	if err := s.CreateMemoryItem(ctx, item); err != nil {
		t.Fatalf("CreateMemoryItem: %v", err)
	}
	if item.ID == "" {
		t.Fatal("adapter must echo assigned ID back onto item")
	}
	if item.Status != "PROPOSED" {
		t.Fatalf("adapter must echo default status, got %q", item.Status)
	}

	got, err := s.SearchMemory(ctx, p.ID, "pytest", nil, 10)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(got) == 0 || got[0].ID != item.ID {
		t.Fatalf("SearchMemory must return the created row, got %+v", got)
	}

	ep := &mcp.Episode{ProjectID: p.ID, Title: "Auth timeout", EpisodeType: "bug_fix"}
	if err := s.CreateEpisode(ctx, ep); err != nil {
		t.Fatalf("CreateEpisode: %v", err)
	}
	if ep.ID == "" || ep.Status != "OPEN" {
		t.Fatalf("episode echo wrong: %+v", ep)
	}
	eps, err := s.SearchEpisodes(ctx, p.ID, "", "timeout", 10)
	if err != nil {
		t.Fatalf("SearchEpisodes: %v", err)
	}
	if len(eps) == 0 {
		t.Fatal("SearchEpisodes must return the created episode")
	}
}

func TestAuditMemWorkspaceProjectMapping(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemStore()
	p, err := mem.ResolveProject(ctx, "", "", "map-proj")
	if err != nil {
		t.Fatalf("ResolveProject: %v", err)
	}
	ws := &store.Workspace{ProjectID: p.ID, MachineID: "m1", Path: "/tmp/map-proj", Branch: "main"}
	if err := mem.RegisterWorkspace(ctx, ws); err != nil {
		t.Fatalf("RegisterWorkspace: %v", err)
	}
	s := mcpStore{mem: mem}
	got, err := s.GetActiveWorkspace(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetActiveWorkspace: %v", err)
	}
	if got.Branch != "main" || got.Path != "/tmp/map-proj" {
		t.Fatalf("workspace mapping wrong: %+v", got)
	}
	gp, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if gp.FolderName != "map-proj" {
		t.Fatalf("project mapping wrong: %+v", gp)
	}
}

func TestAuditMemCmdStatusProjects(t *testing.T) {
	if err := cmdStatus(); err != nil {
		t.Fatalf("cmdStatus: %v", err)
	}
	if err := cmdProjects(); err != nil {
		t.Fatalf("cmdProjects: %v", err)
	}
}
