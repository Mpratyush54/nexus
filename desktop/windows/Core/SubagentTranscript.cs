#nullable enable
using System.Text.Json;

namespace Nexus.Core;

/// One message of a sub-agent run, reduced to what its card shows.
internal sealed record SubTurn(string Role, string Text, IReadOnlyList<string> Tools, DateTimeOffset? At);

/// Reads Claude Code sub-agent transcripts
/// (~/.claude/projects/&lt;folder&gt;/&lt;parent&gt;/subagents/agent-&lt;hex&gt;.jsonl).
/// Lines are user/assistant messages plus bookkeeping (api-request blobs,
/// attachments) that can be megabytes each; only messages are parsed.
internal static class SubagentTranscript
{
    // Claude Code writes compact JSON; other writers add spaces after colons.
    private static readonly System.Text.RegularExpressions.Regex MessageType = new(@"""type""\s*:\s*""(?:user|assistant)""", System.Text.RegularExpressions.RegexOptions.Compiled);
    private static readonly System.Text.RegularExpressions.Regex Timestamp = new(@"""timestamp""\s*:\s*""([^""]+)""", System.Text.RegularExpressions.RegexOptions.Compiled);

    /// The message on one transcript line, or null for bookkeeping lines,
    /// tool-result-only user lines and unreadable lines.
    internal static SubTurn? ParseLine(string line)
    {
        if (line.Length == 0) return null;
        // Cheap reject before parsing: request blobs are the big lines.
        var head = line.Length > 600 ? line[..600] : line;
        if (!MessageType.IsMatch(head)) return null;
        try
        {
            using var doc = JsonDocument.Parse(line);
            var root = doc.RootElement;
            var type = NxJson.Text(root, "type");
            if (type is not ("user" or "assistant")) return null;
            if (!root.TryGetProperty("message", out var message) || message.ValueKind != JsonValueKind.Object) return null;
            var text = new System.Text.StringBuilder();
            var tools = new List<string>();
            if (message.TryGetProperty("content", out var content))
            {
                if (content.ValueKind == JsonValueKind.String) text.Append(content.GetString());
                else if (content.ValueKind == JsonValueKind.Array)
                    foreach (var block in content.EnumerateArray())
                    {
                        switch (NxJson.Text(block, "type"))
                        {
                            case "text":
                                if (text.Length > 0) text.Append("\n\n");
                                text.Append(NxJson.Text(block, "text"));
                                break;
                            case "tool_use":
                                tools.Add(NxJson.Text(block, "name") is { Length: > 0 } name ? name : "tool");
                                break;
                        }
                    }
            }
            var body = text.ToString().Trim();
            if (body.StartsWith("<system-reminder>", StringComparison.Ordinal) && type == "user") return null;
            if (body.Length == 0 && tools.Count == 0) return null;
            DateTimeOffset? at = DateTimeOffset.TryParse(NxJson.Text(root, "timestamp"), System.Globalization.CultureInfo.InvariantCulture,
                System.Globalization.DateTimeStyles.AssumeUniversal, out var when) ? when : null;
            return new SubTurn(type, body, tools, at);
        }
        catch (JsonException) { return null; }
    }

    /// Messages of a transcript, in order, at most `max` (the first ones; the
    /// last message is always kept so the outcome shows).
    internal static (List<SubTurn> Turns, int Total) Read(IEnumerable<string> lines, int max)
    {
        var turns = new List<SubTurn>();
        SubTurn? last = null;
        var total = 0;
        foreach (var line in lines)
        {
            if (ParseLine(line) is not { } turn) continue;
            total++;
            last = turn;
            if (turns.Count < max) turns.Add(turn);
        }
        if (last is not null && total > max) turns.Add(last);
        return (turns, total);
    }

    /// The first timestamp in a transcript (the run's start).
    internal static DateTimeOffset? FirstTimestamp(IEnumerable<string> lines)
    {
        foreach (var line in lines.Take(20))
        {
            var match = Timestamp.Match(line);
            if (match.Success && DateTimeOffset.TryParse(match.Groups[1].Value, System.Globalization.CultureInfo.InvariantCulture,
                    System.Globalization.DateTimeStyles.AssumeUniversal, out var when))
                return when;
        }
        return null;
    }

    /// The last timestamp in a piece of transcript text (its tail): when the run's last message was written.
    internal static DateTimeOffset? LastTimestamp(string tail)
    {
        DateTimeOffset? last = null;
        foreach (System.Text.RegularExpressions.Match match in Timestamp.Matches(tail))
            if (DateTimeOffset.TryParse(match.Groups[1].Value, System.Globalization.CultureInfo.InvariantCulture,
                    System.Globalization.DateTimeStyles.AssumeUniversal, out var when))
                last = when;
        return last;
    }

    /// "42s", "3m 05s", "1h 12m".
    internal static string Duration(TimeSpan span)
    {
        if (span < TimeSpan.Zero) span = TimeSpan.Zero;
        if (span.TotalSeconds < 60) return $"{(int)span.TotalSeconds}s";
        if (span.TotalMinutes < 60) return $"{(int)span.TotalMinutes}m {span.Seconds:00}s";
        return $"{(int)span.TotalHours}h {span.Minutes:00}m";
    }

    /// "Bash ×3, Read" — the tools a message used, most used first.
    internal static string ToolSummary(IEnumerable<string> tools) =>
        string.Join(", ", tools.GroupBy(t => t).OrderByDescending(g => g.Count()).ThenBy(g => g.Key, StringComparer.Ordinal)
            .Select(g => g.Count() > 1 ? $"{g.Key} ×{g.Count()}" : g.Key));
}
