package checkpoint

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// A later write in the same clock tick must still look changed.
	future := time.Now().Add(time.Duration(len(content)+1) * time.Millisecond)
	_ = os.Chtimes(full, future, future)
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "main")
	gitCmd(t, dir, "config", "core.autocrlf", "false")
	write(t, dir, ".gitignore", "ignored/\n")
	write(t, dir, "src/app.go", lines(1, 20))
	write(t, dir, "README.md", "hello\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func newStore(t *testing.T) *Store {
	return Open(filepath.Join(t.TempDir(), "checkpoints"), Options{})
}

// A turn: snapshot, the "agent" edits, then Finish and Link.
func turn(t *testing.T, s *Store, session, ws string, idx int64, edit func()) *Checkpoint {
	t.Helper()
	cp, err := s.Take(ctx, session, ws, "prompt")
	if err != nil {
		t.Fatal(err)
	}
	edit()
	if _, err := s.Finish(ctx, cp.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Link(cp.ID, idx, "ex-"+itoa(int(idx))); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(cp.ID)
	return got
}

func TestSnapshotNeverTouchesGitIndexOrTree(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "src/app.go", lines(1, 21))     // modified, unstaged
	write(t, repo, "notes.txt", "untracked\n")     // untracked
	write(t, repo, "ignored/big.bin", "ignored\n") // ignored
	gitCmd(t, repo, "add", "notes.txt")            // staged
	// git status may refresh the index itself, so it runs first.
	statusBefore := gitCmd(t, repo, "status", "--porcelain")
	headBefore := gitCmd(t, repo, "rev-parse", "HEAD")
	refsBefore := gitCmd(t, repo, "for-each-ref")
	index := filepath.Join(repo, ".git", "index")
	indexBefore, _ := os.ReadFile(index)
	infoBefore, _ := os.Stat(index)

	s := newStore(t)
	cp, err := s.Take(ctx, "chat", repo, "p")
	if err != nil {
		t.Fatal(err)
	}
	indexAfter, _ := os.ReadFile(index)
	infoAfter, _ := os.Stat(index)
	if !bytes.Equal(indexBefore, indexAfter) || !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Fatal("the snapshot changed .git/index")
	}
	if gitCmd(t, repo, "status", "--porcelain") != statusBefore || gitCmd(t, repo, "rev-parse", "HEAD") != headBefore ||
		gitCmd(t, repo, "for-each-ref") != refsBefore {
		t.Fatal("the snapshot changed the repository")
	}
	m, _ := s.Before(cp)
	if !m.Git || !m.Complete {
		t.Fatalf("manifest git=%v complete=%v", m.Git, m.Complete)
	}
	if !strings.HasPrefix(m.Files["README.md"].ID, "g:") {
		t.Fatalf("a clean tracked file should reuse Git's copy: %+v", m.Files["README.md"])
	}
	if !strings.HasPrefix(m.Files["src/app.go"].ID, "s:") {
		t.Fatalf("a modified file should be copied: %+v", m.Files["src/app.go"])
	}
	// A staged file is in Git's object store already (git add wrote it).
	if !strings.HasPrefix(m.Files["notes.txt"].ID, "g:") {
		t.Fatalf("a staged file should reuse Git's copy: %+v", m.Files["notes.txt"])
	}
	if b, err := s.read(repo, m.Files["notes.txt"]); err != nil || string(b) != "untracked\n" {
		t.Fatalf("read back = %q %v", b, err)
	}
	if _, ok := m.Files["ignored/big.bin"]; ok {
		t.Fatal("ignored files must not be snapshotted")
	}
}

