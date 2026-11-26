#nullable enable
using System.Text;
using System.Text.RegularExpressions;

namespace Nexus.Core;

internal enum MdKind { Paragraph, Heading, ListItem, Quote, Code, Table, Rule }

/// <summary>
/// One block of a chat message. <see cref="Start"/>/<see cref="End"/> are
/// offsets in the source text; <see cref="Source"/> is that slice, so two
/// parses of a growing reply can be compared block by block.
/// </summary>
internal sealed class MdBlock
{
    public MdKind Kind { get; init; }
    /// Display text: a paragraph's lines, a heading or item without its
    /// marker, a fence's code without the fences, a quote without "&gt;".
    public string Text { get; init; } = "";
    public string Source { get; init; } = "";
    public int Start { get; init; }
    public int End { get; init; }
    /// Heading level 1-6.
    public int Level { get; init; }
    /// List nesting depth (0 = top level; one level per two spaces).
    public int Indent { get; init; }
    /// "1." for an ordered item (the number as written), "" for a bullet.
    public string Number { get; init; } = "";
    /// The number shown: CommonMark takes a list's start from its first
    /// item and counts up from there ("1. 2. 10." shows 1. 2. 3.). Set by
    /// <see cref="MarkdownBlocks.NumberLists"/>; "" for bullets.
    public string Ordinal { get; internal set; } = "";
    /// Task list state: null when not a task, else checked or not.
    public bool? Task { get; init; }
    /// The fence's info string ("python", "diff"), lower case.
    public string Language { get; init; } = "";
    /// A fence that has its closing line (always true for other kinds).
    public bool Closed { get; init; } = true;
    /// Table rows (header first), the |---| rule left out.
    public IReadOnlyList<string> Rows { get; init; } = Array.Empty<string>();

    public bool SameAs(MdBlock other) =>
        Kind == other.Kind && Closed == other.Closed && string.Equals(Ordinal, other.Ordinal, StringComparison.Ordinal) &&
        string.Equals(Source, other.Source, StringComparison.Ordinal);

    public override string ToString() => Kind + (Kind == MdKind.Code ? "(" + Language + (Closed ? "" : ",open") + ")" : "") + ": " + Text;
}

/// <summary>
/// Block-level Markdown for chat replies: paragraphs (line breaks kept),
/// ATX headings, bullet/numbered/task lists with nesting, quotes, fenced
/// code, pipe tables and rules. Deliberately small: what agents write, parsed
/// in one pass, with no HTML.
/// </summary>
internal static partial class MarkdownBlocks
{
    public static List<MdBlock> Parse(string? text)
    {
        var blocks = Parse(text ?? "", 0);
        NumberLists(blocks);
        return blocks;
    }

    /// <summary>
    /// Sets <see cref="MdBlock.Ordinal"/> on ordered items: each list (items
    /// at one indent with nothing but deeper items between them) starts at
    /// its first item's number and counts up. Ordinals depend only on the
    /// blocks before an item, so blocks that are final keep theirs.
    /// </summary>
    public static void NumberLists(IReadOnlyList<MdBlock> blocks)
    {
        var runs = new Dictionary<int, (bool Ordered, long Next)>();
        foreach (var block in blocks)
        {
            if (block.Kind != MdKind.ListItem)
            {
                runs.Clear();
                continue;
            }
            foreach (var deeper in runs.Keys.Where(k => k > block.Indent).ToList()) runs.Remove(deeper);
            var ordered = block.Number.Length > 0;
            if (!runs.TryGetValue(block.Indent, out var run) || run.Ordered != ordered)
                run = (ordered, ordered && long.TryParse(block.Number.TrimEnd('.'), out var start) ? start : 1);
            block.Ordinal = ordered ? run.Next + "." : "";
            runs[block.Indent] = (ordered, run.Next + 1);
        }
    }

