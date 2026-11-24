#nullable enable
using System.Text.Json;
using System.Text.RegularExpressions;

namespace Nexus.Core;

internal sealed record DiffLine(string Text, string Kind, int? OldLine, int? NewLine);
internal sealed record FileDiffPreview(string Path, string Diff, string Notice = "", string Status = "modified")
{
    public IReadOnlyList<DiffLine> Lines => ParseLines(Diff);
    public int Added => Lines.Count(line => line.Kind == "added");
    public int Removed => Lines.Count(line => line.Kind == "removed");

    public static FileDiffPreview Captured(string path, string input, string output)
    {
        foreach (var json in new[] { input, output })
        {
            try
            {
                using var doc = JsonDocument.Parse(json);
                var root = doc.RootElement;
                var diff = Find(root, new[] { "diff", "diffString", "patch", "patch_text" });
                if (diff.Length > 0) return new(path, diff, Find(root, new[] { "notice" }));
                var notice = Find(root, new[] { "notice" });
                if (notice.Length > 0) return new(path, "", notice);
                foreach (var pair in new[] { ("old_string", "new_string"), ("oldText", "newText"), ("beforeFullFileContent", "afterFullFileContent") })
                    if (TryString(root, pair.Item1, out var before) && TryString(root, pair.Item2, out var after) && before != after)
                    {
                        var removed = string.Join("\n", before.Replace("\r\n", "\n").TrimEnd('\n').Split('\n').Select(line => "-" + line));
                        var added = string.Join("\n", after.Replace("\r\n", "\n").TrimEnd('\n').Split('\n').Select(line => "+" + line));
                        return new(path, $"--- a/{path}\n+++ b/{path}\n@@ saved replacement @@\n{(before.Length > 0 ? removed + "\n" : "")}{(after.Length > 0 ? added : "")}", "Saved replacement fragment; original line numbers were not recorded.");
                    }
            }
            catch (JsonException) { }
            if (json.StartsWith("diff --git ", StringComparison.Ordinal) || json.StartsWith("@@ ", StringComparison.Ordinal) || json.StartsWith("--- ", StringComparison.Ordinal))
                return new(path, json);
        }
        return new(path, "", "No patch was saved for this file. Its earlier changes cannot be reconstructed accurately from the filename alone.");
    }

    private static bool TryString(JsonElement root, string key, out string text)
    {
        text = "";
        if (root.ValueKind != JsonValueKind.Object || !root.TryGetProperty(key, out var value) || value.ValueKind != JsonValueKind.String) return false;
        text = value.GetString() ?? ""; return true;
    }

    private static string Find(JsonElement root, string[] keys, int depth = 0)
    {
        if (depth > 5 || root.ValueKind != JsonValueKind.Object) return "";
        foreach (var key in keys) if (TryString(root, key, out var text) && text.Length > 0) return text;
        foreach (var key in new[] { "result", "success", "state", "metadata" })
            if (root.TryGetProperty(key, out var child) && Find(child, keys, depth + 1) is { Length: > 0 } result) return result;
        return "";
    }

    internal static IReadOnlyList<DiffLine> ParseLines(string diff)
    {
        var lines = new List<DiffLine>();
        int? old = null, current = null;
        var inHunk = false;
        foreach (var line in diff.Replace("\r\n", "\n").Split('\n'))
        {
            if (line.StartsWith("@@"))
            {
                var match = Regex.Match(line, @"^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@");
                old = match.Success && int.TryParse(match.Groups[1].Value, out var oldNumber) ? oldNumber : null;
                current = match.Success && int.TryParse(match.Groups[2].Value, out var newNumber) ? newNumber : null;
                inHunk = true; lines.Add(new(line, "header", null, null)); continue;
            }
            if (line.StartsWith("diff --git") || !inHunk && (line.StartsWith("--- ") || line.StartsWith("+++ ")))
            { lines.Add(new(line, "header", null, null)); if (line.StartsWith("diff --git")) { inHunk = false; old = current = null; } continue; }
            if (line.StartsWith("+") && (inHunk || !line.StartsWith("+++ ")))
            { lines.Add(new(line, "added", null, current)); if (current.HasValue) current++; }
            else if (line.StartsWith("-") && (inHunk || !line.StartsWith("--- ")))
            { lines.Add(new(line, "removed", old, null)); if (old.HasValue) old++; }
            else if (inHunk && line.StartsWith(" "))
            { lines.Add(new(line, "context", old, current)); if (old.HasValue) old++; if (current.HasValue) current++; }
            else lines.Add(new(line, "header", null, null));
        }
        return lines;
    }
}
