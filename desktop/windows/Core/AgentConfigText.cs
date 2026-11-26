namespace Nexus.Core;

/// <summary>
/// Text helpers for the Agents page editors (rules files, MCP servers).
/// No UI types, so they are unit tested.
/// </summary>
internal static class AgentConfigText
{
    /// <summary>The value the core shows in place of a hidden secret.</summary>
    public const string Mask = "••••••";

    /// <summary>
    /// WinUI's TextBox reports line breaks as "\r"; files and the core use "\n".
    /// </summary>
    public static string Newlines(string text) => (text ?? "").Replace("\r\n", "\n").Replace('\r', '\n');

    /// <summary>For a TextBox: "\n" shown as the platform's line break.</summary>
    public static string ForTextBox(string text) => Newlines(text);

    public static bool Differs(string saved, string edited) => Newlines(saved) != Newlines(edited);

    /// <summary>One argument per line; blank lines are dropped, nothing else is trimmed away inside.</summary>
    public static List<string> Lines(string text) =>
        Newlines(text).Split('\n').Select(l => l.Trim()).Where(l => l.Length > 0).ToList();

    /// <summary>
    /// "KEY=value" (sep '=') or "Name: value" (sep ':') per line. Returns the
    /// first bad line's number (1-based) in <paramref name="badLine"/>, or 0.
    /// </summary>
    public static Dictionary<string, string> Pairs(string text, char sep, out int badLine)
    {
        var map = new Dictionary<string, string>(StringComparer.Ordinal);
        badLine = 0;
        var lines = Newlines(text).Split('\n');
        for (var i = 0; i < lines.Length; i++)
        {
            var line = lines[i].Trim();
            if (line.Length == 0 || line.StartsWith('#')) continue;
            var at = line.IndexOf(sep);
            if (at <= 0)
            {
                if (badLine == 0) badLine = i + 1;
                continue;
            }
            map[line[..at].Trim()] = line[(at + 1)..].Trim();
        }
        return map;
    }

    public static string FormatPairs(IEnumerable<KeyValuePair<string, string>> pairs, char sep) =>
        string.Join("\n", pairs.OrderBy(p => p.Key, StringComparer.OrdinalIgnoreCase).Select(p => p.Key + (sep == ':' ? ": " : "=") + p.Value));

    /// <summary>"Lines 3–5 are kept up to date by Nexus (Project memory…)."</summary>
    public static string ManagedNote(IReadOnlyList<(int Start, int End, string Label)> ranges)
    {
        if (ranges.Count == 0) return "";
        var parts = ranges.Select(r => (r.Start == r.End ? $"line {r.Start}" : $"lines {r.Start}–{r.End}") + " (" + r.Label + ")");
        var text = string.Join(", ", parts);
        return char.ToUpperInvariant(text[0]) + text[1..] + ". Nexus updates " + (ranges.Count == 1 ? "this section" : "these sections") +
               " by itself, so edit outside " + (ranges.Count == 1 ? "it" : "them") + ".";
    }

    /// <summary>A plain one-line summary for the add/edit review step.</summary>
    public static string AgentList(IReadOnlyList<string> names) => names.Count switch
    {
        0 => "",
        1 => names[0],
        2 => names[0] + " and " + names[1],
        _ => string.Join(", ", names.Take(names.Count - 1)) + " and " + names[^1]
    };
}
