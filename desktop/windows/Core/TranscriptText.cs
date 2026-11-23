using System.Text.Json;
using System.Text.RegularExpressions;

namespace Nexus.Core;

internal static class TranscriptText
{
    internal sealed record ToolField(string Label, string Text, bool Code);

    internal static IReadOnlyList<ToolField> ToolFields(string text)
    {
        var fields = new List<ToolField>();
        if (string.IsNullOrWhiteSpace(text)) return fields;
        try
        {
            using var doc = JsonDocument.Parse(text);
            AddFields(doc.RootElement, "Input", fields, 0);
        }
        catch (JsonException) { fields.Add(new ToolField("Input", text, true)); }
        return fields;
    }

    private static void AddFields(JsonElement value, string label, List<ToolField> fields, int depth)
    {
        if (value.ValueKind == JsonValueKind.String)
        {
            var text = value.GetString() ?? "";
            if (depth < 3 && text.TrimStart().StartsWith("{", StringComparison.Ordinal))
            {
                try
                {
                    using var nested = JsonDocument.Parse(text);
                    AddFields(nested.RootElement, label, fields, depth + 1);
                    return;
                }
                catch (JsonException) { }
            }
            if (text.Length > 0) fields.Add(new ToolField(label, text, text.Contains('\n') || label is "Command" or "Patch" or "Code" or "Script" or "Input"));
            return;
        }
        if (value.ValueKind == JsonValueKind.Object && depth < 3)
        {
            foreach (var property in value.EnumerateObject())
            {
                var title = property.Name switch
                {
                    "cmd" or "command_line" => "Command",
                    "file_path" or "target_file" or "path" => "File",
                    "workdir" or "cwd" or "working_directory" => "Working directory",
                    _ => string.Join(" ", property.Name.Split('_', StringSplitOptions.RemoveEmptyEntries))
                };
                if (title.Length > 0) title = char.ToUpperInvariant(title[0]) + title[1..];
                AddFields(property.Value, title, fields, depth + 1);
            }
            return;
        }
        fields.Add(new ToolField(label, JsonSerializer.Serialize(value, new JsonSerializerOptions { WriteIndented = true }), value.ValueKind is JsonValueKind.Array or JsonValueKind.Object));
    }
    // Some harnesses persist encrypted reasoning as a message. It is metadata,
    // not readable tool input, and must not be mistaken for chat prose.
    internal static bool IsOpaque(string text) => IsOpaque(text, 0);

    private static bool IsOpaque(string text, int depth)
    {
        if (depth > 4) return false;
        text = text.Trim();
        if (text.StartsWith("{", StringComparison.Ordinal))
        {
            try
            {
                using var doc = JsonDocument.Parse(text);
                var root = doc.RootElement;
                if (root.TryGetProperty("encrypted_content", out _)) return true;
                if (root.TryGetProperty("message", out var message) && message.ValueKind == JsonValueKind.String)
                    return IsOpaque(message.GetString() ?? "", depth + 1);
            }
            catch (JsonException) { }
        }
        return text.Length > 100 && text.StartsWith("gAAAA", StringComparison.Ordinal) &&
            Regex.IsMatch(text, @"^[A-Za-z0-9_+/=\-]+$");
    }

    internal static string FormatArguments(string text)
    {
        try
        {
            using var doc = JsonDocument.Parse(text);
            return JsonSerializer.Serialize(doc.RootElement, new JsonSerializerOptions { WriteIndented = true });
        }
        catch (JsonException) { return text; }
    }

    internal static string Argument(string text, params string[] keys)
    {
        try
        {
            using var doc = JsonDocument.Parse(text);
            if (doc.RootElement.ValueKind != JsonValueKind.Object) return "";
            foreach (var key in keys)
                if (doc.RootElement.TryGetProperty(key, out var value) && value.ValueKind == JsonValueKind.String)
                    return value.GetString() ?? "";
        }
        catch (JsonException) { }
        return "";
    }
}