func TestRewindRestoresGitFolderAndUndo(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "mine.txt", "user file\n")
	s := newStore(t)
	turn(t, s, "chat", repo, 100, func() {
		write(t, repo, "src/app.go", strings.Replace(lines(1, 20), "line 4\n", "four\n", 1))
		write(t, repo, "src/new.go", "package src\n")
	})
	turn(t, s, "chat", repo, 200, func() {
		write(t, repo, "src/new.go", "package src\n\nfunc New() {}\n")
		os.Remove(filepath.Join(repo, "README.md"))
	})
	// The person edits a file no agent touched: it is left alone.
	write(t, repo, "mine.txt", "user edit\n")

	plan, err := s.PreviewRewind(ctx, "chat", "100")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range plan.Files {
		got[f.Path] = f.Action
	}
	want := map[string]string{"src/app.go": "restore", "src/new.go": "delete", "README.md": "recreate"}
	if len(got) != len(want) || plan.Later != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("plan[%s] = %q, want %q (%+v)", k, got[k], v, plan.Files)
		}
	}
	res, err := s.RewindTo(ctx, "chat", "100")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Restored) != 3 || len(res.Failed) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if read(t, repo, "src/app.go") != lines(1, 20) || read(t, repo, "src/new.go") != "<missing>" || read(t, repo, "README.md") != "hello\n" {
		t.Fatal("files were not restored")
	}
	if read(t, repo, "mine.txt") != "user edit\n" {
		t.Fatal("a file no agent changed was touched")
	}
	if res.Rewind.FromIdx != 100 || res.Rewind.CutoffIdx <= 200 || res.Rewind.UndoID == "" {
		t.Fatalf("rewind = %+v", res.Rewind)
	}
	if rw := s.Rewinds("chat"); len(rw) != 1 {
		t.Fatalf("rewinds = %+v", rw)
	}
	// Rewound messages can't be restored to again.
	if _, err := s.Find("chat", "200"); err != ErrNoCheckpoint {
		t.Fatalf("find rewound = %v", err)
	}

	put, err := s.UndoRewind("chat", res.Rewind.UndoID)
	if err != nil || len(put) != 3 {
		t.Fatalf("undo = %v %v", put, err)
	}
	if read(t, repo, "src/new.go") != "package src\n\nfunc New() {}\n" || read(t, repo, "README.md") != "<missing>" {
		t.Fatal("undo did not put the files back")
	}
	if len(s.Rewinds("chat")) != 0 {
		t.Fatal("undo should forget the rewind")
	}
	if _, err := s.Find("chat", "200"); err != nil {
		t.Fatalf("after undo the later message is restorable again: %v", err)
	}
}

func TestNonGitFolder(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "alpha\n")
	write(t, dir, "node_modules/x/index.js", "dep\n")
	s := newStore(t)
	cp := turn(t, s, "c", dir, 10, func() {
		write(t, dir, "a.txt", "ALPHA\n")
		write(t, dir, "b.txt", "beta\n")
		write(t, dir, "node_modules/x/index.js", "changed dep\n")
	})
	m, _ := s.Before(cp)
	if m.Git {
		t.Fatal("not a git folder")
	}
	if _, ok := m.Files["node_modules/x/index.js"]; ok {
		t.Fatal("dependency folders are skipped")
	}
	if strings.Join(cp.Changed, ",") != "a.txt,b.txt" {
		t.Fatalf("changed = %v", cp.Changed)
	}
	if _, err := s.RewindTo(ctx, "c", "10"); err != nil {
		t.Fatal(err)
	}
	if read(t, dir, "a.txt") != "alpha\n" || read(t, dir, "b.txt") != "<missing>" {
		t.Fatal("non-git restore failed")
	}
	if read(t, dir, "node_modules/x/index.js") != "changed dep\n" {
		t.Fatal("skipped folders must not be touched")
	}
}

func TestSecondSnapshotReusesUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "alpha\n")
	s := newStore(t)
	if _, err := s.Take(ctx, "c", dir, ""); err != nil {
		t.Fatal(err)
	}
	// Make the file unreadable-by-content: a reused entry must not read it.
	blobs := countFiles(t, s.blobDir())
	if _, err := s.Take(ctx, "c", dir, ""); err != nil {
		t.Fatal(err)
	}
	if countFiles(t, s.blobDir()) != blobs {
		t.Fatal("an unchanged file was copied again")
	}
}

