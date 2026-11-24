#nullable enable
using System.Text.RegularExpressions;

namespace Nexus.Core;

/// Pages of a chat's turns, newest first.
///
/// The desktop asks sessions.turns for direction=backward (the newest
/// `limit` turns before an exclusive idx cursor, in idx order, next_cursor =
/// the first idx on the page). A server that does not know the parameter
/// ignores it and answers a forward page instead: the OLDEST turns, with
/// next_cursor = the last idx on the page. Painting that as "the newest
/// page" showed the start of the chat at the bottom, prepended later pages
/// above it and never loaded the newest turns. These helpers detect that
/// answer and cut backward pages out of a forward walk instead.
internal static class TurnPaging
{
    /// True when a response to a backward request is really a forward page.
    /// idxs are the page's turn indexes in the order received.
    public static bool LooksForward(IReadOnlyList<long> idxs, string nextCursor, long? before)
    {
        if (idxs.Count == 0) return false;
        // Backward pages only hold turns before the cursor.
        if (before is { } limit && idxs.Any(i => i >= limit)) return true;
        // Backward: next is the first idx on the page; forward: the last.
        if (idxs.Count < 2 || !long.TryParse(nextCursor, out var next)) return false;
        return next == idxs[^1] && next != idxs[0];
    }

    /// The newest `limit` turns before `before` (null: the end of the chat)
    /// from turns sorted by idx, and the cursor for the page before them
    /// ("" when this page starts the chat).
    public static (List<T> Page, string Next) Before<T>(IReadOnlyList<T> sorted, Func<T, long> idx, long? before, int limit)
    {
        var end = sorted.Count;
        if (before is { } cursor)
        {
            end = 0;
            while (end < sorted.Count && idx(sorted[end]) < cursor) end++;
        }
        var start = Math.Max(0, end - Math.Max(1, limit));
        var page = new List<T>(end - start);
        for (var i = start; i < end; i++) page.Add(sorted[i]);
        return (page, start > 0 && page.Count > 0 ? idx(page[0]).ToString(System.Globalization.CultureInfo.InvariantCulture) : "");
    }

    /// The server rejects cursors past a 32-bit int (HTTP 500).
    public const long MaxCursor = int.MaxValue;

    /// Cursors to probe for the end of a chat whose turn indexes are increasing
    /// but sparse. lo: a cursor with a full page after it; hi: a cursor with
    /// less than a page after it (null while unknown). Before the end is
    /// bracketed the probes double from lo; then they split (lo, hi) evenly.
    public static List<long> TailProbes(long lo, long? hi, int count)
    {
        var probes = new List<long>();
        if (count <= 0) return probes;
        lo = Math.Max(lo, 0);
        if (hi is null)
        {
            var next = Math.Max(lo, 1);
            for (var i = 0; i < count && next < MaxCursor; i++)
            {
                next = Math.Min(MaxCursor, next * 2);
                probes.Add(next);
            }
            return probes;
        }
        var top = Math.Min(hi.Value, MaxCursor);
        if (top - lo < 2) return probes;
        var step = (top - lo) / (double)(count + 1);
        for (var i = 1; i <= count; i++)
        {
            var cursor = lo + (long)Math.Round(step * i);
            if (cursor > lo && cursor < top && (probes.Count == 0 || probes[^1] != cursor)) probes.Add(cursor);
        }
        return probes;
    }
}

/// Hides turns a capture stored twice. Claude Code transcripts reach the
/// server through two paths: the live capture (rows with source_ts) and a
/// later import (no source_ts) that repeats some of the same messages: a
/// prompt again with a system reminder or a queued task notification
/// wrapped around it, a reply again with its tool calls, and sometimes a
/// block of earlier replies replayed after a later prompt. Once the
/// wrappers are stripped the texts are identical.
///
/// A turn is hidden when it repeats the turn just shown (same role and text),
/// or when it has no source time and the same role and text were already
/// shown anywhere in the chat. Short texts ("yes", "continue") only collapse
/// when adjacent: a person may well send them twice. When pages arrive
/// newest first, a copy can be shown before its original; the original then
/// supersedes it (the caller removes the copy).
///
/// Messages a person queued while the agent was busy: Claude Code records
/// each one as its own prompt (that is what the person saw), and the import
/// adds one more turn with all of them joined by blank lines. That joined
/// turn is hidden when it repeats the prompts shown just before it.
///
/// Compaction summaries ("This session is being continued…") are stored by
/// the capture and again by the import, cut at different lengths: of two in
/// a row only the last stays (the earlier one is superseded).
internal sealed class TranscriptDedupe
{
    internal const int MinRepeatLength = 24;
    private readonly HashSet<string> _shown = new(StringComparer.Ordinal);
    private readonly Dictionary<string, object> _copies = new(StringComparer.Ordinal);
    // The prompts shown back to back since the last other turn (text part of the signature).
    private readonly List<(string Text, object Element)> _prompts = new();
    private string _last = "";
    private object? _lastElement;

