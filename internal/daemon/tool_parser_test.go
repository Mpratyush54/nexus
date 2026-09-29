package daemon

import (
	"strings"
	"testing"
)

func TestParseReplaceFileContent(t *testing.T) {
	payload := map[string]any{
		"workspace_root": `D:\central-memory`,
		"tool_calls": []any{
			map[string]any{
				"name": "replace_file_content",
				"args": map[string]any{
					"TargetFile":         `D:\central-memory\internal\store\memory.go`,
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
