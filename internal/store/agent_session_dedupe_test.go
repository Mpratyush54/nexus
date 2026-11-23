package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

// legacyDedupeTurns mirrors the shapes left by the old capture: undated
// assistant copies carrying every tool call of a message that is stored
// dated as a text turn plus one turn per call, and undated harness-only
// user envelopes.
func legacyDedupeTurns() []SessionTurn {
	at := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	ts := func(m int) *time.Time { v := at.Add(time.Duration(m) * time.Minute); return &v }
	read1 := `{"id":"toolu_1","name":"Read","input":{"path":"a.go"}}`
	read2 := `{"id":"toolu_2","name":"Read","input":{"path":"b.go"}}`
	return []SessionTurn{
		{Idx: 1, Role: "user", TextPreview: "fix it", SourceAt: ts(0)},
		{Idx: 2, Role: "assistant", TextPreview: "Reading the files.", SourceAt: ts(1)},
		{Idx: 3, Role: "assistant", ToolCalls: json.RawMessage(`[` + read1 + `]`), SourceAt: ts(2)},
		{Idx: 4, Role: "assistant", ToolCalls: json.RawMessage(`[` + read2 + `]`), SourceAt: ts(3)},
		{Idx: 5, Role: "user", TextPreview: "fix it"},                                                                                  // exact
		{Idx: 6, Role: "assistant", TextPreview: " Reading the files.\n", ToolCalls: json.RawMessage(`[` + read2 + `,` + read1 + `]`)}, // partial: calls on dated turns
		{Idx: 7, Role: "assistant", TextPreview: "Reading the files. ", ToolCalls: json.RawMessage(`null`)},                            // partial: no calls
		{Idx: 8, Role: "assistant", TextPreview: "Reading the files.", ToolCalls: json.RawMessage(`[{"id":"toolu_9","name":"Bash"}]`)}, // call not stored dated
		{Idx: 9, Role: "user", TextPreview: "<system-reminder>\nctx\n</system-reminder>\n\n<task-notification>\n<status>done</status>\n</task-notification>\n"},
		{Idx: 10, Role: "user", TextPreview: "<system-reminder>ctx</system-reminder>\nplease fix the bug"}, // real text
		{Idx: 11, Role: "user", TextPreview: "<system-reminder>ctx</system-reminder>", SourceAt: ts(4)},    // dated
		{Idx: 12, Role: "user", TextPreview: "<system-reminder>\ncut off…"},                                // truncated
		{Idx: 13, Role: "user", TextPreview: `<agent-message from="a1">done</agent-message>`},
		{Idx: 14, Role: "system", TextPreview: "fix it"},                                  // different role
		{Idx: 15, Role: "assistant", TextPreview: "<system-reminder>x</system-reminder>"}, // not a user turn
		{Idx: 16, Role: "user", TextPreview: "<system-reminders>x</system-reminders>"},    // not an envelope tag
		{Idx: 17, Role: "user", TextPreview: "please <system-reminder>x</system-reminder>"},
	}
}