    public static string Signature(string role, string text) =>
        role.Trim().ToLowerInvariant() + "\n" + Regex.Replace(text ?? "", @"\s+", " ").Trim();

    private static readonly Regex CompactionLead = new(@"^\s*This session is being continued from a previous conversation\b", RegexOptions.IgnoreCase | RegexOptions.CultureInvariant | RegexOptions.Compiled);
    private static readonly Regex InterruptNote = new(@"^\s*\[Request interrupted by user(?: for tool use)?\]\s*$", RegexOptions.IgnoreCase | RegexOptions.CultureInvariant | RegexOptions.Compiled);

    /// The summary Claude Code puts in a user turn after compacting the chat.
    public static bool IsCompactionSummary(string? text) => text is not null && CompactionLead.IsMatch(text);

    /// "[Request interrupted by user]": the harness's note, not a prompt.
    public static bool IsInterruptNote(string? text) => text is not null && InterruptNote.IsMatch(text);

    public void Clear()
    {
        _shown.Clear();
        _copies.Clear();
        _prompts.Clear();
        _last = "";
        _lastElement = null;
    }

    /// Whether to show a turn. lastShown is the element now at the end of the
    /// list being built (adjacency holds only while it is the previous turn's
    /// element). superseded: an earlier-rendered copy this turn replaces.
    public bool Admit(string signature, bool sourced, object? lastShown, out object? superseded)
    {
        superseded = null;
        var adjacent = lastShown is not null && ReferenceEquals(lastShown, _lastElement);
        if (adjacent && signature == _last) return false;
        var repeatable = signature.Length - signature.IndexOf('\n') - 1 >= MinRepeatLength;
        if (!sourced && repeatable && _shown.Contains(signature)) return false;
        if (adjacent && JoinsQueuedPrompts(signature)) return false;
        if (sourced && _copies.Remove(signature, out var copy)) superseded = copy;
        // Two compaction summaries in a row: the later one replaces the earlier.
        if (superseded is null && adjacent && IsUser(signature) && IsUser(_last) &&
            IsCompactionSummary(Text(signature)) && IsCompactionSummary(Text(_last)))
            superseded = _lastElement;
        return true;
    }

    /// Records a turn that was shown as `element`.
    public void Shown(string signature, bool sourced, object element)
    {
        _shown.Add(signature);
        if (!sourced) _copies.TryAdd(signature, element);
        if (!IsUser(signature) || IsCompactionSummary(Text(signature))) _prompts.Clear();
        else
        {
            if (_prompts.Count > 0 && !ReferenceEquals(_prompts[^1].Element, _lastElement)) _prompts.Clear();
            _prompts.Add((Text(signature), element));
        }
        _last = signature;
        _lastElement = element;
    }

    // The text equals two or more of the prompts just shown, joined in order.
    private bool JoinsQueuedPrompts(string signature)
    {
        if (!IsUser(signature) || _prompts.Count < 2 || !ReferenceEquals(_prompts[^1].Element, _lastElement)) return false;
        var text = Text(signature);
        var joined = _prompts[^1].Text;
        for (var i = _prompts.Count - 2; i >= 0; i--)
        {
            joined = _prompts[i].Text + " " + joined;
            if (joined.Length > text.Length) return false;
            if (joined == text) return true;
        }
        return false;
    }

    private static bool IsUser(string signature) => signature.StartsWith("user\n", StringComparison.Ordinal);

    private static string Text(string signature) => signature[(signature.IndexOf('\n') + 1)..];

    /// Same rule for a list of prompts in order (the conversation index):
    /// whether `text` is the prompts just before it joined together.
    public static bool JoinsPrompts(IReadOnlyList<string> previous, string text)
    {
        if (previous.Count < 2) return false;
        var target = Text(Signature("user", text));
        var joined = Text(Signature("user", previous[^1]));
        for (var i = previous.Count - 2; i >= 0; i--)
        {
            joined = Text(Signature("user", previous[i])) + " " + joined;
            if (joined.Length > target.Length) return false;
            if (joined == target) return true;
        }
        return false;
    }
}
