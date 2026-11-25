#nullable enable
namespace Nexus.Core;

/// Tags a person types on chats and memories.
internal static class TagText
{
    internal const int MaxLength = 32;

    /// "  #Bug Fix, " → "bug-fix"; "" when nothing usable is left.
    internal static string Normalize(string? raw)
    {
        var text = (raw ?? "").Trim().TrimStart('#').Trim().ToLowerInvariant();
        var chars = new System.Text.StringBuilder(text.Length);
        var dash = false;
        foreach (var c in text)
        {
            if (char.IsLetterOrDigit(c) || c is '_' or '.' or '/')
            {
                chars.Append(c);
                dash = false;
            }
            else if (!dash && chars.Length > 0)
            {
                chars.Append('-');
                dash = true;
            }
        }
        var tag = chars.ToString().Trim('-');
        return tag.Length > MaxLength ? tag[..MaxLength].Trim('-') : tag;
    }

    /// Splits "a, b c" style input into normalized, distinct tags.
    internal static List<string> Parse(string? raw) =>
        (raw ?? "").Split(new[] { ',', ';', '\n' }, StringSplitOptions.RemoveEmptyEntries)
            .Select(Normalize).Where(t => t.Length > 0).Distinct(StringComparer.Ordinal).ToList();

    /// Memory tags Nexus sets itself (provenance and the project brief). They
    /// are kept on save but not shown or offered as filters.
    internal static bool IsSystemMemoryTag(string tag) =>
        tag.StartsWith("session:", StringComparison.OrdinalIgnoreCase) ||
        tag.StartsWith("source:", StringComparison.OrdinalIgnoreCase) ||
        tag.StartsWith("brief-", StringComparison.OrdinalIgnoreCase) ||
        tag.Equals("project-brief", StringComparison.OrdinalIgnoreCase) ||
        tag.Equals("pinned", StringComparison.OrdinalIgnoreCase);

    /// The memory's full tag list after replacing its user tags: system tags
    /// are carried over unchanged, so provenance links never break.
    internal static List<string> WithUserTags(IEnumerable<string> current, IEnumerable<string> userTags)
    {
        var result = current.Where(IsSystemMemoryTag).ToList();
        foreach (var raw in userTags)
        {
            if (IsSystemMemoryTag(raw.Trim())) continue;
            var tag = Normalize(raw);
            if (tag.Length > 0 && !IsSystemMemoryTag(tag) && !result.Contains(tag, StringComparer.Ordinal)) result.Add(tag);
        }
        return result;
    }
}
