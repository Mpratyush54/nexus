package checkpoint

import (
	"math/rand"
	"strings"
	"testing"
)

func TestHunksGroupsNearbyChanges(t *testing.T) {
	old := lines(1, 30)
	cur := strings.Replace(old, "line 3\n", "line three\n", 1)
	cur = strings.Replace(cur, "line 5\n", "", 1)
	cur = strings.Replace(cur, "line 25\n", "line 25\nadded\n", 1)
	hunks := Hunks(old, cur)
	if len(hunks) != 2 {
		t.Fatalf("hunks = %d, want 2: %+v", len(hunks), hunks)
	}
	h := hunks[0]
	if h.Added != 1 || h.Removed != 2 || h.Header != "@@ -1,8 +1,7 @@" {
		t.Fatalf("first hunk = %+v", h)
	}
	if hunks[1].Added != 1 || hunks[1].Removed != 0 {
		t.Fatalf("second hunk = %+v", hunks[1])
	}
	for _, l := range h.Lines {
		if strings.HasSuffix(l.Text, "\n") {
			t.Fatalf("line text keeps its newline: %q", l.Text)
		}
	}
}

func TestRevertAndApplySingleHunk(t *testing.T) {
	old := lines(1, 40)
	cur := strings.Replace(old, "line 2\n", "two\n", 1)
	cur = strings.Replace(cur, "line 30\n", "thirty\n", 1)
	got, ok := RevertHunk(old, cur, 1)
	if !ok {
		t.Fatal("no hunk 1")
	}
	want := strings.Replace(old, "line 2\n", "two\n", 1)
	if got != want {
		t.Fatalf("revert second hunk:\n%s", got)
	}
	base, ok := ApplyHunk(old, cur, 0)
	if !ok || base != want {
		t.Fatalf("apply first hunk:\n%s", base)
	}
	if _, ok := RevertHunk(old, cur, 2); ok {
		t.Fatal("hunk 2 should not exist")
	}
}

func TestHunksKeepLineEndingsExact(t *testing.T) {
	old := "a\r\nb\r\nc"
	cur := "a\r\nB\r\nc\n"
	hunks := Hunks(old, cur)
	if len(hunks) != 1 {
		t.Fatalf("hunks = %+v", hunks)
	}
	back, _ := RevertHunk(old, cur, 0)
	if back != old {
		t.Fatalf("revert = %q, want %q", back, old)
	}
	fwd, _ := ApplyHunk(old, cur, 0)
	if fwd != cur {
		t.Fatalf("apply = %q, want %q", fwd, cur)
	}
}

func TestHunksNewAndDeletedFile(t *testing.T) {
	h := Hunks("", "one\ntwo\n")
	if len(h) != 1 || h[0].Added != 2 || h[0].Header != "@@ -0,0 +1,2 @@" {
		t.Fatalf("new file = %+v", h)
	}
	h = Hunks("one\n", "")
	if len(h) != 1 || h[0].Removed != 1 || h[0].Header != "@@ -1 +0,0 @@" {
		t.Fatalf("deleted file = %+v", h)
	}
	if Hunks("same\n", "same\n") != nil {
		t.Fatal("no change should give no hunks")
	}
}

// Reverting every hunk (last first) gives the old text back, and applying
// every hunk (last first) gives the new text, for random edits.
func TestHunksRoundTripRandom(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	words := []string{"a\n", "b\n", "c\n", "d\n", "e\n", "f\r\n", "g"}
	gen := func(n int) string {
		var sb strings.Builder
		for i := 0; i < n; i++ {
			w := words[r.Intn(len(words))]
			if w == "g" && i != n-1 {
				w = "g\n"
			}
			sb.WriteString(w)
		}
		return sb.String()
	}
	for i := 0; i < 300; i++ {
		old, cur := gen(r.Intn(60)), gen(r.Intn(60))
		hunks := Hunks(old, cur)
		back := cur
		for k := len(hunks) - 1; k >= 0; k-- {
			var ok bool
			back, ok = RevertHunk(old, back, k)
			if !ok {
				t.Fatalf("case %d: revert %d failed", i, k)
			}
		}
		if back != old {
			t.Fatalf("case %d: revert all = %q, want %q (new %q)", i, back, old, cur)
		}
		fwd := old
		for k := len(hunks) - 1; k >= 0; k-- {
			fwd, _ = ApplyHunk(fwd, cur, k)
		}
		if fwd != cur {
			t.Fatalf("case %d: apply all = %q, want %q", i, fwd, cur)
		}
		a, d := LineStats(old, cur)
		var ha, hd int
		for _, h := range hunks {
			ha += h.Added
			hd += h.Removed
		}
		if a != ha || d != hd {
			t.Fatalf("case %d: stats %d/%d vs hunks %d/%d", i, a, d, ha, hd)
		}
	}
}

func TestHugeDiffFallsBackToOneBlock(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < 6000; i++ {
		a.WriteString("x" + itoa(i) + "\n")
		b.WriteString("y" + itoa(i) + "\n")
	}
	hunks := Hunks(a.String(), b.String())
	if len(hunks) != 1 || hunks[0].Added != 6000 || hunks[0].Removed != 6000 {
		t.Fatalf("got %d hunks", len(hunks))
	}
	back, _ := RevertHunk(a.String(), b.String(), 0)
	if back != a.String() {
		t.Fatal("revert of the fallback block is not exact")
	}
}

func lines(from, to int) string {
	var sb strings.Builder
	for i := from; i <= to; i++ {
		sb.WriteString("line " + itoa(i) + "\n")
	}
	return sb.String()
}
