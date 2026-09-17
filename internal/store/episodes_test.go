package store

import (
	"context"
	"strings"
	"testing"
)

// Full arc: fail → FILE_READ cluster → FILE_MODIFIED → pass → commit.
func episodeFullArc() []EventView {
	return []EventView{
		{ID: 1, Type: EventCommandExecuted, Command: "go", Args: []string{"test", "./..."}, ExitCode: 1,
			Stderr: "FAIL: TestWSLoadConcurrent\npgx pool exhausted, all connections in use\n"},
		{ID: 2, Type: EventFileRead, Path: "internal/store/db.go"},
		{ID: 3, Type: EventFileRead, Path: "internal/server/ws.go"},
		{ID: 4, Type: EventFileRead, Path: "internal/store/db.go"}, // dup read
		{ID: 5, Type: EventFileModified, Path: "internal/store/db.go"},
		{ID: 6, Type: EventFileModified, Path: "internal/server/ws.go"},
		{ID: 7, Type: EventCommandExecuted, Command: "go", Args: []string{"test", "./..."}, ExitCode: 0},
		{ID: 8, Type: EventGitCommitted, CommitSHA: "abc123def456", Message: "Increase pool lifetime, per-message conn acquire"},
	}
}

func TestEpisodeFullArcDetection(t *testing.T) {
	d := DetectEpisode(episodeFullArc())
	if d == nil {
		t.Fatal("expected draft for full arc, got nil")
	}
	if d.EpisodeType != EpisodeBugFix {
		t.Errorf("type = %q, want bug_fix", d.EpisodeType)
	}
	if d.Title == "" || !strings.Contains(d.Title, "go") {
		t.Errorf("title should name the command, got %q", d.Title)
	}
	for _, field := range []string{d.Trigger, d.Investigation, d.RootCause, d.Resolution, d.Verification, d.Narrative} {
		if field == "" {
			t.Error("arc section left empty; want full trigger/investigation/root_cause/resolution/verification")
		}
	}
	// error_patterns from stderr.
	found := false
	for _, p := range d.ErrorPatterns {
		if strings.Contains(p, "pgx pool exhausted") {
			found = true
		}
	}
	if !found {
		t.Errorf("error_patterns missing stderr signal, got %v", d.ErrorPatterns)
	}
	// files_involved from mods only (deduped, order-preserved).
	if len(d.FilesInvolved) != 2 || d.FilesInvolved[0] != "internal/store/db.go" || d.FilesInvolved[1] != "internal/server/ws.go" {
		t.Errorf("files_involved = %v, want [db.go ws.go] from mods", d.FilesInvolved)
	}
	// Link roles across the arc.
	roles := map[int64]string{}
	for _, l := range d.Links {
		roles[l.EventID] = l.Role
	}
	want := map[int64]string{
		1: RoleTrigger, 2: RoleInvestigation, 3: RoleInvestigation,
		4: RoleInvestigation, 5: RoleFix, 6: RoleFix,
		7: RoleVerification, 8: RoleContext,
	}
	for id, role := range want {
		if roles[id] != role {
			t.Errorf("event %d role = %q, want %q (links=%v)", id, roles[id], role, d.Links)
		}
	}
	if !strings.Contains(d.Resolution, "abc123def456") {
		t.Errorf("resolution should cite the commit, got %q", d.Resolution)
	}
}

func TestEpisodeNoFalsePositiveCleanRun(t *testing.T) {
	clean := []EventView{
		{ID: 1, Type: EventCommandExecuted, Command: "go", Args: []string{"test", "./..."}, ExitCode: 0},
		{ID: 2, Type: EventFileRead, Path: "main.go"},
		{ID: 3, Type: EventFileModified, Path: "main.go"},
		{ID: 4, Type: EventCommandExecuted, Command: "go", Args: []string{"test", "./..."}, ExitCode: 0},
		{ID: 5, Type: EventGitCommitted, CommitSHA: "deadbee", Message: "routine change"},
	}
	if d := DetectEpisode(clean); d != nil {
		t.Errorf("clean run must not produce an episode, got %+v", d)
	}
	if d := DetectEpisode(nil); d != nil {
		t.Error("empty stream must not produce an episode")
	}
	// Failure with investigation but no fix: not an episode.
	unfixed := []EventView{
		{ID: 1, Type: EventCommandExecuted, Command: "go", Args: []string{"test"}, ExitCode: 1, Stderr: "boom"},
		{ID: 2, Type: EventFileRead, Path: "a.go"},
	}
	if d := DetectEpisode(unfixed); d != nil {
		t.Errorf("unfixed failure must not produce an episode, got %+v", d)
	}
	// Failure + fix but never verified: not an episode yet.
	unverified := []EventView{
		{ID: 1, Type: EventCommandExecuted, Command: "go", Args: []string{"test"}, ExitCode: 1, Stderr: "boom"},
		{ID: 2, Type: EventFileModified, Path: "a.go"},
		{ID: 3, Type: EventCommandExecuted, Command: "go", Args: []string{"vet"}, ExitCode: 1, Stderr: "still boom"},
	}
	if d := DetectEpisode(unverified); d != nil {
		t.Errorf("unverified fix must not produce an episode, got %+v", d)
	}
}

