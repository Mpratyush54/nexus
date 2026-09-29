package daemon

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	maxDiffHunkBytes   = 8 * 1024
	maxOutputSnippetKB = 4 * 1024
)

// ParsedToolCall is one discrete file op or shell execution from a turn.
type ParsedToolCall struct {
	ToolName         string
	TurnIndex        int
	Type             string // "file_op" or "tool_exec"
	FilePath         string
	OpType           string // "read", "create", "modify", "delete"
	LineStart        int
	LineEnd          int
	DiffHunk         string
	CommandLine      string
	WorkingDirectory string
	ExitCode         *int
	OutputSnippet    string
	Truncated        bool
}

// ParseToolCalls extracts structured ops from a conversation turn payload.
func ParseToolCalls(turnPayload map[string]any, turnIndex int) []ParsedToolCall {
	if turnPayload == nil {
		return nil
	}
	raw, _ := turnPayload["tool_calls"].([]any)
	if raw == nil {
		// Some harnesses nest under args / function
		if nested, ok := turnPayload["toolCalls"].([]any); ok {
			raw = nested
		}
	}
	root, _ := turnPayload["workspace_root"].(string)
	var out []ParsedToolCall
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(firstString(m, "name", "Name", "tool", "ToolName")))
		args := toolArgs(m)
		switch name {
		case "replace_file_content", "search_replace", "apply_patch":
			path := firstString(args, "TargetFile", "target_file", "path", "AbsolutePath", "absolute_path")
			start := firstInt(args, "StartLine", "start_line", "startLine")
			end := firstInt(args, "EndLine", "end_line", "endLine")
			target := firstString(args, "TargetContent", "target_content", "old_string", "OldString")
			repl := firstString(args, "ReplacementContent", "replacement_content", "new_string", "NewString")
			hunk := ComputeDiffHunk(target, repl)
			out = append(out, ParsedToolCall{
				ToolName: name, TurnIndex: turnIndex, Type: "file_op",
				FilePath: repoRelative(root, path), OpType: "modify",
				LineStart: start, LineEnd: end, DiffHunk: hunk,
			})
		case "write_to_file", "write_file", "create_file":
			path := firstString(args, "TargetFile", "target_file", "path", "AbsolutePath")
			content := firstString(args, "Contents", "contents", "content", "TargetContent")
			overwrite := firstBool(args, "Overwrite", "overwrite")
			op := "modify"
			if !overwrite {
				op = "create"
			}
			hunk := ComputeDiffHunk("", content)
			out = append(out, ParsedToolCall{
				ToolName: name, TurnIndex: turnIndex, Type: "file_op",
				FilePath: repoRelative(root, path), OpType: op, DiffHunk: hunk,
			})
		case "view_file", "read_file", "grep", "search", "codebase_search":
			path := firstString(args, "AbsolutePath", "absolute_path", "TargetFile", "target_file", "path", "query")
			out = append(out, ParsedToolCall{
				ToolName: name, TurnIndex: turnIndex, Type: "file_op",
				FilePath: repoRelative(root, path), OpType: "read",
			})
		case "run_command", "bash", "terminal", "shell":
			cmd := firstString(args, "CommandLine", "command_line", "command", "cmd")
			cwd := firstString(args, "Cwd", "cwd", "working_directory", "WorkingDirectory")
			outSnippet, trunc := tailCap(firstString(args, "Output", "output", "stdout", "stderr"), maxOutputSnippetKB)
			var exit *int
			if v, ok := args["ExitCode"]; ok {
				if n, ok := asInt(v); ok {
					exit = &n
				}
			} else if v, ok := args["exit_code"]; ok {
				if n, ok := asInt(v); ok {
					exit = &n
				}
			}
			out = append(out, ParsedToolCall{
				ToolName: name, TurnIndex: turnIndex, Type: "tool_exec",
				CommandLine: cmd, WorkingDirectory: cwd, ExitCode: exit,
				OutputSnippet: outSnippet, Truncated: trunc,
			})
		}
	}
	return out
}

// ComputeDiffHunk produces a minimal unified-diff-ish hunk, capped at 8KB.
func ComputeDiffHunk(target, replacement string) string {
	if target == "" && replacement == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("--- a\n+++ b\n")
	for _, line := range strings.Split(target, "\n") {
		b.WriteString("-")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	for _, line := range strings.Split(replacement, "\n") {
		b.WriteString("+")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return headCap(b.String(), maxDiffHunkBytes)
}

func toolArgs(m map[string]any) map[string]any {
	for _, k := range []string{"args", "Args", "input", "arguments", "parameters"} {
		if v, ok := m[k].(map[string]any); ok {
			return v
		}
	}
	return m
}

func repoRelative(root, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if root == "" {
		return filepath.ToSlash(path)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				return t
			case fmt.Stringer:
				return t.String()
			}
		}
	}
	return ""
}

func firstInt(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if n, ok := asInt(v); ok {
				return n
			}
		}
	}
	return 0
}

func firstBool(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case bool:
				return t
			case string:
				return strings.EqualFold(t, "true") || t == "1"
			}
		}
	}
	return false
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int32:
		return int(t), true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	case float32:
		return int(t), true
	default:
		return 0, false
	}
}

func headCap(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func tailCap(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	return s[len(s)-n:], true
}
