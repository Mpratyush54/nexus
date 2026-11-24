using System.Text.Json;
using System.Text.RegularExpressions;
using Nexus.Core;

namespace Nexus.Pages;

// Turns raw tool-call payloads into the one-line labels people expect
// ("Edit · added report.md, updated main.go", "Run · npm test") instead of
// JSON blobs such as {"patch":"*** Begin Patch\n*** Add File: ..."}.
public sealed partial class SessionPage
{
    private static readonly Regex PatchFile = new(@"\*\*\* (Add|Update|Delete) File: ([^\r\n""\\]+)", RegexOptions.Compiled);

    internal static (string Kind, string Label) DescribeCommand(string raw)
    {
        var text = (raw ?? "").Trim();
        if (text.Length == 0) return ("Run", "command");
        var unescaped = text.Replace("\\n", "\n").Replace("\\\"", "\"");
        var files = PatchFile.Matches(unescaped).Select(m => (Action: m.Groups[1].Value, Path: m.Groups[2].Value.Trim())).ToList();
        if (files.Count > 0) return ("Edit", DescribePatch(files));
        if (text.StartsWith('{') || text.StartsWith('['))
        {
            try
            {
                using var doc = JsonDocument.Parse(text);
                var command = CommandFromJson(doc.RootElement);
                if (command.Length > 0) return ("Run", Unwrap(command));
            }
            catch (JsonException) { }
        }
        return ("Run", Unwrap(text));
    }

    private static string DescribePatch(List<(string Action, string Path)> files)
    {
        static string Verb(string action) => action switch { "Add" => "added", "Delete" => "deleted", _ => "updated" };
        var parts = files.Take(3).Select(f => Verb(f.Action) + " " + System.IO.Path.GetFileName(f.Path.Replace('\\', '/').TrimEnd('/')));
        var label = string.Join(", ", parts);
        if (files.Count > 3) label += " +" + (files.Count - 3) + " more";
        return char.ToUpperInvariant(label[0]) + label[1..];
    }

    private static string CommandFromJson(JsonElement value)
    {
        switch (value.ValueKind)
        {
            case JsonValueKind.String:
                return value.GetString() ?? "";
            case JsonValueKind.Array:
                var parts = value.EnumerateArray().Where(v => v.ValueKind == JsonValueKind.String).Select(v => v.GetString() ?? "").ToList();
                return string.Join(" ", parts.Select(p => p.Contains(' ') ? "\"" + p + "\"" : p));
            case JsonValueKind.Object:
                foreach (var key in new[] { "command", "cmd", "command_line", "script", "code", "input" })
                    if (value.TryGetProperty(key, out var inner))
                    {
                        var found = CommandFromJson(inner);
                        if (found.Length > 0) return found;
                    }
                return "";
            default:
                return "";
        }
    }

    // powershell.exe -Command "x" / bash -lc 'x' / cmd /c x  →  x
    private static string Unwrap(string command)
    {
        var c = Regex.Replace(command.Trim(), @"\s+", " ");
        var m = Regex.Match(c, @"^(?:""?[^""\s]*(?:powershell|pwsh)(?:\.exe)?""?)\s+(?:-NoProfile\s+|-NoLogo\s+|-NonInteractive\s+|-ExecutionPolicy\s+\S+\s+)*-Command\s+(.+)$", RegexOptions.IgnoreCase);
        if (!m.Success) m = Regex.Match(c, @"^(?:\S*/)?(?:bash|sh|zsh)(?:\.exe)?\s+-l?c\s+(.+)$", RegexOptions.IgnoreCase);
        if (!m.Success) m = Regex.Match(c, @"^cmd(?:\.exe)?\s+/[cC]\s+(.+)$");
        if (m.Success) c = m.Groups[1].Value.Trim();
        if (c.Length >= 2 && (c[0] == '"' || c[0] == '\'') && c[^1] == c[0]) c = c[1..^1];
        return c.Length > 0 ? c : command;
    }