    /// Parses text[start..]; offsets in the result are absolute.
    public static List<MdBlock> Parse(string text, int start)
    {
        var blocks = new List<MdBlock>();
        var lines = Lines(text, start);
        var i = 0;
        while (i < lines.Count)
        {
            var line = lines[i];
            if (IsBlank(line.Text)) { i++; continue; }
            if (Fence(line.Text) is { } fence)
            {
                var body = new StringBuilder();
                var j = i + 1;
                var closed = false;
                for (; j < lines.Count; j++)
                {
                    if (IsClosingFence(lines[j].Text, fence.Marker)) { closed = true; break; }
                    if (body.Length > 0 || j > i + 1) body.Append('\n');
                    body.Append(Dedent(lines[j].Text, fence.Indent));
                }
                var end = closed ? lines[j].End : lines[^1].End;
                blocks.Add(Make(text, MdKind.Code, body.ToString(), line.Start, end).With(language: fence.Language, closed: closed));
                i = closed ? j + 1 : lines.Count;
                continue;
            }
            if (HeadingLine().Match(line.Text) is { Success: true } heading)
            {
                blocks.Add(Make(text, MdKind.Heading, heading.Groups[2].Value.Trim(), line.Start, line.End).With(level: heading.Groups[1].Value.Length));
                i++;
                continue;
            }
            if (IsRule(line.Text))
            {
                blocks.Add(Make(text, MdKind.Rule, "", line.Start, line.End));
                i++;
                continue;
            }
            if (IsTableRow(line.Text) && i + 1 < lines.Count && IsTableRule(lines[i + 1].Text))
            {
                var rows = new List<string> { line.Text.Trim() };
                var j = i + 2;
                while (j < lines.Count && IsTableRow(lines[j].Text)) rows.Add(lines[j++].Text.Trim());
                blocks.Add(Make(text, MdKind.Table, string.Join("\n", rows), line.Start, lines[j - 1].End).With(rows: rows));
                i = j;
                continue;
            }
            if (ListLine().Match(line.Text) is { Success: true } item)
            {
                var content = item.Groups[3].Value;
                bool? task = null;
                if (TaskMarker().Match(content) is { Success: true } t)
                {
                    task = t.Groups[1].Value != " ";
                    content = content[t.Length..];
                }
                var body = new StringBuilder(content.TrimEnd());
                var j = i + 1;
                while (j < lines.Count && !IsBlank(lines[j].Text) && !StartsBlock(lines, j))
                    body.Append('\n').Append(lines[j++].Text.Trim());
                var marker = item.Groups[2].Value;
                blocks.Add(Make(text, MdKind.ListItem, body.ToString(), line.Start, lines[j - 1].End).With(
                    indent: Math.Min(6, Columns(item.Groups[1].Value) / 2),
                    number: char.IsDigit(marker[0]) ? marker[..^1] + "." : "",
                    task: task));
                i = j;
                continue;
            }
            if (QuoteLine().IsMatch(line.Text))
            {
                var body = new StringBuilder();
                var j = i;
                while (j < lines.Count && QuoteLine().Match(lines[j].Text) is { Success: true } q)
                {
                    if (j > i) body.Append('\n');
                    body.Append(q.Groups[1].Value.TrimEnd());
                    j++;
                }
                blocks.Add(Make(text, MdKind.Quote, body.ToString(), line.Start, lines[j - 1].End));
                i = j;
                continue;
            }
            {
                // A paragraph: lines up to a blank line or the start of another block.
                var body = new StringBuilder(line.Text.Trim());
                var j = i + 1;
                // A lone pipe row starts its own block, so a table that is
                // still arriving never merges into the paragraph above it.
                if (!IsTableRow(line.Text))
                    while (j < lines.Count && !IsBlank(lines[j].Text) && !StartsBlock(lines, j))
                        body.Append('\n').Append(lines[j++].Text.Trim());
                blocks.Add(Make(text, MdKind.Paragraph, body.ToString(), line.Start, lines[j - 1].End));
                i = j;
            }
        }
        return blocks;
    }

    // Whether line j begins a block other than a paragraph continuation.
    // Depends on that line alone, so a block that ended stays ended however
    // the text after it grows (see MarkdownStream).
    private static bool StartsBlock(List<Line> lines, int j)
    {
        var t = lines[j].Text;
        return Fence(t) is not null || HeadingLine().IsMatch(t) || IsRule(t) || IsTableRow(t) ||
               ListLine().IsMatch(t) || QuoteLine().IsMatch(t);
    }

    private static MdBlock Make(string text, MdKind kind, string body, int start, int end) =>
        new() { Kind = kind, Text = body, Start = start, End = end, Source = text[start..end] };

    // MdBlock is a class with init-only properties; this copies one with a
    // few of them changed (records would compare lists by reference).
    private static MdBlock With(this MdBlock b, int? level = null, int? indent = null, string? number = null, bool? task = null,
        string? language = null, bool? closed = null, IReadOnlyList<string>? rows = null) => new()
    {
        Kind = b.Kind, Text = b.Text, Source = b.Source, Start = b.Start, End = b.End,
        Level = level ?? b.Level, Indent = indent ?? b.Indent, Number = number ?? b.Number,
        Task = task ?? b.Task, Language = language ?? b.Language, Closed = closed ?? b.Closed, Rows = rows ?? b.Rows,
    };

