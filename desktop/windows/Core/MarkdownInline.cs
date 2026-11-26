#nullable enable
using System.Text.RegularExpressions;

namespace Nexus.Core;

internal enum MdSpan { Text, Bold, Italic, Strike, Code, Link, Image, LineBreak }

/// One piece of inline Markdown. Bold, italic, strike and link have children.
internal sealed record MdInline(MdSpan Kind, string Text, string Url = "", IReadOnlyList<MdInline>? Children = null)
{
    public IReadOnlyList<MdInline> Items => Children ?? Array.Empty<MdInline>();
}

/// <summary>
/// Inline Markdown: **bold**, __bold__, *italic*, _italic_, ~~strike~~,
/// `code`, [links](url), ![images](src), &lt;autolinks&gt;, bare URLs and
/// backslash escapes. snake_case and 2*3*4 stay plain text.
/// </summary>
internal static partial class MarkdownInline
{
    public static List<MdInline> Parse(string? text)
    {
        var result = new List<MdInline>();
        if (string.IsNullOrEmpty(text)) return result;
        var lines = text.Split('\n');
        for (var l = 0; l < lines.Length; l++)
        {
            if (l > 0) result.Add(new MdInline(MdSpan.LineBreak, "\n"));
            ParseLine(lines[l], result, 0);
        }
        return Merge(result);
    }

    private static void ParseLine(string value, List<MdInline> into, int depth)
    {
        var start = 0;
        foreach (Match m in Token().Matches(value))
        {
            if (m.Index < start) continue;
            if (m.Index > start) into.Add(new MdInline(MdSpan.Text, value[start..m.Index]));
            into.Add(Node(m, depth));
            start = m.Index + m.Length;
        }
        if (start < value.Length) into.Add(new MdInline(MdSpan.Text, value[start..]));
    }

    private static MdInline Node(Match m, int depth)
    {
        List<MdInline> Inner(string s)
        {
            var list = new List<MdInline>();
            if (depth > 6) list.Add(new MdInline(MdSpan.Text, s));
            else ParseLine(s, list, depth + 1);
            return Merge(list);
        }
        if (m.Groups["code"].Success)
        {
            var code = m.Groups["codetext"].Value;
            // `` `x` `` strips one space each side (CommonMark).
            if (code.Length >= 2 && code[0] == ' ' && code[^1] == ' ' && code.Trim().Length > 0) code = code[1..^1];
            return new MdInline(MdSpan.Code, code);
        }
        if (m.Groups["esc"].Success) return new MdInline(MdSpan.Text, m.Groups["esc"].Value);
        if (m.Groups["alt"].Success) return new MdInline(MdSpan.Image, m.Groups["alt"].Value, Unbracket(m.Groups["src"].Value));
        if (m.Groups["ltext"].Success) return new MdInline(MdSpan.Link, m.Groups["ltext"].Value, Unbracket(m.Groups["href"].Value), Inner(m.Groups["ltext"].Value));
        if (m.Groups["b"].Success) return new MdInline(MdSpan.Bold, m.Groups["b"].Value, "", Inner(m.Groups["b"].Value));
        if (m.Groups["b2"].Success) return new MdInline(MdSpan.Bold, m.Groups["b2"].Value, "", Inner(m.Groups["b2"].Value));
        if (m.Groups["s"].Success) return new MdInline(MdSpan.Strike, m.Groups["s"].Value, "", Inner(m.Groups["s"].Value));
        if (m.Groups["i"].Success) return new MdInline(MdSpan.Italic, m.Groups["i"].Value, "", Inner(m.Groups["i"].Value));
        if (m.Groups["i2"].Success) return new MdInline(MdSpan.Italic, m.Groups["i2"].Value, "", Inner(m.Groups["i2"].Value));
        if (m.Groups["auto"].Success) return new MdInline(MdSpan.Link, m.Groups["auto"].Value, m.Groups["auto"].Value);
        var url = m.Groups["url"].Value;
        return new MdInline(MdSpan.Link, url, url);
    }

    private static string Unbracket(string url) => url.Length > 1 && url[0] == '<' && url[^1] == '>' ? url[1..^1] : url;

    // Adjacent text pieces become one run.
    private static List<MdInline> Merge(List<MdInline> list)
    {
        var merged = new List<MdInline>(list.Count);
        foreach (var item in list)
        {
            if (item.Kind == MdSpan.Text && merged.Count > 0 && merged[^1].Kind == MdSpan.Text)
                merged[^1] = new MdInline(MdSpan.Text, merged[^1].Text + item.Text);
            else if (item.Kind != MdSpan.Text || item.Text.Length > 0)
                merged.Add(item);
        }
        return merged;
    }

    /// The plain text a reader sees (markers removed, link text kept).
    public static string Plain(IEnumerable<MdInline> items) => string.Concat(items.Select(i => i.Kind switch
    {
        MdSpan.Text or MdSpan.Code or MdSpan.LineBreak => i.Text,
        MdSpan.Image => i.Text,
        _ => i.Children is { } c ? Plain(c) : i.Text,
    }));

    /// http(s) and mailto links open in the browser; anything else is not a web link.
    public static bool IsWebUrl(string url) =>
        Uri.TryCreate(url, UriKind.Absolute, out var uri) && (uri.Scheme == Uri.UriSchemeHttp || uri.Scheme == Uri.UriSchemeHttps || uri.Scheme == Uri.UriSchemeMailto);

    [GeneratedRegex(@"(?<code>`+)(?<codetext>(?:(?!\k<code>).)+?)\k<code>(?!`)" +
                    @"|\\(?<esc>[\\`*_{}\[\]()#+\-.!~|<>])" +
                    @"|!\[(?<alt>[^\]\n]*)\]\((?<src><[^>]+>|[^)\s]+)(?:\s+""[^""]*"")?\)" +
                    @"|\[(?<ltext>[^\]\n]+)\]\((?<href><[^>]+>|[^)\s]+)(?:\s+""[^""]*"")?\)" +
                    @"|\*\*(?<b>(?=\S)(?:[^*]|\*(?!\*))+?(?<=\S))\*\*" +
                    @"|(?<![A-Za-z0-9_])__(?<b2>(?=\S).+?(?<=\S))__(?![A-Za-z0-9_])" +
                    @"|~~(?<s>(?=\S).+?(?<=\S))~~" +
                    @"|(?<![*\w\\])\*(?<i>(?=[^\s*])(?:[^*]|\*\*[^*]+\*\*)+?(?<=[^\s*]))\*(?![*\w])" +
                    @"|(?<![A-Za-z0-9_\\])_(?<i2>(?=[^\s_])[^_]+?(?<=[^\s_]))_(?![A-Za-z0-9_])" +
                    @"|<(?<auto>(?:https?|mailto):[^>\s]+)>" +
                    @"|(?<url>\bhttps?://[^\s<>()\[\]`""']+(?:\([^\s<>()]*\))?[^\s<>()\[\]`""'.,;:!?*_~])")]
    private static partial Regex Token();
}