    // ── Work-log steps ───────────────────────────────────────────────────
    // One tool call → icon + verb + target ("Edited src/app.ts", "Ran npm
    // test", "Searched “auth middleware”"), plus what its detail shows.
    internal static WorkStep DescribeStep(string tool, string input, string output = "")
    {
        tool ??= "";
        input ??= "";
        var step = new WorkStep { Tool = tool, Output = output ?? "" };
        var args = ParseArgs(input);
        string Arg(params string[] keys) => args.ValueKind == JsonValueKind.Object ? NxJson.Text(args, keys) : "";
        var mcp = tool.StartsWith("mcp__", StringComparison.Ordinal) ? tool[5..].Split("__", 2) : null;
        var server = mcp is { Length: 2 } ? mcp[0] : "";
        var name = mcp is { Length: 2 } ? mcp[1] : tool;
        var lower = name.ToLowerInvariant();
        var path = Arg("file_path", "path", "target_file", "notebook_path", "filePath", "relative_workspace_path");
        if (path.Length > 0) step.Files.Add(path);
        void Set(StepKind kind, string verb, string target, bool code = false)
        {
            step.Kind = kind;
            step.Verb = verb;
            step.Target = target;
            step.TargetIsCode = code;
        }

        if (tool is "Task" or "Agent" || lower is "agent" or "spawn_agent" || lower.Contains("subagent") || lower.Contains("sub_agent"))
        {
            var type = Arg("subagent_type", "agent_type", "agent", "name");
            Set(StepKind.Agent, type.Length > 0 ? WorkLogText.Humanize(type) : "Sub-agent", WorkLogText.OneLine(Arg("description", "task", "prompt", "message"), 100));
            step.Prompt = Arg("prompt", "task", "message");
            step.Files.Clear();
            return step;
        }
        if (server == "nexus" || lower is "memory_search" or "memory_write" or "project_knowledge" or "session_fetch" or "session_summary_get")
        {
            var query = Arg("query", "key", "content");
            var verb = lower switch
            {
                "memory_search" => "Searched memory",
                "memory_write" => "Saved to memory",
                "project_knowledge" => "Read project knowledge",
                "session_fetch" or "session_summary_get" => "Read an earlier session",
                _ => "Memory · " + WorkLogText.Humanize(name)
            };
            Set(StepKind.Memory, verb, query.Length > 0 ? Quote(query) : "");
            step.Files.Clear();
            return step;
        }
        if (lower is "todowrite" or "todoread" or "todo_write" or "write_todos" or "update_plan")
        {
            var items = 0;
            foreach (var key in new[] { "todos", "plan", "items" })
                if (args.ValueKind == JsonValueKind.Object && args.TryGetProperty(key, out var list) && list.ValueKind == JsonValueKind.Array)
                    items = list.GetArrayLength();
            Set(StepKind.Todo, "Updated plan", items > 0 ? WorkLogText.Plural(items, "item") : "");
            return step;
        }
        if (lower is "websearch" or "web_search" or "search_web")
        {
            Set(StepKind.Web, "Searched the web", Quote(Arg("query", "q", "search_term")));
            return step;
        }
        if (lower is "webfetch" or "web_fetch" or "fetch" or "fetch_url" or "open_url" or "read_url" || lower.Contains("browser"))
        {
            var url = Regex.Replace(Arg("url", "uri", "href"), "^https?://", "");
            Set(StepKind.Web, "Fetched", WorkLogText.MiddleEllipsis(url, 72), code: true);
            return step;
        }
        var patch = (args.ValueKind == JsonValueKind.Object ? Arg("patch", "input", "patch_text") : input).Replace("\\n", "\n");
        if (IsCommandTool(name) || lower is "powershell" or "local_shell" or "run_terminal_cmd")
        {
            var command = args.ValueKind == JsonValueKind.Object ? CommandFromJson(args) : input.TrimStart().StartsWith('{') ? "" : input;
            var (kind, label) = DescribeCommand(command);
            if (kind == "Edit") return PatchStep(step, command.Replace("\\n", "\n").Replace("\\\"", "\""));
            Set(StepKind.Run, "Ran", WorkLogText.OneLine(label, 140), code: true);
            step.Command = label;
            step.Files.Clear();
            return step;
        }
        if (PatchFile.IsMatch(patch)) return PatchStep(step, patch);
        // MCP tools only count as file reads/edits when they name a file.
        var fileTool = server.Length == 0 || path.Length > 0;
        if (fileTool && (lower is "workspace_change" or "file_change" or "apply_patch" or "search_replace" or "multiedit" or "notebookedit" or "create_file" or "delete_file" ||
            lower.Contains("edit") || lower.Contains("write") || lower.Contains("patch") || lower.Contains("replace")))
        {
            var verb = lower.Contains("delete") ? "Deleted" : lower.Contains("write") || lower.Contains("create") ? "Wrote" : "Edited";
            Set(StepKind.Edit, verb, path.Length > 0 ? WorkLogText.MiddleEllipsis(path) : WorkLogText.Humanize(name), code: path.Length > 0);
            step.Diff = EditDiff(path, input, step.Output, args);
            return step;
        }
        if (fileTool && (lower.StartsWith("read") || lower.Contains("_read") || lower is "view" or "view_file" or "open_file" or "cat" or "notebookread"))
        {
            var target = path.Length > 0 ? path : Arg("uri", "url", "name");
            Set(StepKind.Read, "Read", WorkLogText.MiddleEllipsis(target), code: true);
            return step;
        }
        if (lower.Contains("glob"))
        {
            Set(StepKind.List, "Found files", WorkLogText.OneLine(Arg("pattern", "glob", "query"), 80), code: true);
            step.Files.Clear();
            return step;
        }
        if (lower is "ls" or "list" or "list_dir" or "list_directory" or "list_files" or "listdir")
        {
            Set(StepKind.List, "Listed", WorkLogText.MiddleEllipsis(path.Length > 0 ? path : Arg("directory", "dir") is { Length: > 0 } dir ? dir : "."), code: true);
            step.Files.Clear();
            return step;
        }
        if (lower.Contains("grep") || lower.Contains("search") || lower is "rg" or "ripgrep" or "find")
        {
            var query = Arg("pattern", "query", "regex", "search_term", "q");
            var where = Arg("path", "directory", "glob", "include");
            Set(StepKind.Search, "Searched", (query.Length > 0 ? Quote(query) : "") + (where.Length > 0 ? " in " + WorkLogText.MiddleEllipsis(where, 48) : ""));
            step.Files.Clear();
            return step;
        }
        // Anything else: name it plainly and list its arguments as fields.
        var hint = Arg("query", "path", "file_path", "url", "description", "prompt", "name", "title", "command");
        var label2 = WorkLogText.OneLine(hint, 80);
        Set(StepKind.Tool, server.Length > 0 ? WorkLogText.Humanize(name) : tool.Length > 0 ? tool : "Tool",
            server.Length > 0 ? server + (label2.Length > 0 ? " · " + label2 : "") : label2);
        step.Fields = ArgFields(args, input);
        return step;
    }

