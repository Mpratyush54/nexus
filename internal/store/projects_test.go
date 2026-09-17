// Unit tests for the project resolver: URL normalization, priority
// matching, and deterministic dedup. All DB-free.
package store_test

import (
	"testing"

	"central-memory/internal/store"
)

func TestStore_NormalizeRemoteURL(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"blank", "  \t\n ", ""},
		{"scp with git suffix", "git@github.com:org/repo.git", "github.com/org/repo"},
		{"scp without suffix", "git@github.com:org/repo", "github.com/org/repo"},
		{"https with suffix", "https://github.com/org/repo.git", "github.com/org/repo"},
		{"https bare", "https://github.com/org/repo", "github.com/org/repo"},
		{"https trailing slash", "https://github.com/org/repo/", "github.com/org/repo"},
		{"https suffix plus slash", "https://github.com/org/repo.git/", "github.com/org/repo"},
		{"case folding", "HTTPS://GitHub.COM/Org/Repo.GIT", "github.com/org/repo"},
		{"http scheme", "http://github.com/org/repo.git", "github.com/org/repo"},
		{"ssh scheme", "ssh://git@github.com/org/repo.git", "github.com/org/repo"},
		{"ssh scheme with port", "ssh://git@github.com:22/org/repo.git", "github.com/org/repo"},
		{"git scheme", "git://github.com/org/repo.git", "github.com/org/repo"},
		{"nested groups", "git@gitlab.example.com:group/sub/repo.git", "gitlab.example.com/group/sub/repo"},
		{"userinfo stripped", "https://user:token123@github.com/org/repo.git", "github.com/org/repo"},
		{"host colon path without user", "github.com:org/repo.git", "github.com/org/repo"},
		{"trailing newline from git", "git@github.com:org/repo.git\n", "github.com/org/repo"},
		{"surrounding whitespace", "  https://github.com/org/repo.git  ", "github.com/org/repo"},
		{"local posix path", "/srv/git/foo.git", "srv/git/foo"},
		{"windows path", `C:\repos\foo.git`, "c:/repos/foo"},
		{"windows path forward slashes", "C:/repos/foo/", "c:/repos/foo"},
		{"query stripped", "https://github.com/org/repo.git?ref=main", "github.com/org/repo"},
		{"ssh user no port", "ssh://git@github.com/org/repo", "github.com/org/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := store.NormalizeRemoteURL(tc.input); got != tc.want {
				t.Errorf("NormalizeRemoteURL(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestStore_MatchProject(t *testing.T) {
	rows := []store.Project{
		{ID: "a", CanonicalURL: "github.com/o/r", RootCommit: "aaa", FolderName: "r"},
		{ID: "b", CanonicalURL: "github.com/o/other", RootCommit: "bbb", FolderName: "other"},
		{ID: "c", CanonicalURL: "", RootCommit: "", FolderName: "plain"},
	}
	cases := []struct {
		name      string
		canonical string
		root      string
		folder    string
		wantID    string
		wantMatch store.MatchStrategy
	}{
		{"canonical hit", "github.com/o/r", "", "", "a", store.MatchCanonicalURL},
		{"canonical wins over root of another row", "github.com/o/r", "bbb", "other", "a", store.MatchCanonicalURL},
		{"root hit", "", "bbb", "", "b", store.MatchRootCommit},
		{"root wins over folder of another row", "", "bbb", "plain", "b", store.MatchRootCommit},
		{"folder fallback", "", "", "plain", "c", store.MatchFolderName},
		{"folder matches row that also has url", "", "", "r", "a", store.MatchFolderName},
		{"no signals", "", "", "", "", store.MatchNone},
		{"unknown signals", "github.com/x/y", "ccc", "missing", "", store.MatchNone},
		{"empty signals never match populated rows", "", "", "", "", store.MatchNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, strategy := store.MatchProject(rows, tc.canonical, tc.root, tc.folder)
			if strategy != tc.wantMatch {
				t.Fatalf("strategy = %q, want %q", strategy, tc.wantMatch)
			}
			if tc.wantID == "" {
				if got != nil {
					t.Fatalf("expected nil match, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected match %q, got nil", tc.wantID)
			}
			if got.ID != tc.wantID {
				t.Errorf("matched ID = %q, want %q", got.ID, tc.wantID)
			}
		})
	}
}

// TestStore_MatchProjectDeterministicDedup pins the no-duplicates acceptance:
// identical signals always resolve to the same row, and ambiguous folder-only
// duplicates resolve to the first candidate (Resolve orders by created_at,
// id, so "first" is stable across calls).
func TestStore_MatchProjectDeterministicDedup(t *testing.T) {
	rows := []store.Project{
		{ID: "first", FolderName: "api"},
		{ID: "second", FolderName: "api"},
	}
	var prev string
	for i := 0; i < 5; i++ {
		got, strategy := store.MatchProject(rows, "", "", "api")
		if strategy != store.MatchFolderName {
			t.Fatalf("strategy = %q, want folder_name", strategy)
		}
		if got == nil || got.ID != "first" {
			t.Fatalf("call %d: got %+v, want stable match to 'first'", i, got)
		}
		prev = got.ID
	}
	if prev != "first" {
		t.Fatalf("resolution drifted: %q", prev)
	}
}
