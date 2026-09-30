package memoryact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMemoryPromotePublicStatus(t *testing.T) {
	cases := map[string]string{
		"PROPOSED":   PublicActive,
		"CONFIRMED":  PublicActive,
		"proposed":   PublicActive,
		"confirmed":  PublicActive,
		"ACTIVE":     PublicActive,
		"active":     PublicActive,
		"":           PublicActive,
		"SUPERSEDED": PublicSuperseded,
		"superseded": PublicSuperseded,
		"forgotten":  PublicForgotten,
		"FORGOTTEN":  PublicForgotten,
		"REJECTED":   PublicForgotten,
		"rejected":   PublicForgotten,
	}
	for in, want := range cases {
		if got := PublicStatus(in); got != want {
			t.Errorf("PublicStatus(%q) = %q, want %q", in, got, want)
		}
		if got := PublicStatus(in); got == "PROPOSED" || got == "CONFIRMED" {
			t.Errorf("PublicStatus(%q) leaked %q", in, got)
		}
	}

	org, ok := CanonicalLevel("Team")
	if !ok || org != "organization" {
		t.Fatalf("Team -> %q ok=%v", org, ok)
	}
	if _, ok := CanonicalLevel("ephemeral"); ok {
		t.Fatal("ephemeral must be rejected")
	}
	if _, ok := CanonicalLevel("nope"); ok {
		t.Fatal("unknown level must be rejected")
	}
}

func TestMemoryPromoteProvenance(t *testing.T) {
	p := Provenance{
		SourceSessionID:     "sess-private-9",
		SourceOwnerID:       "user-ada",
		PromotedFromPrivate: true,
	}
	owner := p.ForOwner()
	if owner.SourceSessionID != "sess-private-9" || !owner.PromotedFromPrivate {
		t.Fatalf("owner view = %+v", owner)
	}
	team := p.ForTeammate()
	if team.SourceSessionID != "" {
		t.Fatalf("teammate view kept session id %q", team.SourceSessionID)
	}
	if team.Summary != PrivateSessionSummary {
		t.Fatalf("summary = %q", team.Summary)
	}
	raw, err := json.Marshal(team)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sess-private-9") || strings.Contains(string(raw), "title") {
		t.Fatalf("teammate JSON leaked session or title: %s", raw)
	}

	p.RemovedByOwner = true
	prefix := FormatProvenancePrefix(p, "kept snippet")
	if strings.Contains(prefix, "title") || strings.Contains(strings.ToLower(prefix), "session_title") {
		t.Fatalf("prefix contains a session title: %s", prefix)
	}
	parsed, ok := ParseProvenancePrefix(prefix)
	if !ok {
		t.Fatalf("parse failed: %s", prefix)
	}
	if parsed.SourceSessionID != "sess-private-9" || parsed.SourceOwnerID != "user-ada" || !parsed.PromotedFromPrivate || !parsed.RemovedByOwner {
		t.Fatalf("parsed = %+v", parsed)
	}
	if !strings.Contains(prefix, "kept snippet") {
		t.Fatalf("prefix dropped existing snippet: %s", prefix)
	}
}
