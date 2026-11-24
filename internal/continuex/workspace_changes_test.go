package continuex

import "testing"

func TestWorkspaceChangesIncludesNewUpdatedAndRemovedPaths(t *testing.T) {
	before := map[string]string{"dirty.go": "old", "removed.go": "old"}
	after := map[string]string{"dirty.go": "new", "added.go": "new"}
	got := workspaceChanges(before, after)
	if len(got) != 3 || got[0] != "added.go" || got[1] != "dirty.go" || got[2] != "removed.go" {
		t.Fatalf("changes=%v", got)
	}
	if workspaceChanges(nil, after) != nil {
		t.Fatal("an unavailable baseline must not attribute every dirty file to the agent")
	}
}
