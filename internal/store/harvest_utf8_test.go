package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateUTF8DoesNotSplitRune(t *testing.T) {
	// Em dash is 3 bytes (e2 80 94). Cutting at 8000 mid-content used to
	// leave a lone 0xe2 and Postgres rejected the insert (SQLSTATE 22021).
	prefix := strings.Repeat("a", 7999)
	s := prefix + "—" // 7999 + 3 = 8002 bytes
	got := TruncateUTF8(s, 8000)
	if !utf8.ValidString(got) {
		t.Fatalf("invalid utf8 after truncate: %q hex=%x", got, []byte(got))
	}
	if len(got) > 8000 {
		t.Fatalf("len=%d", len(got))
	}
	// Must drop the partial rune, not keep a leading 0xe2.
	if strings.HasSuffix(got, "\xe2") || strings.Contains(got, "\xe2\xe2") {
		t.Fatalf("truncated left partial UTF-8: %x", []byte(got[len(got)-3:]))
	}
}

func TestSanitizeUTF8DropsInvalid(t *testing.T) {
	raw := "ok\xe2\xe2\x80 more"
	got := SanitizeUTF8(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("still invalid: %x", []byte(got))
	}
	if strings.Contains(got, "\xe2\xe2") {
		t.Fatalf("invalid sequence survived: %q", got)
	}
}

func TestCleanHarvestTurnsAndPreviewValid(t *testing.T) {
	// Content that would break c[:8000] and preview line[:remain].
	long := strings.Repeat("x", 7998) + "—" + strings.Repeat("y", 100)
	turns := CleanHarvestTurns([]HarvestTurn{
		{Speaker: "user\xe2\xe2", Content: long, SessionID: "s1"},
		{Speaker: "assistant", Content: "short decision about UTF-8 sanitization for harvest."},
	})
	if len(turns) != 2 {
		t.Fatalf("turns=%d", len(turns))
	}
	for _, tr := range turns {
		if !utf8.ValidString(tr.Content) || !utf8.ValidString(tr.Speaker) {
			t.Fatalf("invalid turn: %+v", tr)
		}
		if len(tr.Content) > 8000 {
			t.Fatalf("content too long: %d", len(tr.Content))
		}
	}
	prev := HarvestRawPreview(turns, 100)
	if !utf8.ValidString(prev) {
		t.Fatalf("invalid preview: %x", []byte(prev))
	}
}

func TestHarvestRawPreviewMidEllipsis(t *testing.T) {
	// Force preview truncation on a line that ends with a multi-byte rune.
	turns := []HarvestTurn{{
		Speaker: "u",
		Content: strings.Repeat("字", 80), // 3 bytes each
	}}
	prev := HarvestRawPreview(turns, 50)
	if !utf8.ValidString(prev) {
		t.Fatalf("invalid preview: %x", []byte(prev))
	}
}
