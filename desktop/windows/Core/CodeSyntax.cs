#nullable enable
namespace Nexus.Core;

internal enum CodeToken { Plain, Keyword, String, Comment, Number, Type, Added, Removed, Meta, Property }

internal readonly record struct CodeSpan(int Start, int Length, CodeToken Kind);

/// <summary>
/// A small, fast highlighter for code blocks in chat: keywords, strings,
/// comments and numbers for the languages agents write most, and +/- lines
/// for diffs. One pass, no grammar files. Unknown languages stay plain.
/// </summary>
internal static class CodeSyntax
{
    /// Code past this size is shown plain: colour is not worth the time.
    public const int MaxHighlightChars = 120_000;

    private enum Family { None, CLike, Python, Shell, Json, Diff, Sql, Yaml, Markup, Css }

    private sealed record Language(Family Family, HashSet<string> Keywords, string LineComment, bool HashComment, bool BlockComment, bool Backtick);

    private static HashSet<string> Words(string list) => new(list.Split(' ', StringSplitOptions.RemoveEmptyEntries), StringComparer.Ordinal);

    private static readonly HashSet<string> CLikeWords = Words(
        "abstract as async await base bool boolean break byte case catch char class const continue debugger def default delegate delete do double " +
        "else enum export extends extern false final finally float fn for foreach from func function go goto if impl implements import in " +
        "instanceof int interface internal is let long match mod module mut namespace new nil null object of operator out override package " +
        "private protected pub public readonly record ref return sealed self short static string struct super switch this throw throws trait " +
        "true try type typeof uint ulong unsafe use using val var virtual void volatile when where while yield chan defer select range map " +
        "crate dyn loop move where Self undefined declare keyof satisfies get set init required record partial event");

    private static readonly HashSet<string> PythonWords = Words(
        "and as assert async await break class continue def del elif else except False finally for from global if import in is lambda " +
        "None nonlocal not or pass raise return True try while with yield self print match case");

    private static readonly HashSet<string> ShellWords = Words(
        "if then else elif fi case esac for while until do done in function return exit export local readonly set unset echo cd source alias " +
        "sudo git npm npx pnpm yarn go dotnet cargo make docker kubectl pip python node curl grep sed awk cat ls rm cp mv mkdir " +
        "Get-ChildItem Set-Location Write-Host param foreach");

    private static readonly HashSet<string> SqlWords = new(Words(
        "select from where and or not insert into values update set delete create table index view drop alter add column primary key foreign " +
        "references join left right inner outer full on group by order having limit offset as distinct union all null is in exists case when " +
        "then else end returning with begin commit rollback transaction default unique check constraint int integer text varchar boolean " +
        "timestamp timestamptz uuid jsonb bigint serial if"), StringComparer.OrdinalIgnoreCase);

    private static readonly HashSet<string> None = new();

    private static Language? For(string language) => (language ?? "").Trim().ToLowerInvariant() switch
    {
        "c" or "h" or "cpp" or "c++" or "cc" or "hpp" or "cs" or "csharp" or "c#" or "java" or "kotlin" or "kt" or "swift" or "go" or "golang" or
        "rust" or "rs" or "scala" or "dart" or "php" or "groovy" or "zig"
            => new Language(Family.CLike, CLikeWords, "//", language is "php", true, language is "go" or "golang"),
        "js" or "javascript" or "jsx" or "ts" or "typescript" or "tsx" or "mjs" or "cjs" or "node"
            => new Language(Family.CLike, CLikeWords, "//", false, true, true),
        "py" or "python" or "python3" or "rb" or "ruby" or "r" or "perl" or "pl"
            => new Language(Family.Python, PythonWords, "", true, false, false),
        "sh" or "bash" or "shell" or "zsh" or "console" or "terminal" or "ps1" or "powershell" or "pwsh" or "bat" or "cmd" or "fish" or "dockerfile" or "makefile" or "make"
            => new Language(Family.Shell, ShellWords, "", true, false, false),
        "json" or "jsonc" or "json5" => new Language(Family.Json, Words("true false null"), "//", false, true, false),
        "diff" or "patch" or "udiff" => new Language(Family.Diff, None, "", false, false, false),
        "sql" or "psql" or "postgres" or "postgresql" or "mysql" or "sqlite" => new Language(Family.Sql, SqlWords, "--", false, true, false),
        "yaml" or "yml" or "toml" or "ini" or "conf" or "env" or "properties" => new Language(Family.Yaml, Words("true false null yes no on off"), "", true, false, false),
        "html" or "xml" or "xaml" or "svg" or "vue" or "svelte" or "csproj" or "plist" => new Language(Family.Markup, None, "", false, false, false),
        "css" or "scss" or "less" => new Language(Family.Css, None, "//", false, true, false),
        _ => null,
    };

    public static bool Supports(string language) => For(language) is not null;

