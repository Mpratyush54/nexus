package store

import "testing"

// The desktop (internal/daemon) pins the same vectors: the two
// implementations of the line hash must agree.
func TestLineHashVector(t *testing.T) {
	for in, want := range map[string]string{
		"\t\treturn   err  \r":    "d72fd683e43841b8",
		"return err":              "d72fd683e43841b8",
		"x :=  a +\tb // sum\r\n": "d395ffcf25d3b3f0",
	} {
		if got := LineHash(in); got != want {
			t.Errorf("LineHash(%q) = %s, want %s", in, got, want)
		}
	}
	if NormalizeLine("  a \t\t b  ") != "a b" {
		t.Fatal("normalise")
	}
}

func TestLowInformationLine(t *testing.T) {
	for in, want := range map[string]bool{
		"}": true, "\t})": true, "": true, "  return nil": true, "} else {": true,
		"return fmt.Errorf(\"upload: %w\", err)": false, "for i := 0; i < 3; i++ {": false,
	} {
		if got := LowInformationLine(in); got != want {
			t.Errorf("LowInformationLine(%q) = %v", in, got)
		}
	}
}

func hashes(lines ...string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = LineHash(l)
	}
	return out
}

func strp(s string) *string { return &s }

func TestMatchProbes(t *testing.T) {
	// Change A added "retry()" and "}" between "for {" and "return err".
	a := &CodeChange{AddedHashes: hashes("\tretry()", "}"), ContextBefore: hashes("x := 1", "for {"), ContextAfter: hashes("return err", "")}
	// Change B added a different "}" elsewhere.
	b := &CodeChange{AddedHashes: hashes("}"), ContextBefore: hashes("func b() {", "\tb()"), ContextAfter: hashes("")}

	rank := func(c *CodeChange, text string, prev, next *string) (string, bool) {
		r, ok := c.MatchProbes(ProbesForLines([]string{text}, prev, next))
		return MatchName(r), ok
	}
	// A distinctive line matches alone, and exactly with its neighbours.
	if m, ok := rank(a, "retry()", nil, nil); !ok || m != "hash" {
		t.Fatalf("hash: %s %v", m, ok)
	}
	if m, ok := rank(a, "  retry()  ", strp("for {"), strp("}")); !ok || m != "exact" {
		t.Fatalf("exact: %s %v", m, ok)
	}
	// Neighbour moved (a line inserted between) but still near: context.
	if m, ok := rank(a, "retry()", strp("log()"), strp("return err")); !ok || m != "context" {
		t.Fatalf("context: %s %v", m, ok)
	}
	// "}" alone is low-information: no match without context, and the
	// context picks the right change.
	if _, ok := rank(a, "}", nil, nil); ok {
		t.Fatal("low-information line matched without context")
	}
	if m, ok := rank(a, "}", strp("retry()"), strp("return err")); !ok || m != "exact" {
		t.Fatalf("} in A: %s %v", m, ok)
	}
	if _, ok := rank(b, "}", strp("retry()"), strp("return err")); ok {
		t.Fatal("} matched the wrong change")
	}
	if m, ok := rank(b, "}", strp("b()"), strp("")); !ok || m != "exact" {
		t.Fatalf("} in B: %s %v", m, ok)
	}
	// Modified text: no content match.
	if _, ok := rank(a, "retry(3)", strp("for {"), nil); ok {
		t.Fatal("modified line matched")
	}
	// Bad stored hashes are blanked, not trusted.
	c := &CodeChange{ProjectID: "p", EntityID: "e", SessionID: "s", StartLine: 1,
		AddedHashes: []string{"NOT-A-HASH", LineHash("ok line here")}, ContextBefore: hashes("a", "b", "c")}
	if err := normalizeCodeChange(c); err != nil || c.AddedHashes[0] != "" || len(c.ContextBefore) != 2 || c.ContextBefore[1] != LineHash("c") {
		t.Fatalf("clean: %+v %v", c, err)
	}
}
