package store

import (
	"testing"
)

func TestProvenanceCRUDMem(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	sid := "11111111-1111-1111-1111-111111111111"
	pid := "22222222-2222-2222-2222-222222222222"

	if err := m.InsertFileOperation(ctx, FileOperation{
		ProjectID: pid, SessionID: sid, Harness: "antigravity", ToolName: "view_file",
		FilePath: "a.go", OpType: "read", TurnIndex: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.InsertBatchFileOperations(ctx, []FileOperation{{
		ProjectID: pid, SessionID: sid, Harness: "antigravity", ToolName: "write_to_file",
		FilePath: "b.go", OpType: "create", TurnIndex: 2,
	}}); err != nil {
		t.Fatal(err)
	}
	ops, err := m.ListFileOperations(ctx, sid, FileOpListOpts{OpType: "create"})
	if err != nil || len(ops) != 1 {
		t.Fatalf("ops=%v err=%v", ops, err)
	}
	code := 0
	if err := m.InsertToolExecution(ctx, ToolExecution{
		ProjectID: pid, SessionID: sid, Harness: "antigravity", ToolName: "run_command",
		CommandLine: "go test", ExitCode: &code, OutputSnippet: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	tex, err := m.ListToolExecutions(ctx, sid)
	if err != nil || len(tex) != 1 {
		t.Fatalf("tex=%v err=%v", tex, err)
	}

	for i := 0; i < 7; i++ {
		snap := &SessionSnapshot{
			SessionID: sid, ProjectID: pid, Harness: "antigravity",
			ConversationID: "c1", TurnCount: i + 1,
			TranscriptPayload: []byte("gz"),
		}
		if err := m.UpsertSessionSnapshot(ctx, snap); err != nil {
			t.Fatal(err)
		}
		if snap.SnapshotVersion != i+1 {
			t.Fatalf("version=%d want %d", snap.SnapshotVersion, i+1)
		}
	}
	latest, err := m.GetLatestSnapshot(ctx, sid)
	if err != nil || latest.SnapshotVersion != 7 {
		t.Fatalf("latest=%v err=%v", latest, err)
	}
	if err := m.PruneOldSnapshots(ctx, sid, 5); err != nil {
		t.Fatal(err)
	}
	list, err := m.ListSnapshotsForProject(ctx, pid, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	// After prune, latest version among remaining should still be 7.
	latest, err = m.GetLatestSnapshot(ctx, sid)
	if err != nil || latest.SnapshotVersion != 7 {
		t.Fatalf("after prune latest=%v", latest)
	}
}