func TestLargeFilesAreListedNotCopied(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "big.bin", strings.Repeat("x", 2048))
	s := Open(filepath.Join(t.TempDir(), "cp"), Options{Limits: Limits{MaxFiles: 100, MaxFileBytes: 1024, MaxNewBytes: 1 << 20}})
	cp := turn(t, s, "c", dir, 5, func() { write(t, dir, "big.bin", "small now") })
	plan, err := s.PreviewRewind(ctx, "c", "5")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Files[0].Problem == "" {
		t.Fatalf("plan = %+v", plan)
	}
	res, err := s.RewindTo(ctx, "c", cp.ID)
	if err != nil || len(res.Failed) != 1 || read(t, dir, "big.bin") != "small now" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

func TestReviewKeepAndUndoHunks(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "f.txt", lines(1, 40))
	write(t, dir, "gone.txt", "bye\n")
	s := newStore(t)
	edited := strings.Replace(strings.Replace(lines(1, 40), "line 2\n", "two\n", 1), "line 30\n", "thirty\n", 1)
	cp := turn(t, s, "c", dir, 1, func() {
		write(t, dir, "f.txt", edited)
		write(t, dir, "new.txt", "fresh\n")
		os.Remove(filepath.Join(dir, "gone.txt"))
		write(t, dir, "img.png", "\x89PNG\x00\x01binary")
	})
	r, err := s.Review(cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 4 || r.Pending != 4 || r.Added != 3 || r.Removed != 3 {
		t.Fatalf("review = %+v", r)
	}
	files := map[string]ReviewFile{}
	for _, f := range r.Files {
		files[f.Path] = f
	}
	if files["new.txt"].Status != "added" || files["gone.txt"].Status != "deleted" || !files["img.png"].Binary || len(files["f.txt"].Hunks) != 2 {
		t.Fatalf("files = %+v", files)
	}
	// A stale version is refused.
	if _, err := s.Undo(cp.ID, "f.txt", 1, "stale"); err != ErrStale {
		t.Fatalf("stale undo = %v", err)
	}
	// Undo the second hunk only.
	r, err = s.Undo(cp.ID, "f.txt", 1, files["f.txt"].Version)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, dir, "f.txt") != strings.Replace(lines(1, 40), "line 2\n", "two\n", 1) {
		t.Fatalf("after hunk undo:\n%s", read(t, dir, "f.txt"))
	}
	// Keep the remaining hunk: the file is settled and stays edited.
	f := find(r, "f.txt")
	if f == nil || len(f.Hunks) != 1 {
		t.Fatalf("f.txt after undo = %+v", f)
	}
	r, err = s.Keep(cp.ID, "f.txt", 0, f.Version)
	if err != nil {
		t.Fatal(err)
	}
	if find(r, "f.txt") != nil || !strings.Contains(read(t, dir, "f.txt"), "two\n") {
		t.Fatal("kept hunk should settle the file")
	}
	// Binary files: whole-file only.
	if _, err := s.Undo(cp.ID, "img.png", 0, ""); err == nil {
		t.Fatal("a binary hunk undo should be refused")
	}
	r, err = s.Undo(cp.ID, "img.png", -1, "")
	if err != nil || read(t, dir, "img.png") != "<missing>" {
		t.Fatalf("binary undo: %v", err)
	}
	// Undo all: the new file goes, the deleted one comes back.
	r, err = s.Undo(cp.ID, "", -1, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Pending != 0 || read(t, dir, "new.txt") != "<missing>" || read(t, dir, "gone.txt") != "bye\n" {
		t.Fatalf("undo all: %+v", r)
	}
	// What was kept is not undone by Undo all.
	if !strings.Contains(read(t, dir, "f.txt"), "two\n") {
		t.Fatal("undo all reverted a kept hunk")
	}
}