    /// Coloured spans (plain text between them is left out), in order.
    public static List<CodeSpan> Tokenize(string code, string language)
    {
        var spans = new List<CodeSpan>();
        if (string.IsNullOrEmpty(code) || code.Length > MaxHighlightChars) return spans;
        var lang = For(language);
        if (lang is null) return spans;
        switch (lang.Family)
        {
            case Family.Diff: Diff(code, spans); return spans;
            case Family.Markup: Markup(code, spans); return spans;
        }
        var i = 0;
        var n = code.Length;
        var lineStart = true;
        while (i < n)
        {
            var c = code[i];
            if (c == '\n') { lineStart = true; i++; continue; }
            if (char.IsWhiteSpace(c)) { i++; continue; }
            // Comments.
            if (lang.LineComment.Length > 0 && string.CompareOrdinal(code, i, lang.LineComment, 0, lang.LineComment.Length) == 0)
            {
                var end = LineEnd(code, i);
                spans.Add(new CodeSpan(i, end - i, CodeToken.Comment));
                i = end;
                continue;
            }
            if (lang.HashComment && c == '#' && (lang.Family != Family.Shell || i == 0 || char.IsWhiteSpace(code[i - 1])))
            {
                // "#!" shebang and comments alike.
                var end = LineEnd(code, i);
                spans.Add(new CodeSpan(i, end - i, CodeToken.Comment));
                i = end;
                continue;
            }
            if (lang.BlockComment && c == '/' && i + 1 < n && code[i + 1] == '*')
            {
                var close = code.IndexOf("*/", i + 2, StringComparison.Ordinal);
                var end = close < 0 ? n : close + 2;
                spans.Add(new CodeSpan(i, end - i, CodeToken.Comment));
                i = end;
                continue;
            }
            // Strings.
            if (lang.Family == Family.Python && (Starts(code, i, "\"\"\"") || Starts(code, i, "'''")))
            {
                var quote = code.Substring(i, 3);
                var close = code.IndexOf(quote, i + 3, StringComparison.Ordinal);
                var end = close < 0 ? n : close + 3;
                spans.Add(new CodeSpan(i, end - i, CodeToken.String));
                i = end;
                continue;
            }
            if (c == '"' || c == '\'' || (c == '`' && lang.Backtick))
            {
                // A lone apostrophe in prose-like shell text ("don't") is not a string.
                if (c == '\'' && lang.Family is Family.Shell or Family.Yaml && i > 0 && char.IsLetter(code[i - 1])) { i++; continue; }
                var end = StringEnd(code, i, c, multiline: c == '`');
                var kind = lang.Family is Family.Json or Family.Yaml && IsKey(code, end) ? CodeToken.Property : CodeToken.String;
                spans.Add(new CodeSpan(i, end - i, kind));
                i = end;
                continue;
            }
            // Numbers.
            if (char.IsDigit(c) && (i == 0 || !IsWord(code[i - 1])))
            {
                var end = i + 1;
                while (end < n && (char.IsLetterOrDigit(code[end]) || code[end] == '.' || code[end] == '_') && !(code[end] == '.' && end + 1 < n && code[end + 1] == '.')) end++;
                spans.Add(new CodeSpan(i, end - i, CodeToken.Number));
                i = end;
                continue;
            }
            // Words: keywords, types (Capitalised in C-like code), YAML keys.
            if (IsWordStart(c) || (lang.Family == Family.Shell && c == '-' && i + 1 < n && char.IsLetter(code[i + 1]) && (i == 0 || char.IsWhiteSpace(code[i - 1]))))
            {
                var end = i + 1;
                while (end < n && (IsWord(code[end]) || (lang.Family == Family.Shell && code[end] == '-'))) end++;
                var word = code[i..end];
                if (lang.Family == Family.Yaml && lineStart && end < n && code[end] == ':')
                    spans.Add(new CodeSpan(i, end - i, CodeToken.Property));
                else if (lang.Family == Family.Shell && word[0] == '-')
                    spans.Add(new CodeSpan(i, end - i, CodeToken.Meta));
                else if (lang.Keywords.Contains(word) && (lang.Family != Family.Shell || lineStart || PrecededByCommandBreak(code, i)))
                    spans.Add(new CodeSpan(i, end - i, CodeToken.Keyword));
                else if (lang.Family == Family.CLike && char.IsUpper(word[0]) && word.Length > 1 && word.Any(char.IsLower))
                    spans.Add(new CodeSpan(i, end - i, CodeToken.Type));
                else if (lang.Family == Family.Css && end < n && code[end] == ':')
                    spans.Add(new CodeSpan(i, end - i, CodeToken.Property));
                lineStart = false;
                i = end;
                continue;
            }
            if (lang.Family == Family.CLike && c == '@' && i + 1 < n && IsWordStart(code[i + 1]))
            {
                var end = i + 1;
                while (end < n && IsWord(code[end])) end++;
                spans.Add(new CodeSpan(i, end - i, CodeToken.Meta));
                i = end;
                continue;
            }
            if (lang.Family == Family.Shell && c == '$' && i + 1 < n && (IsWordStart(code[i + 1]) || code[i + 1] == '{'))
            {
                var end = i + 1;
                if (code[end] == '{') { var close = code.IndexOf('}', end); end = close < 0 ? n : close + 1; }
                else while (end < n && IsWord(code[end])) end++;
                spans.Add(new CodeSpan(i, end - i, CodeToken.Type));
                i = end;
                continue;
            }
            lineStart = lineStart && (c == '-' || c == ' ');
            i++;
        }
        return spans;
    }