    internal readonly record struct Line(string Text, int Start, int End);

    // Lines of text[start..] with absolute offsets; End excludes the newline
    // (and a trailing \r).
    internal static List<Line> Lines(string text, int start)
    {
        var lines = new List<Line>();
        var at = start;
        while (at <= text.Length)
        {
            var nl = text.IndexOf('\n', at);
            var end = nl < 0 ? text.Length : nl;
            var trimmedEnd = end > at && text[end - 1] == '\r' ? end - 1 : end;
            if (nl < 0 && at == text.Length && lines.Count > 0) break;
            lines.Add(new Line(text[at..trimmedEnd], at, trimmedEnd));
            if (nl < 0) break;
            at = nl + 1;
        }
        return lines;
    }

    private static bool IsBlank(string line) => string.IsNullOrWhiteSpace(line);

    internal sealed record FenceInfo(string Marker, int Indent, string Language);

    internal static FenceInfo? Fence(string line)
    {
        var indent = 0;
        while (indent < line.Length && line[indent] == ' ') indent++;
        if (indent > 3 && !(indent < line.Length && (line[indent] == '`' || line[indent] == '~'))) return null;
        if (indent >= line.Length) return null;
        var ch = line[indent];
        if (ch != '`' && ch != '~') return null;
        var run = 0;
        while (indent + run < line.Length && line[indent + run] == ch) run++;
        if (run < 3) return null;
        var info = line[(indent + run)..].Trim();
        // ``` inside the info string means inline code, not a fence.
        if (ch == '`' && info.Contains('`')) return null;
        var language = info.Split(' ', 2)[0].Trim('{', '}', '.').ToLowerInvariant();
        return new FenceInfo(new string(ch, run), indent, language);
    }

    private static bool IsClosingFence(string line, string marker)
    {
        var t = line.Trim();
        if (t.Length < marker.Length || t[0] != marker[0]) return false;
        foreach (var c in t) if (c != marker[0]) return false;
        return true;
    }

    private static string Dedent(string line, int indent)
    {
        var n = 0;
        while (n < indent && n < line.Length && line[n] == ' ') n++;
        return line[n..];
    }

    private static int Columns(string whitespace)
    {
        var n = 0;
        foreach (var c in whitespace) n += c == '\t' ? 4 : 1;
        return n;
    }

    internal static bool IsRule(string line) => RuleLine().IsMatch(line);

    internal static bool IsTableRow(string line)
    {
        var t = line.Trim();
        return t.Length > 2 && t[0] == '|' && t.IndexOf('|', 1) > 0;
    }

    internal static bool IsTableRule(string line) => line.Contains('|') && TableRuleLine().IsMatch(line.Trim());

    /// Cells of a pipe-table row; an escaped \| stays in the cell.
    internal static List<string> TableCells(string row)
    {
        var t = row.Trim();
        if (t.StartsWith('|')) t = t[1..];
        if (t.EndsWith('|') && !t.EndsWith("\\|", StringComparison.Ordinal)) t = t[..^1];
        return CellSplit().Split(t).Select(c => c.Trim().Replace("\\|", "|")).ToList();
    }

    [GeneratedRegex(@"^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$")] private static partial Regex HeadingLine();
    [GeneratedRegex(@"^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$")] private static partial Regex RuleLine();
    [GeneratedRegex(@"^([ \t]*)([-*+]|\d{1,9}[.)])[ \t]+(.*)$")] private static partial Regex ListLine();
    [GeneratedRegex(@"^\[([ xX])\][ \t]+")] private static partial Regex TaskMarker();
    [GeneratedRegex(@"^ {0,3}>[ ]?(.*)$")] private static partial Regex QuoteLine();
    [GeneratedRegex(@"^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?$")] private static partial Regex TableRuleLine();
    [GeneratedRegex(@"(?<!\\)\|")] private static partial Regex CellSplit();
}

/// <summary>
/// A reply that grows while it streams. Blocks that can no longer change
/// (another block has started after them on a finished line) are kept;
/// each update re-parses only the text after them. The last block is the
/// one still arriving; <see cref="Partial"/> makes its inline markers safe
/// to show half-written.
/// </summary>
internal sealed class MarkdownStream
{
    private readonly List<MdBlock> _committed = new();
    private string _committedPrefix = "";