func TestIsHarnessEnvelope(t *testing.T) {
	for text, want := range map[string]bool{
		"<system-reminder>a</system-reminder>":                                          true,
		"  <system-reminder>a</system-reminder>\n<system-reminder>b</system-reminder> ": true,
		"<task-notification><task-id>1</task-id></task-notification>":                   true,
		"<agent-message from=\"x\">hi</agent-message>":                                  true,
		"<system-reminder>a</system-reminder> hello":                                    false,
		"hello <system-reminder>a</system-reminder>":                                    false,
		"<system-reminder>a":                     false,
		"<system-reminderx>a</system-reminderx>": false,
		"":                                       false,
		"<agent-message":                         false,
	} {
		if got := isHarnessEnvelope(text); got != want {
			t.Errorf("isHarnessEnvelope(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestDuplicateTurnsModes(t *testing.T) {
	turns := legacyDedupeTurns()
	for mode, want := range map[string]map[int]string{
		DedupeModeAll: {5: DedupeRuleExact, 6: DedupeRulePartialCopy, 7: DedupeRulePartialCopy,
			9: DedupeRuleEnvelope, 13: DedupeRuleEnvelope},
		DedupeModeExact:     {5: DedupeRuleExact},
		DedupeModeCopies:    {5: DedupeRuleExact, 6: DedupeRulePartialCopy, 7: DedupeRulePartialCopy},
		DedupeModeEnvelopes: {9: DedupeRuleEnvelope, 13: DedupeRuleEnvelope},
	} {
		if got := duplicateTurns(turns, mode); !reflect.DeepEqual(got, want) {
			t.Errorf("mode %s: got %v, want %v", mode, got, want)
		}
	}
	if _, err := NormalizeDedupeMode("everything"); !errors.Is(err, ErrBadDedupeMode) {
		t.Fatalf("bad mode: %v", err)
	}
	if m, err := NormalizeDedupeMode(" "); err != nil || m != DedupeModeAll {
		t.Fatalf("default mode = %q %v", m, err)
	}
}

func TestMemStoreDedupeLegacyRows(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	project, err := m.ResolveProject(ctx, "", "", "mem-dedupe-legacy")
	if err != nil {
		t.Fatal(err)
	}
	row, err := m.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: "alice", Harness: "claude", NativeID: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range legacyDedupeTurns() {
		turn.SessionID = row.ID
		if err := m.AppendSessionTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.DedupeSessionTurns(ctx, row.ID, TurnDedupeOptions{Mode: "nope"}); !errors.Is(err, ErrBadDedupeMode) {
		t.Fatalf("bad mode: %v", err)
	}
	dry, err := m.DedupeSessionTurns(ctx, row.ID, TurnDedupeOptions{DryRun: true})
	if err != nil || dry.Mode != DedupeModeAll || dry.Removed != 5 || dry.Kept != 12 || !dry.DryRun ||
		!reflect.DeepEqual(dry.RemovedIdx, []int{5, 6, 7, 9, 13}) ||
		!reflect.DeepEqual(dry.Rules, map[string]int{DedupeRuleExact: 1, DedupeRulePartialCopy: 2, DedupeRuleEnvelope: 2}) {
		t.Fatalf("dry run = %+v %v", dry, err)
	}
	if got, _ := m.ListSessionTurns(ctx, row.ID); len(got) != 17 {
		t.Fatalf("dry run removed turns: %d", len(got))
	}
	env, err := m.DedupeSessionTurns(ctx, row.ID, TurnDedupeOptions{Mode: DedupeModeEnvelopes})
	if err != nil || env.Removed != 2 || env.Rules[DedupeRuleEnvelope] != 2 || env.Rules[DedupeRuleExact] != 0 {
		t.Fatalf("envelopes = %+v %v", env, err)
	}
	res, err := m.DedupeSessionTurns(ctx, row.ID, TurnDedupeOptions{})
	if err != nil || res.Removed != 3 || res.Kept != 12 || !reflect.DeepEqual(res.RemovedIdx, []int{5, 6, 7}) {
		t.Fatalf("dedupe = %+v %v", res, err)
	}
	got, _ := m.ListSessionTurns(ctx, row.ID)
	var idx []int
	for _, turn := range got {
		idx = append(idx, turn.Idx)
	}
	if !reflect.DeepEqual(idx, []int{1, 2, 3, 4, 8, 10, 11, 12, 14, 15, 16, 17}) {
		t.Fatalf("kept = %v", idx)
	}
}

func TestDedupeSessionTurnsPostgresLegacyRows(t *testing.T) {
	s, owner, projectID := deleteTestPG(t)
	ctx := t.Context()
	row, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: projectID, OwnerUserID: owner, Harness: "claude", NativeID: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range legacyDedupeTurns() {
		turn.SessionID = row.ID
		if err := s.AppendSessionTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateSessionVersion(ctx, row.ID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DedupeSessionTurns(ctx, row.ID, TurnDedupeOptions{Mode: "nope"}); !errors.Is(err, ErrBadDedupeMode) {
		t.Fatalf("bad mode: %v", err)
	}
	dry, err := s.DedupeSessionTurns(ctx, row.ID, TurnDedupeOptions{DryRun: true})
	if err != nil || dry.Removed != 5 || dry.Kept != 12 ||
		!reflect.DeepEqual(dry.RemovedIdx, []int{5, 6, 7, 9, 13}) ||
		!reflect.DeepEqual(dry.Rules, map[string]int{DedupeRuleExact: 1, DedupeRulePartialCopy: 2, DedupeRuleEnvelope: 2}) {
		t.Fatalf("dry run = %+v %v", dry, err)
	}
	if turns, _ := s.ListSessionTurns(ctx, row.ID); len(turns) != 17 {
		t.Fatalf("dry run deleted: %d", len(turns))
	}
	copies, err := s.DedupeSessionTurns(ctx, row.ID, TurnDedupeOptions{Mode: DedupeModeCopies})
	if err != nil || copies.Removed != 3 || copies.Rules[DedupeRulePartialCopy] != 2 || copies.Rules[DedupeRuleEnvelope] != 0 {
		t.Fatalf("copies = %+v %v", copies, err)
	}
	res, err := s.DedupeSessionTurns(ctx, row.ID, TurnDedupeOptions{})
	if err != nil || res.Removed != 2 || res.Kept != 12 || !reflect.DeepEqual(res.RemovedIdx, []int{9, 13}) {
		t.Fatalf("dedupe = %+v %v", res, err)
	}
	turns, err := s.ListSessionTurns(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	var idx []int
	for _, turn := range turns {
		idx = append(idx, turn.Idx)
	}
	if !reflect.DeepEqual(idx, []int{1, 2, 3, 4, 8, 10, 11, 12, 14, 15, 16, 17}) {
		t.Fatalf("kept = %v", idx)
	}
	var versionTurns int
	if err := s.pool.QueryRow(ctx, `SELECT MAX(turn_count) FROM session_versions WHERE session_id = $1::uuid`, row.ID).Scan(&versionTurns); err != nil || versionTurns > 12 {
		t.Fatalf("version turn_count = %d %v", versionTurns, err)
	}
}
