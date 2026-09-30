package daemon

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseReplaceFileContent(t *testing.T) {
	root := filepath.Join("D:", "central-memory")
	if runtime.GOOS != "windows" {
		root = "/tmp/central-memory"
	}
	target := filepath.Join(root, "internal", "store", "memory.go")
	payload := map[string]any{
		"workspace_root": root,
		"tool_calls": []any{
			map[string]any{
				"name": "replace_file_content",
				"args": map[string]any{
					"TargetFile":         target,
					"StartLine":          10,
					"EndLine":            12,
					"TargetContent":      "old line",
					"ReplacementContent": "new line",
				},
			},
		},
	}
	got := ParseToolCalls(payload, 3)
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	p := got[0]
	if p.Type != "file_op" || p.OpType != "modify" || p.TurnIndex != 3 {
		t.Fatalf("%+v", p)
	}
	if !strings.Contains(p.DiffHunk, "-old line") || !strings.Contains(p.DiffHunk, "+new line") {
		t.Fatalf("diff=%q", p.DiffHunk)
	}
	if p.FilePath != "internal/store/memory.go" && !strings.HasSuffix(p.FilePath, "internal/store/memory.go") {
		t.Fatalf("path=%q", p.FilePath)
	}
}

func TestParseRunCommandTailTruncate(t *testing.T) {
	long := strings.Repeat("x", maxOutputSnippetKB+100) + "TAIL"
	payload := map[string]any{
		"tool_calls": []any{
			map[string]any{
				"name": "run_command",
				"args": map[string]any{
					"CommandLine": "go test ./...",
					"Cwd":         "/tmp",
					"ExitCode":    1,
					"Output":      long,
				},
			},
		},
	}
	got := ParseToolCalls(payload, 1)
	if len(got) != 1 || got[0].Type != "tool_exec" {
		t.Fatalf("%+v", got)
	}
	if !got[0].Truncated || len(got[0].OutputSnippet) != maxOutputSnippetKB {
		t.Fatalf("trunc=%v len=%d", got[0].Truncated, len(got[0].OutputSnippet))
	}
	if !strings.HasSuffix(got[0].OutputSnippet, "TAIL") {
		t.Fatalf("want tail kept: %q", got[0].OutputSnippet[len(got[0].OutputSnippet)-10:])
	}
}

func TestParseViewFileRead(t *testing.T) {
	got := ParseToolCalls(map[string]any{
		"tool_calls": []any{map[string]any{
			"name": "view_file",
			"args": map[string]any{"AbsolutePath": "/repo/a.go"},
		}},
	}, 0)
	if len(got) != 1 || got[0].OpType != "read" {
		t.Fatalf("%+v", got)
	}
}

func TestComputeDiffHunkCap(t *testing.T) {
	big := strings.Repeat("line\n", 3000)
	h := ComputeDiffHunk(big, big)
	if len(h) > maxDiffHunkBytes {
		t.Fatalf("len=%d", len(h))
	}
}

func TestWriteToFileCreate(t *testing.T) {
	got := ParseToolCalls(map[string]any{
		"tool_calls": []any{map[string]any{
			"name": "write_to_file",
			"args": map[string]any{
				"TargetFile": "new.go",
				"Contents":   "package main",
				"Overwrite":  false,
			},
		}},
	}, 2)
	if len(got) != 1 || got[0].OpType != "create" {
		t.Fatalf("%+v", got)
	}
}

// F3 rest: interceptor FILE_*/COMMAND_* merge into the same provenance shape
// as ParseToolCalls, then dedupe by (turn, path, op) when they overlap.
func TestInterceptorEventsMergeAndDedupeWithToolCalls(t *testing.T) {
	root := "/workspace"
	turnPayload := map[string]any{
		"workspace_root": root,
		"tool_calls": []any{
			map[string]any{
				"name": "Edit",
				"input": map[string]any{
					"file_path":  "internal/daemon/runtime.go",
					"old_string": "r.pushParsedToolOps(ctx, sid, harness, batch)",
					"new_string": "r.pushParsedToolOps(ctx, sid, harness, batch, inter)",
				},
			},
			map[string]any{
				"name": "Bash",
				"args": map[string]any{
					"command": "go test ./internal/daemon -run Parse",
				},
			},
		},
	}
	fromTools := ParseToolCalls(turnPayload, 1)
	if len(fromTools) < 2 {
		t.Fatalf("tool_calls parse: %+v", fromTools)
	}

	// Synthetic interceptor events that overlap the Edit (same path/op) and
	// add a distinct FILE_READ plus a COMMAND_EXECUTED that overlaps Bash.
	inter := []ToolEvent{
		{
			Type: ToolEventFileModified,
			Payload: map[string]any{
				"path":       filepath.Join(root, "internal/daemon/runtime.go"),
				"diff":       "-old\n+new",
				"turn_index": 1,
			},
		},
		{
			Type: ToolEventFileRead,
			Payload: map[string]any{
				"path":       filepath.Join(root, "internal/daemon/tool_parser.go"),
				"preview":    "package daemon",
				"turn_index": 1,
			},
		},
		{
			Type: ToolEventCommandExecuted,
			Payload: map[string]any{
				"command":    "go",
				"args":       []string{"test", "./internal/daemon", "-run", "Parse"},
				"exit_code":  0,
				"output":     "ok",
				"turn_index": 1,
			},
		},
	}
	fromInter := ParseInterceptorEvents(inter, 1, root)
	if len(fromInter) != 3 {
		t.Fatalf("interceptor parse want 3, got %+v", fromInter)
	}

	merged := MergeProvenanceOps([]map[string]any{nil /* turn 0 */, turnPayload}, inter, root)
	if len(merged) == 0 {
		t.Fatal("merged provenance ops empty after dedupe")
	}

	// Overlapping modify on runtime.go and overlapping shell command collapse;
	// FILE_READ on tool_parser.go survives as unique.
	var reads, modifies, execs int
	paths := map[string]int{}
	for _, p := range merged {
		switch {
		case p.Type == "file_op" && p.OpType == "read":
			reads++
			paths[p.FilePath]++
		case p.Type == "file_op" && p.OpType == "modify":
			modifies++
			paths[p.FilePath]++
		case p.Type == "tool_exec":
			execs++
		}
	}
	if modifies != 1 {
		t.Fatalf("want 1 modify after dedupe, got %d in %+v", modifies, merged)
	}
	if reads != 1 {
		t.Fatalf("want 1 read (interceptor-only), got %d in %+v", reads, merged)
	}
	if execs != 1 {
		t.Fatalf("want 1 tool_exec after command dedupe, got %d in %+v", execs, merged)
	}
	if paths["internal/daemon/runtime.go"] != 1 {
		t.Fatalf("runtime.go path count=%v want 1: %+v", paths, merged)
	}
	if paths["internal/daemon/tool_parser.go"] != 1 {
		t.Fatalf("tool_parser.go missing: %+v", merged)
	}

	// Raw concat without dedupe would be longer than merged.
	raw := append(append([]ParsedToolCall{}, fromTools...), fromInter...)
	if len(merged) >= len(raw) {
		t.Fatalf("dedupe did not shrink: merged=%d raw=%d", len(merged), len(raw))
	}
}