func TestReviewKeepAll(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "a\n")
	s := newStore(t)
	cp := turn(t, s, "c", dir, 1, func() {
		write(t, dir, "a.txt", "A\n")
		os.Remove(filepath.Join(dir, "a.txt"))
		write(t, dir, "b.txt", "b\n")
	})
	r, err := s.Keep(cp.ID, "", -1, "")
	if err != nil || r.Pending != 0 || r.Total != 2 {
		t.Fatalf("keep all = %+v %v", r, err)
	}
	if read(t, dir, "b.txt") != "b\n" || read(t, dir, "a.txt") != "<missing>" {
		t.Fatal("keep must not change files")
	}
}

func TestPrunesOldCheckpointsAndBlobs(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	s := Open(filepath.Join(t.TempDir(), "cp"), Options{MaxPerSession: 2, Now: func() time.Time { return now }})
	var first *Checkpoint
	for i := 1; i <= 4; i++ {
		cp := turn(t, s, "c", dir, int64(i), func() { write(t, dir, "f.txt", strings.Repeat("v", i*10)+itoa(i)) })
		if first == nil {
			first = cp
		}
		now = now.Add(time.Minute)
	}
	// Blobs written "moments ago" survive one GC; move the clock on.
	now = now.Add(time.Hour)
	if err := s.Prune(); err != nil {
		t.Fatal(err)
	}
	if n := len(s.List("c")); n != 2 {
		t.Fatalf("kept %d checkpoints, want 2", n)
	}
	if _, err := s.Get(first.ID); err == nil {
		t.Fatal("the oldest checkpoint should be pruned")
	}
	// Only blobs of the kept snapshots (and the last scan) remain.
	if n := countFiles(t, s.blobDir()); n > 3 {
		t.Fatalf("%d blobs left after GC", n)
	}

	// Age limit.
	s.opts.MaxAge = time.Minute
	now = now.Add(48 * time.Hour)
	_ = s.Prune()
	if n := len(s.List("c")); n != 0 {
		t.Fatalf("%d checkpoints older than the age limit kept", n)
	}
}

func TestPruneBySize(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	s := Open(filepath.Join(t.TempDir(), "cp"), Options{MaxBlobBytes: 3000, Now: func() time.Time { return now }})
	for i := 1; i <= 5; i++ {
		turn(t, s, "c", dir, int64(i), func() { write(t, dir, "f"+itoa(i)+".txt", strings.Repeat(itoa(i), 1000)) })
		now = now.Add(time.Minute)
	}
	now = now.Add(time.Hour)
	_ = s.Prune()
	if n := len(s.List("c")); n >= 5 || n == 0 {
		t.Fatalf("size pruning kept %d", n)
	}
}

func TestFindAndLatest(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a", "1")
	s := newStore(t)
	a := turn(t, s, "c", dir, 11, func() {})
	b := turn(t, s, "c", dir, 22, func() {})
	if cp, _ := s.Find("c", "11"); cp == nil || cp.ID != a.ID {
		t.Fatal("find by message index")
	}
	if cp, _ := s.Find("c", "ex-22"); cp == nil || cp.ID != b.ID {
		t.Fatal("find by exchange id")
	}
	if cp, _ := s.Latest("c"); cp == nil || cp.ID != b.ID {
		t.Fatal("latest")
	}
	if _, err := s.Find("other", "11"); err != ErrNoCheckpoint {
		t.Fatal("another chat's checkpoint must not be found")
	}
}

func TestSafePathRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"../x", "/etc/passwd", "a/../../b", `C:\x`, "a\\b", ""} {
		if _, err := safePath(root, p); err == nil {
			t.Fatalf("%q accepted", p)
		}
	}
}

func find(r *Review, path string) *ReviewFile {
	for i := range r.Files {
		if r.Files[i].Path == path {
			return &r.Files[i]
		}
	}
	return nil
}

func countFiles(t *testing.T, dir string) int {
	n := 0
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n++
		}
		return nil
	})
	return n
}
