using System.Text.Json;

namespace Nexus.Core;

public static class NxJson
{
    private static readonly string[] ListKeys =
    [
        "items", "events", "sessions", "memories", "teleports", "agents",
        "results", "projects", "turns", "files", "versions", "grants"
    ];

    public static IEnumerable<JsonElement> Items(JsonElement? result)
    {
        if (result is null)
        {
            yield break;
        }
        var el = result.Value;
        if (el.ValueKind == JsonValueKind.Array)
        {
            foreach (var item in el.EnumerateArray())
            {
                yield return item;
            }
            yield break;
        }
        if (el.ValueKind != JsonValueKind.Object)
        {
            yield break;
        }
        foreach (var name in ListKeys)
        {
            if (el.TryGetProperty(name, out var arr) && arr.ValueKind == JsonValueKind.Array)
            {
                foreach (var item in arr.EnumerateArray())
                {
                    yield return item;
                }
                yield break;
            }
        }
    }

    public static string Text(JsonElement el, params string[] names)
    {
        if (el.ValueKind != JsonValueKind.Object)
        {
            return el.ValueKind == JsonValueKind.String ? el.GetString() ?? "" : "";
        }
        foreach (var name in names)
        {
            if (!el.TryGetProperty(name, out var value))
            {
                continue;
            }
            var text = value.ValueKind switch
            {
                JsonValueKind.String => value.GetString() ?? "",
                JsonValueKind.Number => value.ToString(),
                JsonValueKind.True => "true",
                JsonValueKind.False => "false",
                _ => ""
            };
            if (text.Length > 0)
            {
                return text;
            }
        }
        return "";
    }

    public static string PublicStatus(string raw)
    {
        return raw.Trim().ToUpperInvariant() switch
        {
            "" or "PROPOSED" or "CONFIRMED" or "ACTIVE" => "active",
            "REJECTED" or "FORGOTTEN" => "forgotten",
            "SUPERSEDED" => "superseded",
            _ => raw.Trim().ToLowerInvariant()
        };
    }

    public static string Scrub(string text)
    {
        return text
            .Replace("PROPOSED", "active", StringComparison.Ordinal)
            .Replace("CONFIRMED", "active", StringComparison.Ordinal)
            .Replace("REJECTED", "forgotten", StringComparison.Ordinal)
            .Replace("SUPERSEDED", "superseded", StringComparison.Ordinal);
    }
}