    private static bool PrecededByCommandBreak(string code, int at)
    {
        for (var k = at - 1; k >= 0; k--)
        {
            var c = code[k];
            if (c == ' ' || c == '\t') continue;
            return c is '|' or ';' or '&' or '(' or '\n' or '{' or '`' or '$';
        }
        return true;
    }

    private static void Diff(string code, List<CodeSpan> spans)
    {
        var at = 0;
        while (at < code.Length)
        {
            var end = LineEnd(code, at);
            if (end > at)
            {
                var kind = code[at] switch
                {
                    '+' when !Starts(code, at, "+++") => CodeToken.Added,
                    '-' when !Starts(code, at, "---") => CodeToken.Removed,
                    '@' when Starts(code, at, "@@") => CodeToken.Meta,
                    _ when Starts(code, at, "diff ") || Starts(code, at, "index ") || Starts(code, at, "+++") || Starts(code, at, "---") => CodeToken.Comment,
                    _ => CodeToken.Plain,
                };
                if (kind != CodeToken.Plain) spans.Add(new CodeSpan(at, end - at, kind));
            }
            at = end + 1;
        }
    }

    private static void Markup(string code, List<CodeSpan> spans)
    {
        var i = 0;
        while (i < code.Length)
        {
            if (Starts(code, i, "<!--"))
            {
                var close = code.IndexOf("-->", i + 4, StringComparison.Ordinal);
                var end = close < 0 ? code.Length : close + 3;
                spans.Add(new CodeSpan(i, end - i, CodeToken.Comment));
                i = end;
                continue;
            }
            if (code[i] == '<' && i + 1 < code.Length && (char.IsLetter(code[i + 1]) || code[i + 1] == '/' || code[i + 1] == '!' || code[i + 1] == '?'))
            {
                var nameStart = i + 1;
                while (nameStart < code.Length && (code[nameStart] == '/' || code[nameStart] == '!' || code[nameStart] == '?')) nameStart++;
                var nameEnd = nameStart;
                while (nameEnd < code.Length && (IsWord(code[nameEnd]) || code[nameEnd] is ':' or '-' or '.')) nameEnd++;
                spans.Add(new CodeSpan(nameStart, nameEnd - nameStart, CodeToken.Keyword));
                var tagEnd = code.IndexOf('>', nameEnd);
                if (tagEnd < 0) tagEnd = code.Length;
                var j = nameEnd;
                while (j < tagEnd)
                {
                    if (code[j] == '"' || code[j] == '\'')
                    {
                        var end = Math.Min(StringEnd(code, j, code[j], multiline: true), tagEnd);
                        spans.Add(new CodeSpan(j, end - j, CodeToken.String));
                        j = end;
                        continue;
                    }
                    if (IsWordStart(code[j]))
                    {
                        var end = j;
                        while (end < tagEnd && (IsWord(code[end]) || code[end] is ':' or '-' or '.')) end++;
                        spans.Add(new CodeSpan(j, end - j, CodeToken.Property));
                        j = end;
                        continue;
                    }
                    j++;
                }
                i = tagEnd;
                continue;
            }
            i++;
        }
    }

    private static bool IsKey(string code, int end)
    {
        while (end < code.Length && (code[end] == ' ' || code[end] == '\t')) end++;
        return end < code.Length && code[end] == ':';
    }

    private static int StringEnd(string code, int start, char quote, bool multiline)
    {
        var i = start + 1;
        while (i < code.Length)
        {
            var c = code[i];
            if (c == '\\') { i += 2; continue; }
            if (c == quote) return i + 1;
            if (c == '\n' && !multiline) return i;
            i++;
        }
        return code.Length;
    }

    private static int LineEnd(string code, int at)
    {
        var nl = code.IndexOf('\n', at);
        return nl < 0 ? code.Length : nl;
    }

    private static bool Starts(string code, int at, string token) =>
        at + token.Length <= code.Length && string.CompareOrdinal(code, at, token, 0, token.Length) == 0;

    private static bool IsWordStart(char c) => char.IsLetter(c) || c == '_';
    private static bool IsWord(char c) => char.IsLetterOrDigit(c) || c == '_';
}
