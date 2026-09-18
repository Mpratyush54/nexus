package adapters

import (
	"encoding/json"
	"testing"
)

func TestAuditClassificationValuesDistinct(t *testing.T) {
	if Backup == Ignore || Backup == Never || Ignore == Never {
		t.Fatalf("classification constants must be distinct: backup=%d ignore=%d never=%d", Backup, Ignore, Never)
	}
	if Backup != 0 {
		t.Errorf("Backup should be zero value, got %d", Backup)
	}
}

func TestAuditArtifactZeroValue(t *testing.T) {
	var a Artifact
	if a.Agent != "" || a.Kind != "" || a.NativePath != "" || a.RawPath != "" || a.Project != "" || a.Was != "" || a.Repo != "" || a.Root != "" {
		t.Errorf("zero Artifact should have all empty fields, got %+v", a)
	}
}

func TestAuditArtifactFieldsRoundTrip(t *testing.T) {
	a := Artifact{
		Agent: "claude", Kind: "session", NativePath: `C:\x\y.jsonl`,
		RawPath: `V:\agents\claude\raw\0\y.jsonl`, Project: "a/b",
		Was: "stale", Repo: "https://example.com/r.git", Root: "abc123",
	}
	// Real round-trip (issue #139): Artifacts serialize through index.json
	// (Export) and back (Restore/migrate), so the test must prove the
	// JSON mapping preserves every field — not just struct assignment.
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Artifact
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back != a {
		t.Errorf("JSON round-trip lost fields:\n got %+v\nwant %+v", back, a)
	}
}

func TestAuditAdapterInterfaceCompliance(t *testing.T) {
	var _ Adapter = genericAdapter{}
	var _ Adapter = genericAdapter{name: "x"}
	regs := Registry()
	if len(regs) == 0 {
		t.Fatal("Registry() returned no adapters")
	}
	for _, a := range regs {
		var _ Adapter = a
		if a.Name() == "" {
			t.Errorf("adapter has empty name: %#v", a)
		}
	}
}

func TestAuditRegistryNamesUnique(t *testing.T) {
	seen := map[string]int{}
	for _, a := range Registry() {
		seen[a.Name()]++
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("adapter name %q appears %d times, want 1", name, n)
		}
	}
}
