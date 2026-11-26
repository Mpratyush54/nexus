#nullable enable
namespace Nexus.Core;

/// <summary>
/// ↑ in an empty composer brings back the last prompt sent in this chat;
/// more ↑ goes further back and ↓ comes forward again, ending at the empty
/// composer (as in Claude Code and a shell). Pure, so it is tested without a
/// window; mirrors recallStep in frontend/src/lib/composer-keys.ts.
/// </summary>
internal sealed class PromptRecall
{
    private IReadOnlyList<string> _history = Array.Empty<string>();

    /// <summary>-1 while the composer shows what the person typed; otherwise the index (0 = newest) shown.</summary>
    public int Index { get; private set; } = -1;

    /// <summary>The prompt on show, or null.</summary>
    public string? Shown => Index >= 0 && Index < _history.Count ? _history[Index] : null;

    /// <summary>
    /// Earlier prompts, newest first, without repeats or blanks. The ones
    /// sent from this composer (exactly as typed) come before the
    /// transcript's, which are oldest first.
    /// </summary>
    public static IReadOnlyList<string> Build(IEnumerable<string> sentHere, IEnumerable<string> transcriptOldestFirst, int max = 100)
    {
        var seen = new HashSet<string>(StringComparer.Ordinal);
        var list = new List<string>();
        void Add(string text)
        {
            var t = (text ?? "").Trim();
            if (t.Length == 0 || list.Count >= max || !seen.Add(t)) return;
            list.Add(t);
        }
        foreach (var text in sentHere.Reverse()) Add(text);
        foreach (var text in transcriptOldestFirst.Reverse()) Add(text);
        return list;
    }

    /// <summary>Forget the walk (the chat changed, a message was sent, or the person typed).</summary>
    public void Reset(IReadOnlyList<string>? history = null)
    {
        if (history is not null) _history = history;
        Index = -1;
    }

    /// <summary>
    /// ↑ (older) or ↓ (newer). Returns the text to show (an empty string for
    /// back to empty), or null when the key should move the caret as usual:
    /// the composer has text the person typed, the caret is not at the edge,
    /// or there is nothing further.
    /// </summary>
    public string? Step(bool up, string composerText, int caret, IReadOnlyList<string> history)
    {
        composerText ??= "";
        _history = history;
        var untouched = composerText.Length == 0 || (Shown is { } shown && composerText == shown);
        if (!untouched) { Index = -1; return null; }
        if (composerText.Length > 0)
        {
            // A recalled multi-line prompt: ↑/↓ move between its lines until the caret is at its edge.
            var atEdge = up ? !composerText[..Math.Clamp(caret, 0, composerText.Length)].Contains('\n')
                            : !composerText[Math.Clamp(caret, 0, composerText.Length)..].Contains('\n');
            if (!atEdge) return null;
        }
        if (up)
        {
            if (Index + 1 >= history.Count) return null;
            Index++;
            return history[Index];
        }
        if (Index < 0) return null;
        Index--;
        return Index >= 0 ? history[Index] : "";
    }
}