    /// The blocks of the text last given to <see cref="Update"/>.
    public IReadOnlyList<MdBlock> Blocks { get; private set; } = Array.Empty<MdBlock>();

    /// Blocks that are final; the rest (at most one) may still change.
    public int CommittedCount => _committed.Count;

    public IReadOnlyList<MdBlock> Update(string text)
    {
        text ??= "";
        if (!text.StartsWith(_committedPrefix, StringComparison.Ordinal))
        {
            // Not a continuation (the reply was replaced): start over.
            _committed.Clear();
            _committedPrefix = "";
        }
        var from = _committedPrefix.Length;
        var tail = MarkdownBlocks.Parse(text, from);
        // The last line is final only once its newline has arrived.
        var lastNewline = text.LastIndexOf('\n');
        var finishedLinesEnd = lastNewline < 0 ? 0 : lastNewline + 1;
        var keep = 0;
        for (var i = 0; i + 1 < tail.Count; i++)
        {
            if (!tail[i].Closed) break;
            // Final when the next block starts on a finished line, or a
            // finished blank line (or a closing fence) already ended it.
            var gap = text.AsSpan(tail[i].End, tail[i + 1].Start - tail[i].End);
            var final = tail[i + 1].Start < finishedLinesEnd || gap.Count('\n') >= 2 || (tail[i].Kind == MdKind.Code && gap.Contains('\n'));
            if (!final) break;
            keep = i + 1;
        }
        for (var i = 0; i < keep; i++) _committed.Add(tail[i]);
        if (keep > 0) _committedPrefix = text[..tail[keep].Start];
        var all = new List<MdBlock>(_committed.Count + tail.Count - keep);
        all.AddRange(_committed);
        for (var i = keep; i < tail.Count; i++) all.Add(tail[i]);
        MarkdownBlocks.NumberLists(all);
        Blocks = all;
        return all;
    }

    public void Reset()
    {
        _committed.Clear();
        _committedPrefix = "";
        Blocks = Array.Empty<MdBlock>();
    }

    /// <summary>
    /// Inline text that is still being written, made safe to render: an
    /// unclosed `code`, **bold** or ~~strike~~ is closed (or dropped when
    /// nothing follows it yet) and a half-typed link shows its text, so
    /// markers never flash as raw characters.
    /// </summary>
    public static string Partial(string text)
    {
        if (string.IsNullOrEmpty(text)) return text ?? "";
        var opens = new List<(int At, string Marker)>();
        var i = 0;
        while (i < text.Length)
        {
            var c = text[i];
            if (c == '\\') { i += 2; continue; }
            if (c == '`')
            {
                var run = 1;
                while (i + run < text.Length && text[i + run] == '`') run++;
                var marker = new string('`', run);
                var close = text.IndexOf(marker, i + run, StringComparison.Ordinal);
                // A longer run is not a match for a shorter one.
                while (close >= 0 && close + run < text.Length && text[close + run] == '`')
                    close = text.IndexOf(marker, close + run + 1, StringComparison.Ordinal);
                if (close < 0) { opens.Add((i, marker)); break; }
                i = close + run;
                continue;
            }
            if ((c == '*' || c == '~' || c == '_') && i + 1 < text.Length && text[i + 1] == c)
            {
                var marker = new string(c, 2);
                var last = opens.FindLastIndex(o => o.Marker == marker);
                // __ only opens at the start of a word (snake__case stays text).
                if (c == '_' && last < 0 && i > 0 && char.IsLetterOrDigit(text[i - 1])) { i += 2; continue; }
                if (last >= 0) opens.RemoveRange(last, opens.Count - last);
                else opens.Add((i, marker));
                i += 2;
                continue;
            }
            i++;
        }
        var result = text;
        // Innermost first: the markers opened last close first.
        for (var k = opens.Count - 1; k >= 0; k--)
        {
            var (at, marker) = opens[k];
            if (result[(at + marker.Length)..].Trim().Length == 0) result = result[..at].TrimEnd();
            else result = result.TrimEnd() + marker;
        }
        // "[text](http://exa" → "text"; "![alt](pa" → "".
        result = PartialImage.Replace(result, "");
        result = PartialLink.Replace(result, "$1");
        return result;
    }

    private static readonly Regex PartialImage = new(@"!\[[^\]]*(\]\([^)]*)?$", RegexOptions.Compiled);
    private static readonly Regex PartialLink = new(@"\[([^\]]*)\]\([^)\s]*$", RegexOptions.Compiled);
}
