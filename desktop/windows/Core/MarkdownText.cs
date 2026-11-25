#nullable enable
using System.Text.RegularExpressions;

namespace Nexus.Core;

/// <summary>
/// Markdown → one line of plain text for card previews and titles, where a
/// rendered document would not fit: "# Project brief\n## Key decisions\n- Use
/// Postgres" becomes "Project brief · Key decisions · Use Postgres".
/// </summary>
internal static partial class MarkdownText
{
    public static string Plain(string? markdown)
    {
        if (string.IsNullOrWhiteSpace(markdown)) return "";
        var parts = new List<string>();
        var inFence = false;
        foreach (var raw in markdown.Replace("\r", "").Split('\n'))
        {
            var line = raw.Trim();
            if (line.StartsWith("```", StringComparison.Ordinal) || line.StartsWith("~~~", StringComparison.Ordinal))
            {
                inFence = !inFence;
                continue;
            }
            if (line.Length == 0 || HorizontalRule().IsMatch(line) || TableDivider().IsMatch(line)) continue;
            if (!inFence)
            {
                line = Heading().Replace(line, "");
                line = Quote().Replace(line, "");
                line = Bullet().Replace(line, "");
                line = Task().Replace(line, "");
                line = Image().Replace(line, "$1");
                line = Link().Replace(line, "$1");
                line = Emphasis().Replace(line, "$2");
                line = Underscore().Replace(line, "$2");
                line = Code().Replace(line, "$1");
                line = line.Replace("<br>", " ", StringComparison.OrdinalIgnoreCase);
                if (line.StartsWith('|') && line.EndsWith('|'))
                    line = string.Join(" ", line.Trim('|').Split('|').Select(c => c.Trim()).Where(c => c.Length > 0));
            }
            line = Spaces().Replace(line, " ").Trim();
            if (line.Length > 0) parts.Add(line);
        }
        return string.Join(" · ", parts);
    }

    /// True when the text has block markdown worth rendering (headings,
    /// lists, fences) rather than showing as a plain sentence.
    public static bool LooksLikeMarkdown(string? text) =>
        !string.IsNullOrEmpty(text) && BlockMarker().IsMatch(text);

    [GeneratedRegex(@"^#{1,6}\s+")] private static partial Regex Heading();
    [GeneratedRegex(@"^(>\s?)+")] private static partial Regex Quote();
    [GeneratedRegex(@"^([-*+]|\d+[.)])\s+")] private static partial Regex Bullet();
    [GeneratedRegex(@"^\[[ xX]\]\s+")] private static partial Regex Task();
    [GeneratedRegex(@"!\[([^\]]*)\]\([^)]*\)")] private static partial Regex Image();
    [GeneratedRegex(@"\[([^\]]+)\]\([^)]*\)")] private static partial Regex Link();
    [GeneratedRegex(@"(\*\*|\*|~~)(?=\S)(.+?)(?<=\S)\1")] private static partial Regex Emphasis();
    // _x_ and __x__ only at word edges, so snake_case names survive.
    [GeneratedRegex(@"(?<![A-Za-z0-9])(__|_)(?=\S)(.+?)(?<=\S)\1(?![A-Za-z0-9])")] private static partial Regex Underscore();
    [GeneratedRegex(@"`([^`]*)`")] private static partial Regex Code();
    [GeneratedRegex(@"^([-*_]\s*){3,}$")] private static partial Regex HorizontalRule();
    [GeneratedRegex(@"^\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?$")] private static partial Regex TableDivider();
    [GeneratedRegex(@"\s+")] private static partial Regex Spaces();
    [GeneratedRegex(@"(?m)^(#{1,6}\s|\s*[-*+]\s|\s*\d+[.)]\s|```|>\s)")] private static partial Regex BlockMarker();
}
