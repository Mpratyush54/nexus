package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestLiveVsPointInTimeGrants(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	proj, err := m.ResolveProject(ctx, "", "", "pit-share")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := m.UpsertAgentSession(ctx, &AgentSession{
		ProjectID: proj.ID, OwnerUserID: "owner", Harness: "claude",
		NativeID: "ses_live_pit", OriginMachineID: "m1",
	})
	if err != nil {
		t.Fatal(err)
	}

	body1 := []byte("version-one-content")
	sum1 := sha256.Sum256(body1)
	hash1 := hex.EncodeToString(sum1[:])
	if err := m.PutBlob(ctx, proj.ID, hash1, "plain", "transcript", body1); err != nil {
		t.Fatal(err)
	}
	v1, err := m.CreateSessionVersion(ctx, parent.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	man1, _ := json.Marshal(map[string]any{"transcript": map[string]string{"blob": "sha256:" + hash1}})
	if _, err := m.CompleteSessionVersion(ctx, parent.ID, v1.Version, man1); err != nil {
		t.Fatal(err)
	}

	if err := m.GrantAgentSessionOpts(ctx, parent.ID, "pit-user", "owner", SessionGrantOpts{
		Live: false, VersionID: v1.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.GrantAgentSessionOpts(ctx, parent.ID, "live-user", "owner", SessionGrantOpts{Live: true}); err != nil {
		t.Fatal(err)
	}

	body2 := []byte("version-two-newer")
	sum2 := sha256.Sum256(body2)
	hash2 := hex.EncodeToString(sum2[:])
	if err := m.PutBlob(ctx, proj.ID, hash2, "plain", "transcript", body2); err != nil {
		t.Fatal(err)
	}
	v2, err := m.CreateSessionVersion(ctx, parent.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	man2, _ := json.Marshal(map[string]any{"transcript": map[string]string{"blob": "sha256:" + hash2}})
	if _, err := m.CompleteSessionVersion(ctx, parent.ID, v2.Version, man2); err != nil {
		t.Fatal(err)
	}

	pit, err := m.ListVisibleSessionVersions(ctx, parent.ID, "pit-user")
	if err != nil {
		t.Fatal(err)
	}
	if len(pit) != 1 || pit[0].ID != v1.ID {
		t.Fatalf("PIT should see only v1, got %+v", pit)
	}
	if string(pit[0].Manifest) != string(man1) {
		t.Fatalf("PIT saw wrong content: %s", pit[0].Manifest)
	}

	live, err := m.ListVisibleSessionVersions(ctx, parent.ID, "live-user")
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 {
		t.Fatalf("live should see both versions, got %+v", live)
	}
	sawNew := false
	for _, v := range live {
		if v.ID == v2.ID {
			sawNew = true
			if string(v.Manifest) != string(man2) {
				t.Fatalf("live missing new content: %s", v.Manifest)
			}
		}
	}
	if !sawNew {
		t.Fatal("live grant did not see newer complete version")
	}

	if err := m.GrantAgentSessionOpts(ctx, parent.ID, "bad", "owner", SessionGrantOpts{Live: false}); err == nil {
		t.Fatal("PIT without version_id should fail")
	}
}

func TestForkAgentSessionTree(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	proj, err := m.ResolveProject(ctx, "", "", "fork-tree")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := m.UpsertAgentSession(ctx, &AgentSession{
		ProjectID: proj.ID, OwnerUserID: "alice", Harness: "claude",
		NativeID: "ses_root", OriginMachineID: "m1",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := m.ForkAgentSession(ctx, parent.ID, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentSessionID != parent.ID || child.LineageKind != "fork" {
		t.Fatalf("child lineage: %+v", child)
	}
	if child.OwnerUserID != "bob" || child.ProjectID != parent.ProjectID {
		t.Fatalf("child ownership: %+v", child)
	}
	forks, err := m.ListAgentSessionForks(ctx, parent.ID)
	if err != nil || len(forks) != 1 || forks[0].ID != child.ID {
		t.Fatalf("forks=%+v err=%v", forks, err)
	}
}

func TestMarkAgentSessionCodeMerged(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	proj, err := m.ResolveProject(ctx, "", "", "merge-marker")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := m.UpsertAgentSession(ctx, &AgentSession{
		ProjectID: proj.ID, OwnerUserID: "alice", Harness: "claude",
		NativeID: "ses_merge_parent", OriginMachineID: "m1",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := m.ForkAgentSession(ctx, parent.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	marked, err := m.MarkAgentSessionCodeMerged(ctx, child.ID, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if marked.CodeMergedInto != parent.ID || marked.CodeMergedAt == nil {
		t.Fatalf("marker: %+v", marked)
	}
	got, err := m.GetAgentSession(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CodeMergedInto != parent.ID {
		t.Fatalf("persisted marker: %+v", got)
	}
	// No extra session minted.
	forks, err := m.ListAgentSessionForks(ctx, parent.ID)
	if err != nil || len(forks) != 1 {
		t.Fatalf("forks=%+v err=%v", forks, err)
	}
}
