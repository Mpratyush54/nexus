package extract

import (
	"regexp"
	"strings"
	"testing"
)

func TestKeyFromContentAvoidsPrefixCollisions(t *testing.T) {
	a := KeyFromContent("We decided to use Postgres for the billing ledger.")
	b := KeyFromContent("We decided to use Redis for session caching.")
	if a == b {
		t.Fatalf("unrelated memories with the same first words share key %q", a)
	}
	// Readable slug plus a 6-hex content hash.
	if !regexp.MustCompile(`^decided/use-postgres-for-[0-9a-f]{6}$`).MatchString(a) {
		t.Fatalf("key %q: want readable slug + 6-hex hash", a)
	}
	// Same content (modulo case/punctuation/spacing) keeps the same key, so
	// re-extracting a sentence still dedupes against itself.
	if c := KeyFromContent("we decided to use postgres, for the billing   ledger"); c != a {
		t.Fatalf("normalised content changed key: %q vs %q", c, a)
	}
	if k := KeyFromContent("!!"); !strings.HasPrefix(k, "misc/note-") {
		t.Fatalf("empty slug key = %q", k)
	}
}

func TestPromptShowsExistingKeys(t *testing.T) {
	existing := []Existing{{Key: "auth/jwt-rotation", Level: "project", Scope: "decision", Content: "JWTs rotate every 24 hours."}}
	for name, p := range map[string]string{
		"extract":  BuildPrompt("p", existing, []Turn{{Speaker: "user", Content: "rotate weekly now"}}),
		"compress": BuildCompressPrompt("p", "s1", existing, []Turn{{Speaker: "user", Content: "rotate weekly now"}}),
	} {
		if !strings.Contains(p, "- [project/decision] key=auth/jwt-rotation: JWTs rotate every 24 hours.") {
			t.Fatalf("%s prompt does not show the existing key:\n%s", name, p)
		}
	}
}
