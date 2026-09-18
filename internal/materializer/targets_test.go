package materializer

// AddTarget validation tests (nexus issue #41): invalid push targets fail
// at registration, before they can break Regenerate.

import (
	"testing"
)

func TestAddTargetValidation(t *testing.T) {
	m := &Materializer{}
	if err := m.AddTarget(Target{AgentName: "copilot", FilePath: ".github/copilot-instructions.md", Format: "markdown", Budget: 8000}); err != nil {
		t.Fatalf("valid target: %v", err)
	}
	if len(m.Targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(m.Targets))
	}
	// Re-adding the same path replaces (per-project override).
	if err := m.AddTarget(Target{AgentName: "copilot", FilePath: ".github/copilot-instructions.md", Format: "markdown", Budget: 4000}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(m.Targets) != 1 || m.Targets[0].Budget != 4000 {
		t.Fatalf("replace must update in place: %+v", m.Targets)
	}

	bad := []Target{
		{FilePath: "x.md", Format: "markdown", Budget: 100},                     // blank agent
		{AgentName: "x", Format: "markdown", Budget: 100},                       // blank path
		{AgentName: "x", FilePath: "/abs/path.md", Format: "text", Budget: 100}, // absolute path
		{AgentName: "x", FilePath: "x.md", Format: "yaml", Budget: 100},         // bad format
		{AgentName: "x", FilePath: "x.md", Format: "text", Budget: 0},           // bad budget
		{AgentName: "x", FilePath: "x.md", Format: "text", Budget: -5},          // bad budget
	}
	for i, tgt := range bad {
		if err := m.AddTarget(tgt); err == nil {
			t.Errorf("case %d (%+v): want error", i, tgt)
		}
	}
	if len(m.Targets) != 1 {
		t.Fatalf("targets = %d, want 1 (rejections add nothing)", len(m.Targets))
	}
}
