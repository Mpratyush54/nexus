package daemon

import (
	"path/filepath"
	"reflect"
	"testing"
)

// Same vectors as store.TestLineHashVector on the server: the desktop and
// the server must hash lines identically.
func TestLineHashVector(t *testing.T) {
	for in, want := range map[string]string{
		"\t\treturn   err  \r":    "d72fd683e43841b8",
		"return err":              "d72fd683e43841b8",
		"x :=  a +\tb // sum\r\n": "d395ffcf25d3b3f0",
	} {
		if got := lineHash(in); got != want {
			t.Errorf("lineHash(%q) = %s, want %s", in, got, want)
		}
	}
}

func hashAll(lines ...string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = lineHash(l)
	}
	return out
}

func TestEditHunksCarryContentAnchors(t *testing.T) {
	root := t.TempDir()
	const up = "internal/up/uploader.go"
	// CRLF on disk: anchors hash the normalised lines.
	crlf := ""
	for _, l := range []string{"package up", "", "import \"time\"", "", "func Upload(req Req) error {",
		"\tvar err error", "\tfor i := 0; i < 3; i++ {", "\t\tif err = put(req); err == nil {", "\t\t\tbreak",
		"\t\t}", "\t\ttime.Sleep(time.Second)", "\t}", "\treturn err", "}"} {
		crlf += l + "\r\n"
	}
	writeFixture(t, root, up, crlf)
	abs := filepath.Join(root, filepath.FromSlash(up))
	edits := EditsFromEvents([]Event{turnWithCalls(
		// Partial-line edit: the anchor is the whole line 7.
		map[string]any{"id": "partial", "name": "Edit", "input": map[string]any{"file_path": abs, "old_string": "i < 5", "new_string": "i < 3"}},
		// Two added lines in the middle.
		map[string]any{"id": "block", "name": "Edit", "input": map[string]any{"file_path": abs,
			"old_string": "\t\t}\n\t}", "new_string": "\t\t}\n\t\ttime.Sleep(time.Second)\n\t}"}},
		// First line of the file: no context above.
		map[string]any{"id": "top", "name": "Edit", "input": map[string]any{"file_path": abs, "old_string": "package upload", "new_string": "package up"}},
	)}, root)
	changes, _ := locateEdits(root, edits)
	byID := map[string]codeChange{}
	for _, c := range changes {
		byID[c.ToolCallID] = c
	}
	p := byID["partial"]
	if !reflect.DeepEqual(p.AddedHashes, hashAll("\tfor i := 0; i < 3; i++ {")) ||
		!reflect.DeepEqual(p.ContextBefore, hashAll("func Upload(req Req) error {", "\tvar err error")) ||
		!reflect.DeepEqual(p.ContextAfter, hashAll("\t\tif err = put(req); err == nil {", "\t\t\tbreak")) {
		t.Fatalf("partial anchors: %+v", p)
	}
	b := byID["block"]
	if b.StartLine != 11 || !reflect.DeepEqual(b.AddedHashes, hashAll("\t\ttime.Sleep(time.Second)")) ||
		!reflect.DeepEqual(b.ContextBefore, hashAll("\t\t\tbreak", "\t\t}")) ||
		!reflect.DeepEqual(b.ContextAfter, hashAll("\t}", "\treturn err")) {
		t.Fatalf("block anchors: %+v", b)
	}
	top := byID["top"]
	if len(top.ContextBefore) != 0 || !reflect.DeepEqual(top.AddedHashes, hashAll("package up")) || len(top.ContextAfter) != 2 {
		t.Fatalf("top anchors: %+v", top)
	}
}
