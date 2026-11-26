#nullable enable
namespace Nexus.Core;

/// <summary>
/// Fuzzy matching for the composer's / and @ pickers: the typed letters in
/// order, scored so a match at the start of a word, in a file's name, or in
/// one unbroken run wins. Returns where the letters matched so the picker can
/// highlight them. Pure and allocation-light: 20 000 paths in a few
/// milliseconds, so it runs on every keystroke (off the UI thread).
/// </summary>
internal static class FuzzyMatch
{
    /// <summary>A match: higher <see cref="Score"/> is better; <see cref="Positions"/> index into the text.</summary>
    internal readonly record struct Result(int Score, int[] Positions);

    /// <summary>
    /// Matches <paramref name="query"/> against <paramref name="text"/>, or
    /// returns null. An empty query matches everything with score 0.
    /// Case-insensitive; spaces in the query are ignored.
    /// </summary>
    public static Result? Match(string query, string text)
    {
        query = (query ?? "").Replace(" ", "");
        if (query.Length == 0) return new Result(0, Array.Empty<int>());
        if (string.IsNullOrEmpty(text) || query.Length > text.Length) return null;

        // A plain substring (best: at a word start) beats a scattered subsequence.
        var at = IndexOfWordStart(text, query);
        if (at < 0) at = text.IndexOf(query, StringComparison.OrdinalIgnoreCase);
        if (at >= 0)
        {
            var positions = new int[query.Length];
            for (var i = 0; i < query.Length; i++) positions[i] = at + i;
            var score = 1000 + query.Length * 20 - Math.Min(at, 200) + (IsWordStart(text, at) ? 300 : 0);
            if (at == 0) score += 200;
            if (query.Length == text.Length) score += 500;
            return new Result(score, positions);
        }

        // Letters in order, preferring word starts (greedy with a look for a word start first).
        var result = new int[query.Length];
        var ti = 0;
        for (var qi = 0; qi < query.Length; qi++)
        {
            var c = char.ToLowerInvariant(query[qi]);
            var found = -1;
            // A later word start that matches is worth skipping ahead for, but
            // only if the rest of the query still fits after it.
            for (var j = ti; j < text.Length; j++)
            {
                if (char.ToLowerInvariant(text[j]) != c) continue;
                if (found < 0) found = j;
                if (IsWordStart(text, j) && FitsAfter(query, qi + 1, text, j + 1)) { found = j; break; }
            }
            if (found < 0) return null;
            result[qi] = found;
            ti = found + 1;
        }
        var total = 0;
        for (var i = 0; i < result.Length; i++)
        {
            total += 10;
            if (IsWordStart(text, result[i])) total += 30;
            if (i > 0 && result[i] == result[i - 1] + 1) total += 25;
            else if (i > 0) total -= Math.Min(20, result[i] - result[i - 1]);
        }
        total -= Math.Min(100, result[0]);
        return new Result(Math.Max(1, total), result);
    }

    /// <summary>
    /// Matches a relative path: a match inside the file (or folder) name is
    /// worth much more than one spread over the directories. Positions index
    /// into the whole path.
    /// </summary>
    public static Result? MatchPath(string query, string path)
    {
        query = (query ?? "").Replace(" ", "");
        if (query.Length == 0) return new Result(0, Array.Empty<int>());
        var trimmed = path.TrimEnd('/', '\\');
        var nameStart = Math.Max(trimmed.LastIndexOf('/'), trimmed.LastIndexOf('\\')) + 1;
        var name = trimmed[nameStart..];
        // A query with a slash is about the directories too: match the whole path.
        if (query.IndexOfAny(new[] { '/', '\\' }) < 0 && Match(query, name) is { } inName)
        {
            var shifted = new int[inName.Positions.Length];
            for (var i = 0; i < shifted.Length; i++) shifted[i] = inName.Positions[i] + nameStart;
            // Shorter paths first among equals (src/a.cs before src/deep/x/a.cs).
            return new Result(inName.Score + 2000 - Math.Min(path.Length, 300), shifted);
        }
        if (Match(query, path) is { } inPath)
        {
            // Letters scattered over several folder names are noise ("ret" in "src/checkout/order_key.go").
            var span = inPath.Positions[^1] - inPath.Positions[0] + 1;
            if (span > query.Length * 2 + 2) return null;
            return new Result(inPath.Score - Math.Min(path.Length, 300) / 2, inPath.Positions);
        }
        return null;
    }

    /// <summary>
    /// Letters to highlight in a result the server found (a chat title, a
    /// memory): the match when it is tight, nothing when the letters are
    /// scattered over the text (that would only be noise).
    /// </summary>
    public static int[] Highlights(string query, string text)
    {
        query = (query ?? "").Replace(" ", "");
        if (query.Length == 0 || Match(query, text) is not { } m || m.Positions.Length == 0) return Array.Empty<int>();
        var span = m.Positions[^1] - m.Positions[0] + 1;
        return span > query.Length * 2 + 2 ? Array.Empty<int>() : m.Positions;
    }

    private static bool FitsAfter(string query, int from, string text, int start)
    {
        var ti = start;
        for (var qi = from; qi < query.Length; qi++)
        {
            var c = char.ToLowerInvariant(query[qi]);
            while (ti < text.Length && char.ToLowerInvariant(text[ti]) != c) ti++;
            if (ti >= text.Length) return false;
            ti++;
        }
        return true;
    }

    private static int IndexOfWordStart(string text, string query)
    {
        var from = 0;
        while (from <= text.Length - query.Length)
        {
            var at = text.IndexOf(query, from, StringComparison.OrdinalIgnoreCase);
            if (at < 0) return -1;
            if (IsWordStart(text, at)) return at;
            from = at + 1;
        }
        return -1;
    }

    /// <summary>The first letter, one after a separator, or an upper-case letter after a lower-case one (camelCase).</summary>
    internal static bool IsWordStart(string text, int i)
    {
        if (i <= 0) return true;
        var prev = text[i - 1];
        var c = text[i];
        if (!char.IsLetterOrDigit(prev)) return char.IsLetterOrDigit(c);
        return char.IsUpper(c) && char.IsLower(prev);
    }

    /// <summary>
    /// Splits <paramref name="text"/> into runs for display: each piece and
    /// whether it is part of the match.
    /// </summary>
    public static IReadOnlyList<(string Text, bool Hit)> Segments(string text, IReadOnlyCollection<int> positions)
    {
        var parts = new List<(string, bool)>();
        if (string.IsNullOrEmpty(text)) return parts;
        var hits = positions as ISet<int> ?? new HashSet<int>(positions);
        var start = 0;
        var hit = hits.Contains(0);
        for (var i = 1; i <= text.Length; i++)
        {
            var now = i < text.Length && hits.Contains(i);
            if (i < text.Length && now == hit) continue;
            parts.Add((text[start..i], hit));
            start = i;
            hit = now;
        }
        return parts;
    }
}
