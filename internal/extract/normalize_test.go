package extract

import "testing"

func TestNormalizeLevelAliases(t *testing.T) {
	cases := []struct {
		in, content, want string
	}{
		{"org", "Company policy requires MFA.", "organization"},
		{"user", "I prefer dark mode in the editor always.", "personal"},
		{"team", "We decided to use Redis for pub/sub.", "project"},
		{"", "We decided to use Redis for pub/sub.", "project"},
		{"garbage", "For now only touch the harvest worker.", "session"},
		{"session", "Temporary note for this task only.", "session"},
	}
	for _, tc := range cases {
		if got := NormalizeLevel(tc.in, tc.content); got != tc.want {
			t.Fatalf("NormalizeLevel(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeScopeAliases(t *testing.T) {
	cases := []struct {
		in, content, want string
	}{
		{"decided", "We decided to use Postgres.", "decision"},
		{"rule", "Agents must never paste API tokens.", "constraint"},
		{"", "We decided to use Postgres for primary store.", "decision"},
		{"pref", "I prefer concise commit messages.", "preference"},
	}
	for _, tc := range cases {
		if got := NormalizeScope(tc.in, tc.content); got != tc.want {
			t.Fatalf("NormalizeScope(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}
