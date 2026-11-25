#nullable enable
namespace Nexus.Core;

internal sealed record ConversationEntry(string Key, string Role, string Text, string Timestamp, int Page, long Order);

internal sealed class ConversationIndex
{
    private readonly Dictionary<string, ConversationEntry> _entries = new(StringComparer.Ordinal);
    private IReadOnlyList<ConversationEntry>? _ordered;
    private IReadOnlyList<ConversationEntry>? _prompts;
    internal IReadOnlyList<ConversationEntry> Entries => _ordered ??= _entries.Values.OrderBy(e => e.Order).ThenBy(e => e.Role == "user" ? 0 : 1).ThenBy(e => e.Key, StringComparer.Ordinal).ToArray();
    // The harness's "[Request interrupted by user]" note is indexed (a joined
    // queued prompt may repeat it) but is not a prompt to jump to.
    internal IReadOnlyList<ConversationEntry> Prompts => _prompts ??= Entries.Where(e => e.Role == "user" && !TranscriptDedupe.IsInterruptNote(e.Text)).ToArray();
    internal long NextOrder => _entries.Count == 0 ? 0 : _entries.Values.Max(e => e.Order) + 1;
    // Turns a capture stored twice (see TranscriptDedupe): signatures indexed,
    // and the key of a copy (no source time) kept for a signature.
    private readonly HashSet<string> _signatures = new(StringComparer.Ordinal);
    private readonly Dictionary<string, string> _copyKeys = new(StringComparer.Ordinal);
    internal void Clear() { _entries.Clear(); _signatures.Clear(); _copyKeys.Clear(); _ordered = _prompts = null; }
    internal void Remove(string key) { _entries.Remove(key); _ordered = _prompts = null; }
    internal bool Contains(string key) => _entries.ContainsKey(key);
    internal ConversationEntry? Find(string key) => _entries.GetValueOrDefault(key);
    /// copy: the turn has no source time, so it may repeat a captured one. A
    /// long copy of a message already indexed is left out, and a captured
    /// message replaces a copy indexed before it (pages arrive newest first).
    internal void Add(ConversationEntry entry, bool copy = false)
    {
        if (entry.Role is not ("user" or "assistant") || string.IsNullOrWhiteSpace(entry.Text) || TranscriptText.IsOpaque(entry.Text)) return;
        // A compaction summary is shown as a collapsed card, not a prompt.
        if (entry.Role == "user" && TranscriptDedupe.IsCompactionSummary(entry.Text)) return;
        if (!_entries.ContainsKey(entry.Key))
        {
            // Queued prompts joined into one turn after the prompts themselves (see TranscriptDedupe).
            if (entry.Role == "user" && TranscriptDedupe.JoinsPrompts(PromptsBefore(entry.Order), entry.Text)) return;
            var signature = TranscriptDedupe.Signature(entry.Role, entry.Text);
            var repeatable = entry.Text.Trim().Length >= TranscriptDedupe.MinRepeatLength;
            if (copy && repeatable && _signatures.Contains(signature)) return;
            if (!copy && _copyKeys.Remove(signature, out var copyKey)) _entries.Remove(copyKey);
            _signatures.Add(signature);
            if (copy) _copyKeys.TryAdd(signature, entry.Key);
        }
        _entries[entry.Key] = entry;
        _ordered = _prompts = null;
    }
    // The user entries directly before `order` (oldest first), back to the previous reply.
    private List<string> PromptsBefore(long order)
    {
        var run = new List<string>();
        foreach (var e in _entries.Values.Where(e => e.Order < order).OrderByDescending(e => e.Order).ThenBy(e => e.Role == "user" ? 1 : 0))
        {
            if (e.Role != "user") break;
            run.Insert(0, e.Text);
        }
        return run;
    }

    internal IReadOnlyList<ConversationEntry> Search(string query)
    {
        query = query.Trim();
        return query.Length == 0 ? [] : Entries.Where(e => e.Text.Contains(query, StringComparison.OrdinalIgnoreCase)).ToArray();
    }

    internal static string Preview(string text, string query = "", int length = 220)
    {
        text = string.Join(" ", text.Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries));
        var at = query.Length > 0 ? text.IndexOf(query, StringComparison.OrdinalIgnoreCase) : -1;
        var start = at < 0 ? 0 : Math.Max(0, at - 70);
        var count = Math.Min(length, text.Length - start);
        return (start > 0 ? "…" : "") + text.Substring(start, count) + (start + count < text.Length ? "…" : "");
    }
}

internal static class TurnIndex
{
    /// The rendered reply ("turn:&lt;idx&gt;") that a saved Nexus exchange with
    /// this answer index belongs to. Turn indexes are int64 values beyond
    /// double precision; an older core passed them through as doubles
    /// (179533922286017891 came back as …900), so a reply whose key differs
    /// only by that rounding is the same turn. Prompt keys (":user") never match.
    public static string? FindReplyKey(IEnumerable<string> keys, string answerIdx)
    {
        if (answerIdx.Length == 0 || answerIdx == "0") return null;
        var exact = "turn:" + answerIdx;
        string? rounded = null;
        var hasNumber = long.TryParse(answerIdx, out var wanted) && wanted > 0;
        foreach (var key in keys)
        {
            if (key == exact) return key;
            if (rounded is null && hasNumber && key.StartsWith("turn:", StringComparison.Ordinal) &&
                !key.EndsWith(":user", StringComparison.Ordinal) &&
                long.TryParse(key.AsSpan(5), out var shown) && shown > 0 && (double)shown == (double)wanted)
                rounded = key;
        }
        return rounded;
    }
}
