namespace Nexus.Core;

/// <summary>
/// How dictated words merge with what is already in the chat box (voice
/// input, Pages/VoiceInput.cs). Pure string logic, no UI, so it is unit
/// tested (ProjectScope.Tests/DictationTextTests.cs) and matches the web
/// portal's mergeDictation.
///
/// Rules, the same as the dictation in ChatGPT, Claude and Cursor:
///   - the words go in at the caret (or replace the selection);
///   - a space separates them from a word typed before or after, never
///     before punctuation the recognizer put at the start;
///   - the first letter is capitalised at the start of the box or of a
///     sentence, otherwise left as the recognizer wrote it;
///   - punctuation comes from the recognizer and is kept as is.
/// </summary>
public static class DictationText
{
    private const string LeadingPunctuation = ".,;:!?)]}…%";
    private const string OpenersBefore = "([{\"'“‘/@#`";
    private const string SentenceEnds = ".!?…";

    /// <summary>
    /// Appends one recognizer phrase (or the current interim words) to the
    /// phrases already recognized this session.
    /// </summary>
    public static string Join(string committed, string next)
    {
        var a = Tidy(committed);
        var b = Tidy(next);
        if (a.Length == 0) return b;
        if (b.Length == 0) return a;
        if (LeadingPunctuation.Contains(b[0])) return a + b;
        return a + " " + (StartsSentence(a) ? Capitalize(b) : b);
    }

    /// <summary>
    /// The text to put between <paramref name="before"/> and
    /// <paramref name="after"/> (the box's text on each side of the caret)
    /// for <paramref name="dictated"/>: the words plus the spaces and
    /// capital the context needs. Empty when there is nothing to insert.
    /// </summary>
    public static string Insertion(string before, string after, string dictated)
    {
        var words = Tidy(dictated);
        if (words.Length == 0) return "";
        before ??= "";
        after ??= "";
        if (StartsSentence(before)) words = Capitalize(words);
        var lead = before.Length > 0
                   && !char.IsWhiteSpace(before[^1])
                   && !OpenersBefore.Contains(before[^1])
                   && !LeadingPunctuation.Contains(words[0]);
        var trail = after.Length > 0 && (char.IsLetterOrDigit(after[0]) || after[0] is '(' or '"' or '“');
        return (lead ? " " : "") + words + (trail ? " " : "");
    }

    /// <summary>
    /// The whole box after inserting <paramref name="dictated"/> at
    /// <paramref name="start"/>, replacing <paramref name="length"/>
    /// characters (a selection), and where the caret ends up.
    /// </summary>
    public static (string Text, int Caret) Merge(string text, int start, int length, string dictated)
    {
        text ??= "";
        start = Math.Clamp(start, 0, text.Length);
        length = Math.Clamp(length, 0, text.Length - start);
        var before = text[..start];
        var after = text[(start + length)..];
        var insert = Insertion(before, after, dictated);
        if (insert.Length == 0) return (text, start + length);
        return (before + insert + after, start + insert.Length);
    }

    /// <summary>True at the start of the box, a new line, or after . ! ? …</summary>
    public static bool StartsSentence(string before)
    {
        if (string.IsNullOrEmpty(before)) return true;
        if (before[^1] is '\n' or '\r') return true;
        var trimmed = before.TrimEnd();
        if (trimmed.Length == 0) return true;
        return SentenceEnds.Contains(trimmed[^1]);
    }

    private static string Capitalize(string words)
    {
        // "iPhone", "macOS": a capital inside the first word means it is
        // already written the way it should be.
        var first = words.Split(' ', 2)[0];
        if (first.Skip(1).Any(char.IsUpper)) return words;
        for (var i = 0; i < words.Length; i++)
        {
            if (!char.IsLetter(words[i])) continue;
            if (char.IsUpper(words[i])) return words;
            return words[..i] + char.ToUpperInvariant(words[i]) + words[(i + 1)..];
        }
        return words;
    }

    /// <summary>Trims and folds runs of spaces and tabs (recognizers sometimes double them).</summary>
    public static string Tidy(string? text)
    {
        if (string.IsNullOrWhiteSpace(text)) return "";
        var chars = new System.Text.StringBuilder(text.Length);
        var space = false;
        foreach (var ch in text.Trim())
        {
            if (ch is ' ' or '\t')
            {
                if (!space) chars.Append(' ');
                space = true;
                continue;
            }
            space = false;
            chars.Append(ch);
        }
        return chars.ToString();
    }
}
