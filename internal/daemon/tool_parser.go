package daemon

import (
	"encoding/json"
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

// ParseInterceptorEvents converts Layer-1 ToolEvents (FILE_READ /
// FILE_MODIFIED / COMMAND_EXECUTED) into the same ParsedToolCall shape
// ParseToolCalls produces so both feeds share one provenance pipeline (F3).
// defaultTurn is used when an event payload omits turn_index. workspaceRoot
// makes absolute paths repo-relative when present.
func ParseInterceptorEvents(evs []ToolEvent, defaultTurn int, workspaceRoot string) []ParsedToolCall {
	if len(evs) == 0 {
		return nil
	}
	var out []ParsedToolCall
	for _, ev := range evs {
		p := ev.Payload
		if p == nil {
			continue
		}
		turn := defaultTurn
		if n, ok := asInt(p["turn_index"]); ok {
			turn = n
		} else if n, ok := asInt(p["turnIndex"]); ok {
			turn = n
		}
		switch ev.Type {
		case ToolEventFileRead:
			path := firstString(p, "path", "file_path", "filePath", "AbsolutePath")
			if path == "" {
				continue
			}
			out = append(out, ParsedToolCall{
				ToolName:  "file_read",
				TurnIndex: turn,
				Type:      "file_op",
				FilePath:  repoRelative(workspaceRoot, path),
				OpType:    "read",
			})
		case ToolEventFileModified:
			path := firstString(p, "path", "file_path", "filePath", "AbsolutePath")
			if path == "" {
				continue
			}
			diff := firstString(p, "diff", "diff_hunk", "DiffHunk")
			if diff == "" {
				before := firstString(p, "before")
				after := firstString(p, "after")
				if before != "" || after != "" {
					diff = ComputeDiffHunk(before, after)
				}
			} else {
				diff = headCap(diff, maxDiffHunkBytes)
			}
			out = append(out, ParsedToolCall{
				ToolName:  "file_modified",
				TurnIndex: turn,
				Type:      "file_op",
				FilePath:  repoRelative(workspaceRoot, path),
				OpType:    "modify",
				DiffHunk:  diff,
			})
		case ToolEventCommandExecuted:
			cmd := interceptorCommandLine(p)
			if cmd == "" {
				continue
			}
			outSnippet, trunc := tailCap(firstString(p, "output", "stdout", "stderr"), maxOutputSnippetKB)
			if firstBool(p, "output_truncated", "stdout_truncated", "stderr_truncated") {
				trunc = true
			}
			var exit *int
			if n, ok := asInt(p["exit_code"]); ok {
				exit = &n
			} else if n, ok := asInt(p["ExitCode"]); ok {
				exit = &n
			}
			cwd := firstString(p, "cwd", "working_directory", "WorkingDirectory", "Cwd")
			out = append(out, ParsedToolCall{
				ToolName:         "command_run",
				TurnIndex:        turn,
				Type:             "tool_exec",
				CommandLine:      cmd,
				WorkingDirectory: cwd,
				ExitCode:         exit,
				OutputSnippet:    outSnippet,
				Truncated:        trunc,
			})
		}
	}
	return out
}

// interceptorCommandLine joins command + args the same way episode detection
// keys commands (processor.toolCommandKey), kept local to avoid coupling.
func interceptorCommandLine(p map[string]any) string {
	cmd := firstString(p, "command", "CommandLine", "command_line", "cmd")
	var args []string
	switch a := p["args"].(type) {
	case []string:
		args = a
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok {
				args = append(args, s)
			}
		}
	}
	if len(args) == 0 {
		return strings.TrimSpace(cmd)
	}
	return strings.TrimSpace(cmd + " " + strings.Join(args, " "))
}