func TestEpisodeArcWithoutReadsOrCommit(t *testing.T) {
	// Fix from memory (no reads) and no commit: still a valid episode.
	d := DetectEpisode([]EventView{
		{ID: 1, Type: EventCommandExecuted, Command: "pytest", ExitCode: 2, Stderr: "AssertionError: timeout"},
		{ID: 2, Type: EventFileModified, Path: "tests/conftest.py"},
		{ID: 3, Type: EventCommandExecuted, Command: "pytest", ExitCode: 0},
	})
	if d == nil {
		t.Fatal("expected draft without reads/commit, got nil")
	}
	if !strings.Contains(d.Resolution, "no linked commit") {
		t.Errorf("resolution should note missing commit, got %q", d.Resolution)
	}
}

func TestEpisodeExtractErrorPatterns(t *testing.T) {
	got := ExtractErrorPatterns("line one\n\nline one\nline two\n", "")
	if len(got) != 2 || got[0] != "line one" || got[1] != "line two" {
		t.Errorf("dedup/blank handling wrong: %v", got)
	}
	// stdout fallback when stderr is blank.
	got = ExtractErrorPatterns("  \n", "from stdout\n")
	if len(got) != 1 || got[0] != "from stdout" {
		t.Errorf("stdout fallback wrong: %v", got)
	}
	// ANSI-colorized output still matches.
	got = ExtractErrorPatterns("\x1b[31mFAIL: thing\x1b[0m\n", "")
	if len(got) != 1 || got[0] != "FAIL: thing" {
		t.Errorf("ANSI stripping wrong: %q", got)
	}
	if got := ExtractErrorPatterns("", ""); len(got) != 0 {
		t.Errorf("empty output should yield no patterns, got %v", got)
	}
}

func episodeRow(id string) []any {
	return []any{id, "t-" + id, EpisodeBugFix, "trig", "inv", "root", "res", "ver",
		[]string{"bug"}, []string{"a.go"}, []string{"boom"}, EpisodeResolved}
}

func TestEpisodeErrorPatternRetrievalSQL(t *testing.T) {
	sql, args := BuildEpisodeErrorSearchSQL("p1", "boom")
	for _, want := range []string{"$2 = ANY(error_patterns)", "status = 'RESOLVED'", "ORDER BY resolved_at DESC", "project_id = $1"} {
		if !strings.Contains(sql, want) {
			t.Errorf("error SQL missing %q:\n%s", want, sql)
		}
	}
	if len(args) != 2 || args[0] != "p1" || args[1] != "boom" {
		t.Errorf("args = %v, want [p1 boom]", args)
	}
	fq := &fakeQuerier{rows: &fakeRows{cols: [][]any{episodeRow("e1")}}}
	eps, err := NewEpisodeStore(fq).ByErrorPattern(context.Background(), "p1", "boom")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fq.gotSQL, "ANY(error_patterns)") {
		t.Errorf("executed SQL missing ANY(error_patterns):\n%s", fq.gotSQL)
	}
	if len(eps) != 1 || eps[0].ID != "e1" || eps[0].ProjectID != "p1" {
		t.Errorf("unexpected result: %+v", eps)
	}
	if len(eps[0].ErrorPatterns) != 1 || eps[0].ErrorPatterns[0] != "boom" {
		t.Errorf("error_patterns not scanned: %+v", eps[0])
	}
}

