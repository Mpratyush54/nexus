package server

import (
	"strings"
	"testing"

	"central-memory/internal/store"
)

func TestCapturedTurnOverviewIsLabelledAndReadable(t *testing.T) {
	got := capturedTurnOverview([]store.SessionTurn{
		{Role: "user", TextPreview: "  Fix the   capture pipeline\n for OpenCode  "},
		{Role: "assistant", TextPreview: "Investigating"},
		{Role: "assistant", TextPreview: "The capture path now separates sessions."},
	})
	for _, want := range []string{"from captured chat", "Fix the capture pipeline for OpenCode", "The capture path now separates sessions."} {
		if !strings.Contains(got, want) {
			t.Fatalf("overview %q missing %q", got, want)
		}
	}
	if got := capturedTurnOverview(nil); got != "" {
		t.Fatalf("empty overview: %q", got)
	}
}