    private static WorkStep PatchStep(WorkStep step, string patch)
    {
        var files = PatchFile.Matches(patch).Select(m => (Action: m.Groups[1].Value, Path: m.Groups[2].Value.Trim())).ToList();
        step.Kind = StepKind.Edit;
        step.TargetIsCode = true;
        step.Files.Clear();
        step.Files.AddRange(files.Select(f => f.Path).Distinct());
        step.Verb = files.Count > 0 && files.All(f => f.Action == "Add") ? "Created" : files.Count > 0 && files.All(f => f.Action == "Delete") ? "Deleted" : "Edited";
        step.Target = step.Files.Count switch
        {
            0 => "files",
            1 => WorkLogText.MiddleEllipsis(step.Files[0]),
            _ => string.Join(", ", step.Files.Take(2).Select(f => System.IO.Path.GetFileName(f.Replace('\\', '/').TrimEnd('/')))) +
                 (step.Files.Count > 2 ? " +" + (step.Files.Count - 2) : "")
        };
        step.Diff = patch;
        return step;
    }

    private static string EditDiff(string path, string input, string output, JsonElement args)
    {
        var captured = FileDiffPreview.Captured(path, input, output).Diff;
        if (captured.Length > 0) return captured;
        static IEnumerable<string> Lines(string text) => text.Replace("\r\n", "\n").TrimEnd('\n').Split('\n');
        if (args.ValueKind == JsonValueKind.Object && args.TryGetProperty("edits", out var edits) && edits.ValueKind == JsonValueKind.Array)
        {
            var hunks = new List<string>();
            foreach (var edit in edits.EnumerateArray().Take(50))
            {
                var before = NxJson.Text(edit, "old_string", "oldText");
                var after = NxJson.Text(edit, "new_string", "newText");
                if (before.Length + after.Length == 0) continue;
                hunks.Add(string.Join("\n", (before.Length > 0 ? Lines(before).Select(l => "-" + l) : Enumerable.Empty<string>())
                    .Concat(after.Length > 0 ? Lines(after).Select(l => "+" + l) : Enumerable.Empty<string>())));
            }
            if (hunks.Count > 0) return string.Join("\n", hunks);
        }
        var content = args.ValueKind == JsonValueKind.Object ? NxJson.Text(args, "content", "contents", "file_text", "new_source") : "";
        return content.Length > 0 ? string.Join("\n", Lines(content).Select(l => "+" + l)) : "";
    }