func TestEpisodeFileRetrievalSQL(t *testing.T) {
	sql, args := BuildEpisodeFileSearchSQL("p1", "a.go")
	for _, want := range []string{"$2 = ANY(files_involved)", "ORDER BY resolved_at DESC"} {
		if !strings.Contains(sql, want) {
			t.Errorf("file SQL missing %q:\n%s", want, sql)
		}
	}
	if len(args) != 2 || args[1] != "a.go" {
		t.Errorf("args = %v", args)
	}
	fq := &fakeQuerier{rows: &fakeRows{cols: [][]any{episodeRow("e2")}}}
	eps, err := NewEpisodeStore(fq).ByFile(context.Background(), "p1", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || eps[0].FilesInvolved[0] != "a.go" {
		t.Errorf("unexpected result: %+v", eps)
	}
}

func TestEpisodeSimilaritySQL(t *testing.T) {
	sql, args := BuildEpisodeSimilaritySQL("p1", []float32{0.1, 0.2}, 0)
	for _, want := range []string{"embedding <=> $2", "status = 'RESOLVED'", "ORDER BY embedding <=> $2", "LIMIT $3"} {
		if !strings.Contains(sql, want) {
			t.Errorf("similarity SQL missing %q:\n%s", want, sql)
		}
	}
	if args[1] != "[0.1,0.2]" {
		t.Errorf("embedding arg = %v, want pgvector literal", args[1])
	}
	if args[2] != DefaultEpisodeLimit {
		t.Errorf("limit = %v, want default %d", args[2], DefaultEpisodeLimit)
	}
	if _, args := BuildEpisodeSimilaritySQL("p1", []float32{1}, 10000); args[2] != MaxEpisodeLimit {
		t.Errorf("limit not clamped: %v", args[2])
	}
	// Store path scans the similarity scalar.
	row := append(episodeRow("e3"), 0.92)
	fq := &fakeQuerier{rows: &fakeRows{cols: [][]any{row}}}
	ranked, err := NewEpisodeStore(fq).Similar(context.Background(), "p1", []float32{0.1}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 1 || ranked[0].Episode.ID != "e3" || abs(ranked[0].Similarity-0.92) > 1e-9 {
		t.Errorf("unexpected ranked result: %+v", ranked)
	}
}

func TestEpisodeOrderBySimilarity(t *testing.T) {
	eps := []Episode{{ID: "a", Title: "a"}, {ID: "b", Title: "b"}, {ID: "c", Title: "c"}}
	vecs := map[string][]float32{
		"a": {1, 0},
		"b": {0, 1},
		"c": {1, 1},
	}
	ranked := OrderBySimilarity(eps, vecs, []float32{1, 0})
	if ranked[0].Episode.ID != "a" || ranked[1].Episode.ID != "c" || ranked[2].Episode.ID != "b" {
		t.Errorf("wrong semantic order: %v", ranked)
	}
}

func TestEpisodeCreate(t *testing.T) {
	d := DetectEpisode(episodeFullArc())
	fq := &fakeQuerier{rows: &fakeRows{cols: [][]any{{"new-id"}}}}
	ep, err := NewEpisodeStore(fq).Create(context.Background(), "p1", d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fq.gotSQL, "INSERT INTO episodes") || !strings.Contains(fq.gotSQL, "RETURNING") {
		t.Errorf("create SQL wrong:\n%s", fq.gotSQL)
	}
	if ep.ID != "new-id" || ep.ProjectID != "p1" || ep.Status != EpisodeResolved {
		t.Errorf("unexpected created episode: %+v", ep)
	}
	if len(ep.FilesInvolved) != 2 || len(ep.ErrorPatterns) == 0 {
		t.Errorf("draft signals not carried: %+v", ep)
	}
	if _, err := NewEpisodeStore(fq).Create(context.Background(), "p1", nil); err == nil {
		t.Error("nil draft should error")
	}
}

func TestEpisodeCreateSQLEmbeddingLiteral(t *testing.T) {
	d := &EpisodeDraft{Title: "t", EpisodeType: EpisodeBugFix, NarrativeEmbedding: []float32{1, 0.5}}
	sql, args := BuildCreateEpisodeSQL("p1", d)
	if !strings.Contains(sql, "embedding") {
		t.Errorf("create SQL missing embedding:\n%s", sql)
	}
	if args[8] != "[1,0.5]" {
		t.Errorf("embedding arg = %v, want pgvector literal", args[8])
	}
	d2 := &EpisodeDraft{Title: "t", EpisodeType: EpisodeBugFix}
	_, args2 := BuildCreateEpisodeSQL("p1", d2)
	if args2[8] != nil {
		t.Errorf("nil embedding should store NULL, got %v", args2[8])
	}
}