// DedupeParsedToolCalls collapses overlapping ParseToolCalls + interceptor
// rows by (turn_index, path, op). For tool_exec, path is the command_line and
// op is "exec". First occurrence wins (prefer harvested tool_calls when they
// are appended before interceptor events).
func DedupeParsedToolCalls(ops []ParsedToolCall) []ParsedToolCall {
	if len(ops) <= 1 {
		return ops
	}
	seen := make(map[string]struct{}, len(ops))
	out := make([]ParsedToolCall, 0, len(ops))
	for _, p := range ops {
		key := provenanceDedupeKey(p)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	return out
}

func provenanceDedupeKey(p ParsedToolCall) string {
	path := p.FilePath
	op := p.OpType
	if p.Type == "tool_exec" {
		path = p.CommandLine
		op = "exec"
	}
	return fmt.Sprintf("%d\x00%s\x00%s", p.TurnIndex, path, op)
}

// MergeProvenanceOps combines harvested tool_calls and interceptor FILE_*/
// COMMAND_* events into one deduped list for PushOperations (F3).
func MergeProvenanceOps(turnPayloads []map[string]any, toolEvs []ToolEvent, workspaceRoot string) []ParsedToolCall {
	var out []ParsedToolCall
	for i, payload := range turnPayloads {
		if payload == nil {
			continue
		}
		if workspaceRoot != "" && payload["workspace_root"] == nil {
			payload = copyMap(payload)
			payload["workspace_root"] = workspaceRoot
		}
		out = append(out, ParseToolCalls(payload, i)...)
	}
	defaultTurn := 0
	if n := len(turnPayloads); n > 0 {
		defaultTurn = n - 1
	}
	out = append(out, ParseInterceptorEvents(toolEvs, defaultTurn, workspaceRoot)...)
	return DedupeParsedToolCalls(out)
}

// ParseToolCalls extracts structured ops from a conversation turn payload.
func ParseToolCalls(turnPayload map[string]any, turnIndex int) []ParsedToolCall {
	if turnPayload == nil {
		return nil
	}
	raw := toolCallSlice(turnPayload["tool_calls"])
	if raw == nil {
		raw = toolCallSlice(turnPayload["toolCalls"])
	}
	root, _ := turnPayload["workspace_root"].(string)
	var out []ParsedToolCall
	for _, m := range raw {
		name := strings.ToLower(strings.TrimSpace(firstString(m, "name", "Name", "tool", "ToolName")))
		args := toolArgs(m)
		switch name {
		case "replace_file_content", "search_replace", "apply_patch",
			"edit", "strreplace", "str_replace", "multiedit", "multi_edit",
			"edit_file", "editnotebook", "edit_notebook":
			path := firstString(args, "TargetFile", "target_file", "path", "AbsolutePath", "absolute_path",
				"file_path", "filePath", "FilePath")
			start := firstInt(args, "StartLine", "start_line", "startLine")
			end := firstInt(args, "EndLine", "end_line", "endLine")
			target := firstString(args, "TargetContent", "target_content", "old_string", "OldString", "oldString")
			repl := firstString(args, "ReplacementContent", "replacement_content", "new_string", "NewString", "newString")
			if target == "" && repl == "" {
				if patch := firstString(args, "patch", "Patch", "diff", "Diff"); patch != "" {
					hunk := headCap(patch, maxDiffHunkBytes)
					out = append(out, ParsedToolCall{
						ToolName: name, TurnIndex: turnIndex, Type: "file_op",
						FilePath: repoRelative(root, path), OpType: "modify", DiffHunk: hunk,
					})
					continue
				}
			}
			hunk := ComputeDiffHunk(target, repl)
			out = append(out, ParsedToolCall{
				ToolName: name, TurnIndex: turnIndex, Type: "file_op",
				FilePath: repoRelative(root, path), OpType: "modify",
				LineStart: start, LineEnd: end, DiffHunk: hunk,
			})
		case "write_to_file", "write_file", "create_file", "write", "writefile":
			path := firstString(args, "TargetFile", "target_file", "path", "AbsolutePath",
				"file_path", "filePath", "FilePath")
			content := firstString(args, "Contents", "contents", "content", "TargetContent", "new_string", "NewString")
			overwrite := firstBool(args, "Overwrite", "overwrite")
			op := "create"
			if overwrite {
				op = "modify"
			}
			hunk := ComputeDiffHunk("", content)
			out = append(out, ParsedToolCall{
				ToolName: name, TurnIndex: turnIndex, Type: "file_op",
				FilePath: repoRelative(root, path), OpType: op, DiffHunk: hunk,
			})
		case "view_file", "read_file", "grep", "search", "codebase_search", "read", "readfile":
			path := firstString(args, "AbsolutePath", "absolute_path", "TargetFile", "target_file", "path", "query",
				"file_path", "filePath", "FilePath")
			out = append(out, ParsedToolCall{
				ToolName: name, TurnIndex: turnIndex, Type: "file_op",
				FilePath: repoRelative(root, path), OpType: "read",
			})
		case "run_command", "bash", "terminal", "shell", "run_terminal_cmd", "runterminalcmd":
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

// toolCallSlice normalizes tool_calls from JSON ([]any) or in-process
// harvest ([]map[string]any) into a homogeneous list.
func toolCallSlice(v any) []map[string]any {
	switch t := v.(type) {
	case []map[string]any:
		return t
	case []any:
		var out []map[string]any
		for _, item := range t {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
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
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case map[string]any:
				return t
			case string:
				s := strings.TrimSpace(t)
				if s == "" {
					continue
				}
				if s[0] == '{' {
					var parsed map[string]any
					if err := json.Unmarshal([]byte(s), &parsed); err == nil {
						return parsed
					}
				}
				if strings.Contains(s, "*** Begin Patch") || strings.Contains(s, "*** Update File:") {
					out := map[string]any{"patch": s}
					for _, line := range strings.Split(s, "\n") {
						line = strings.TrimSpace(line)
						for _, prefix := range []string{"*** Update File:", "*** Add File:", "*** Delete File:"} {
							if strings.HasPrefix(line, prefix) {
								path := strings.TrimSpace(strings.TrimPrefix(line, prefix))
								out["path"] = path
								out["TargetFile"] = path
								return out
							}
						}
					}
					return out
				}
				// Bare shell command string (Codex shell arguments).
				return map[string]any{"command": s, "CommandLine": s}
			}
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
