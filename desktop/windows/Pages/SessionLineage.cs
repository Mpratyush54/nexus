using System.Text.Json;
using System.Text.RegularExpressions;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>Detect agent-spawned (sub-agent) sessions for timeline filtering.</summary>
public static partial class SessionLineage
{
    private static readonly Regex SubagentTitleSuffix = SubagentTitleRegex();
    private static readonly Regex SubagentNativeMarker = SubagentNativeRegex();

    /// <summary>
    /// True when a timeline/session row represents a sub-agent run, not a primary user chat.
    /// Uses explicit lineage fields when present, otherwise Cursor-style title suffixes such as "(@general subagent)".
    /// A parent session id alone is not enough: forks and other linked chats also carry one.
    /// </summary>
    public static bool IsSubagentSession(JsonElement item)
    {
        var kind = NxJson.Text(item, "lineage_kind", "lineageKind").Trim();
        if (kind.Equals("fork", StringComparison.OrdinalIgnoreCase))
        {
            return false;
        }
        if (kind.Length > 0)
        {
            var normalized = kind.Replace("-", "").Replace("_", "").Replace(" ", "");
            return normalized.Equals("agentspawn", StringComparison.OrdinalIgnoreCase) ||
                normalized.Contains("subagent", StringComparison.OrdinalIgnoreCase);
        }

        var title = NxJson.Text(item, "title");
        if (title.Length > 0 && SubagentTitleSuffix.IsMatch(title))
        {
            return true;
        }

        var native = NxJson.Text(item, "native_id", "nativeId");
        if (native.Length > 0 && !native.Contains('\\') && !native.Contains('/') &&
            SubagentNativeMarker.IsMatch(native))
        {
            return true;
        }

        return false;
    }

    /// <summary>
    /// A readable title for a sub-agent chat that has only a placeholder: the
    /// description its parent gave it (Claude Code's agent-*.meta.json), else
    /// the first request it was sent, else "Sub-agent: &lt;type&gt;".
    /// </summary>
    public static string SubagentTitle(string description, string firstRequest, string agentType)
    {
        static string OneLine(string text, int max)
        {
            var line = text.Replace("\r", "").Split('\n').Select(l => l.Trim()).FirstOrDefault(l => l.Length > 0) ?? "";
            line = Regex.Replace(line, @"^#{1,6}\s+", "");
            return line.Length <= max ? line : line[..(max - 1)].TrimEnd() + "…";
        }
        var title = OneLine(description, 90);
        if (title.Length > 0) return title;
        title = OneLine(firstRequest, 80);
        if (title.Length > 0) return title;
        return agentType.Trim().Length > 0 ? "Sub-agent: " + agentType.Trim() : "Sub-agent chat";
    }

    public static bool IsForkSession(JsonElement item) =>
        NxJson.Text(item, "lineage_kind", "lineageKind").Equals("fork", StringComparison.OrdinalIgnoreCase);

    [GeneratedRegex(@"\(\s*(?:@[\w.-]+\s+)?sub-?agent\s*\)\s*$", RegexOptions.IgnoreCase | RegexOptions.CultureInvariant)]
    private static partial Regex SubagentTitleRegex();

    // "sub-agent" markers, and Claude Code's subagent transcripts, which are
    // saved as <session>/subagents/agent-<hex id>.jsonl (native id "agent-<hex>").
    [GeneratedRegex(@"(?:^|[^a-z0-9])sub-?agent(?:[^a-z0-9]|$)|^agent-[0-9a-f]{8,}$", RegexOptions.IgnoreCase | RegexOptions.CultureInvariant)]
    private static partial Regex SubagentNativeRegex();
}