    private static JsonElement ParseArgs(string input)
    {
        var text = input.Trim();
        if (!text.StartsWith('{')) return default;
        try
        {
            using var doc = JsonDocument.Parse(text);
            return doc.RootElement.Clone();
        }
        catch (JsonException) { return default; }
    }

    private static string Quote(string text) => text.Length > 0 ? "“" + WorkLogText.OneLine(text, 80) + "”" : "";

    private static IReadOnlyList<(string Label, string Text)> ArgFields(JsonElement args, string input)
    {
        var fields = new List<(string, string)>();
        if (args.ValueKind != JsonValueKind.Object)
        {
            var raw = input.Trim();
            if (raw.Length > 0 && !raw.StartsWith('{') && !raw.StartsWith('[')) fields.Add(("Input", raw.Length > 4000 ? raw[..4000] + "…" : raw));
            return fields;
        }
        foreach (var property in args.EnumerateObject().Take(10))
        {
            var value = property.Value;
            var text = value.ValueKind switch
            {
                JsonValueKind.String => value.GetString() ?? "",
                JsonValueKind.Number or JsonValueKind.True or JsonValueKind.False => value.GetRawText(),
                JsonValueKind.Array when value.EnumerateArray().All(v => v.ValueKind == JsonValueKind.String) =>
                    string.Join(", ", value.EnumerateArray().Take(20).Select(v => v.GetString())),
                JsonValueKind.Array => WorkLogText.Plural(value.GetArrayLength(), "item"),
                JsonValueKind.Object => WorkLogText.Readable(value.GetRawText()),
                _ => ""
            };
            if (text.Length == 0) continue;
            fields.Add((WorkLogText.Humanize(property.Name), text.Length > 4000 ? text[..4000] + "…" : text));
        }
        return fields;
    }

    // Live and saved Nexus activity → the same step a captured turn yields.
    internal static WorkStep StepFromActivity(LiveToolActivity activity)
    {
        var input = activity.Input;
        if (input.Length == 0 && (activity.Command.Length > 0 || activity.Files.Count > 0))
        {
            var synthetic = new Dictionary<string, string>();
            if (activity.Command.Length > 0) synthetic["command"] = activity.Command;
            if (activity.Files.Count > 0) synthetic["file_path"] = activity.Files[0];
            input = JsonSerializer.Serialize(synthetic);
        }
        var step = DescribeStep(activity.Tool, input, activity.Output);
        if (step.Kind == StepKind.Tool && activity.Command.Length > 0)
            step = DescribeStep("shell", JsonSerializer.Serialize(new { command = activity.Command }), activity.Output);
        else if (step.Kind == StepKind.Tool && activity.Diff.Length > 0)
        {
            step.Kind = StepKind.Edit;
            step.Verb = "Edited";
            step.Target = activity.Files.Count > 0 ? WorkLogText.MiddleEllipsis(activity.Files[0]) : "files";
            step.TargetIsCode = activity.Files.Count > 0;
            step.Fields = Array.Empty<(string, string)>();
        }
        else if (step.Kind == StepKind.Tool && activity.Title.Length > 0 && !activity.Tool.StartsWith("mcp__", StringComparison.Ordinal))
            step.Verb = activity.Title;
        foreach (var file in activity.Files)
            if (step.Kind is StepKind.Read or StepKind.Edit && !step.Files.Contains(file)) step.Files.Add(file);
        step.Id = activity.ID;
        step.Parent = activity.Parent;
        step.Status = activity.Status;
        step.ExitCode = activity.ExitCode;
        step.DurationMS = activity.DurationMS;
        if (activity.Diff.Length > 0) step.Diff = activity.Diff;
        step.Error = activity.Error == activity.Output ? "" : activity.Error;
        return step;
    }

    // Captured transcripts carry no status: read it off the output.
    internal static void MarkOutcome(WorkStep step)
    {
        var text = step.Output.TrimStart();
        if (text.StartsWith("<tool_use_error>", StringComparison.Ordinal) || text.StartsWith("Error:", StringComparison.OrdinalIgnoreCase))
            step.Status = "error";
        var exit = Regex.Match(text, @"^Exit code:? (-?\d+)");
        if (exit.Success && int.TryParse(exit.Groups[1].Value, out var code))
        {
            step.ExitCode = code;
            if (code != 0) step.Status = "error";
        }
    }
}
