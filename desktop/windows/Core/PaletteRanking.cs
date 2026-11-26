namespace Nexus.Core;

/// <summary>
/// Ranking for the command palette (Pages/CommandPalette.cs): fuzzy matching
/// of what was typed against each item's title and keywords, recent picks
/// first. Pure and fast (a few hundred items in well under a millisecond), so
/// results update on every keystroke from memory.
/// </summary>
internal static class PaletteRanking
{
    /// <summary>One candidate. <paramref name="Keywords"/> match too but count a little less than the title.</summary>
    internal sealed record Entry(string Id, string Group, string Title, string Keywords = "", int GroupOrder = 0);

    internal sealed record Ranked(Entry Entry, double Score);

    /// <summary>
    /// How well <paramref name="query"/> matches <paramref name="text"/>; 0
    /// means no match. Exact, then prefix, then the start of a word (also
    /// initials: "nc" for "New chat"), then a substring, then the letters in
    /// order (a subsequence, worth more when they bunch together).
    /// </summary>
    public static double Score(string query, string text)
    {
        query = query.Trim();
        if (query.Length == 0) return 1;
        if (string.IsNullOrEmpty(text)) return 0;
        var q = query.ToLowerInvariant();
        var t = text.ToLowerInvariant();
        if (t == q) return 1000;
        if (t.StartsWith(q, StringComparison.Ordinal)) return 900 - Math.Min(100, t.Length - q.Length);
        var words = WordStarts(t);
        foreach (var start in words)
            if (string.CompareOrdinal(t, start, q, 0, q.Length) == 0)
                return 800 - Math.Min(100, start);
        // Every typed word found somewhere ("chat new" finds "New chat").
        var terms = q.Split(' ', StringSplitOptions.RemoveEmptyEntries);
        if (terms.Length > 1 && terms.All(term => t.Contains(term, StringComparison.Ordinal)))
            return 700 - Math.Min(100, t.Length / 4);
        if (Initials(t, words).StartsWith(q, StringComparison.Ordinal) && q.Length >= 2) return 650;
        var at = t.IndexOf(q, StringComparison.Ordinal);
        if (at >= 0) return 600 - Math.Min(100, at);
        return Subsequence(q, t);
    }

    private static List<int> WordStarts(string t)
    {
        var starts = new List<int>();
        for (var i = 0; i < t.Length; i++)
            if (char.IsLetterOrDigit(t[i]) && (i == 0 || !char.IsLetterOrDigit(t[i - 1]))) starts.Add(i);
        return starts;
    }

    private static string Initials(string t, List<int> starts) => new(starts.Select(i => t[i]).ToArray());

    // Letters in order; 100..400 by how tightly they cluster. Spaces in the
    // query are ignored here.
    private static double Subsequence(string q, string t)
    {
        var letters = q.Replace(" ", "");
        if (letters.Length < 2) return 0;
        var ti = 0;
        var first = -1;
        var last = -1;
        foreach (var c in letters)
        {
            var found = t.IndexOf(c, ti);
            if (found < 0) return 0;
            if (first < 0) first = found;
            last = found;
            ti = found + 1;
        }
        var span = last - first + 1;
        var tightness = (double)letters.Length / span; // 1 = adjacent
        return 100 + 300 * tightness;
    }

    /// <summary>
    /// The matches, best first. With an empty query: recent picks in recency
    /// order, then everything else in its given order. A recent pick also
    /// gets a boost while typing, so the chat you opened a minute ago wins a
    /// tie against one you never open.
    /// </summary>
    public static IReadOnlyList<Ranked> Rank(IEnumerable<Entry> entries, string query, IReadOnlyList<string> recent, int limit = 50)
    {
        query = query.Trim();
        var recency = new Dictionary<string, int>(StringComparer.Ordinal);
        for (var i = 0; i < recent.Count; i++) recency.TryAdd(recent[i], i);
        var list = new List<Ranked>();
        var order = 0;
        var seen = new HashSet<string>(StringComparer.Ordinal);
        foreach (var e in entries)
        {
            order++;
            if (!seen.Add(e.Id)) continue;
            double score;
            if (query.Length == 0)
            {
                // Recent first (most recent highest), then group order, then given order.
                score = recency.TryGetValue(e.Id, out var r) ? 10000 - r : 5000 - e.GroupOrder * 100 - Math.Min(99, order / 10.0);
            }
            else
            {
                var title = Score(query, e.Title);
                var keywords = e.Keywords.Length > 0 ? Score(query, e.Keywords) * 0.85 : 0;
                score = Math.Max(title, keywords);
                if (score <= 0) continue;
                if (recency.TryGetValue(e.Id, out var r)) score += Math.Max(0, 60 - r * 6);
                score -= e.GroupOrder * 0.5; // stable tie-break by group
            }
            list.Add(new Ranked(e, score));
        }
        return list
            .Select((r, index) => (r, index))
            .OrderByDescending(x => x.r.Score)
            .ThenBy(x => x.index)
            .Take(limit)
            .Select(x => x.r)
            .ToList();
    }

    /// <summary>
    /// Groups for display, ordered by each group's best result, items in rank
    /// order. Keyboard order (↑/↓) follows this, top to bottom.
    /// </summary>
    public static IReadOnlyList<(string Group, IReadOnlyList<Ranked> Items)> Grouped(IReadOnlyList<Ranked> ranked, int perGroup = 8)
    {
        return ranked
            .GroupBy(r => r.Entry.Group)
            .Select(g => (Group: g.Key, Items: (IReadOnlyList<Ranked>)g.Take(perGroup).ToList(), Best: g.Max(r => r.Score)))
            .OrderByDescending(g => g.Best)
            .Select(g => (g.Group, g.Items))
            .ToList();
    }

    /// <summary>Puts <paramref name="id"/> first in a most-recent-first list of at most <paramref name="max"/>.</summary>
    public static List<string> Remember(IEnumerable<string> recent, string id, int max = 12)
    {
        var list = recent.Where(r => r != id).ToList();
        list.Insert(0, id);
        if (list.Count > max) list.RemoveRange(max, list.Count - max);
        return list;
    }
}
