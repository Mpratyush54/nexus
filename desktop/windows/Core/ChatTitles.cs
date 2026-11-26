using System.Text.RegularExpressions;

namespace Nexus.Core;

// Human-readable chat titles and previews. Captured transcripts often start
// with harness envelopes (<environment_context>, <timestamp>, AGENTS.md
// instructions) and get a placeholder title like "Captured codex session";
// neither tells a person what the chat was about.
internal static class ChatTitles
{
    private static readonly Regex Envelope = new(
        @"<(environment_context|timestamp|user_info|system-reminder|system_reminder|INSTRUCTIONS|instructions|external_[\w-]*|context|additional_data|attached_files|cursor_commands|rules|user_rules|project_layout|git_status|current_file|open_and_recently_viewed_files|command-name|command-message|command-args|local-command-stdout|turn_aborted|subagent_notification|agent-message|task-notification)\b[^>]*>.*?</\1>",
        RegexOptions.Singleline | RegexOptions.IgnoreCase | RegexOptions.Compiled);
    private static readonly Regex UserQuery = new(@"<user_query>(.*?)</user_query>", RegexOptions.Singleline | RegexOptions.IgnoreCase | RegexOptions.Compiled);
    private static readonly Regex AnyTag = new(@"</?[A-Za-z_][\w\-]*(\s[^>]*)?>", RegexOptions.Compiled);
    private static readonly Regex Space = new(@"\s+", RegexOptions.Compiled);
    private static readonly Regex Placeholder = new(@"^(captured\s+\S+\s+(session|snapshot)|untitled chat|new chat|session)$", RegexOptions.IgnoreCase | RegexOptions.Compiled);

    /// Strips harness envelopes and markup, leaving what a person wrote.
    public static string Clean(string? raw)
    {
        var text = (raw ?? "").Trim();
        if (text.Length == 0) return "";
        var query = UserQuery.Match(text);
        if (query.Success) text = query.Groups[1].Value;
        // Codex wraps the request in file mentions, ambient context and a
        // "My request:" marker; keep only the request.
        text = HarnessMessage.RequestText(text);
        // Chats and memories a person mentioned with "@" go to the agent as context blocks; never a title.
        text = MentionText.Strip(text);
        // Tool blocks Codex imports from another agent, and the frame around a
        // sub-agent's hand-back, are not something a person wrote.
        text = ExternalToolMarkers.Strip(text);
        text = HarnessEnvelope.StripFrames(text);
        text = Envelope.Replace(text, " ");
        // A compaction summary ("This session is being continued…") is never
        // a title or preview: keep the request after it, if any.
        text = HarnessMessage.SkipCompaction(text);
        if (HarnessTurn.IsHarnessOnly(text)) return "";
        // An unterminated envelope (preview truncated mid-block) carries no prose.
        var open = Regex.Match(text, @"^\s*<(environment_context|timestamp|user_info|system-reminder|INSTRUCTIONS|agent-message|task-notification|external_[\w-]*)\b", RegexOptions.IgnoreCase);
        if (open.Success) return "";
        if (text.TrimStart().StartsWith("# AGENTS.md instructions", StringComparison.OrdinalIgnoreCase)) return "";
        text = AnyTag.Replace(text, " ");
        // Titles are plain text: drop markdown markers (`code`, ## headings, **bold**).
        text = text.Replace("`", "").Replace("**", "");
        text = System.Text.RegularExpressions.Regex.Replace(text, @"(^|\s)#{1,6}\s+", " ");
        return Space.Replace(text, " ").Trim();
    }

    // Blocks a harness injects into a turn; never something a person wrote.
    // A tag a person writes is quoted in code (`<system-reminder>` or a
    // ``` fence), so tags inside code are text, not envelopes.
    private const string HarnessTags = @"system-reminder|system_reminder|environment_context|timestamp|user_info|turn_aborted|subagent_notification|task-notification|agent-message|external_[\w-]*";
    private const RegexOptions Opts = RegexOptions.Singleline | RegexOptions.IgnoreCase | RegexOptions.Compiled;
    // Envelopes leading the message: may themselves contain code spans.
    private static readonly Regex LeadingBlock = new(@"^\s*<(" + HarnessTags + @")\b[^>]*>.*?</\1>\s*", Opts);
    private static readonly Regex HarnessBlock = new(@"<(" + HarnessTags + @")\b[^>]*>.*?</\1>\s*|<external_[\w-]*\b[^>]*/>\s*", Opts);
    // Fenced blocks (to the closing fence, or the end if unterminated) and inline spans.
    private static readonly Regex CodeSpan = new(@"```.*?(?:```|$)|`[^`\n]*`", RegexOptions.Singleline | RegexOptions.Compiled);
    // Orphans only cut text that holds no code: a prefix / suffix without backticks.
    private static readonly Regex OrphanClose = new(@"^[^`]*</(" + HarnessTags + @")>\s*", Opts);
    private static readonly Regex OrphanOpen = new(@"<(" + HarnessTags + @")\b[^>`]*>[^`]*$", Opts);

    /// Removes harness-injected blocks from a message body, keeping its
    /// formatting and never touching code. Previews cut mid-block leave a lone
    /// closing tag (drop all before it) or a lone opening tag (drop all after it).
    public static string StripEnvelopes(string? raw)
    {
        // Tool blocks Codex imported from another agent are work-log steps, not prose.
        var text = ExternalToolMarkers.Strip(raw);
        if (text.IndexOf('<') < 0) return text.Trim();
        // A sub-agent hand-back: its lead line and model-facing paragraph go too.
        text = HarnessEnvelope.StripFrames(text);
        for (Match m; (m = LeadingBlock.Match(text)).Success;) text = text[m.Length..];
        var sb = new System.Text.StringBuilder(text.Length);
        var at = 0;
        foreach (Match code in CodeSpan.Matches(text))
        {
            sb.Append(HarnessBlock.Replace(text[at..code.Index], "")).Append(code.Value);
            at = code.Index + code.Length;
        }
        sb.Append(HarnessBlock.Replace(text[at..], ""));
        text = OrphanClose.Replace(sb.ToString(), "");
        text = OrphanOpen.Replace(text, "");
        return text.Trim();
    }

    /// Keeps a borderless text box borderless in every state (WinUI paints a
    /// filled box on hover/focus even when the normal background is clear).
    public static void Bare(Microsoft.UI.Xaml.Controls.TextBox box)
    {
        var clear = new Microsoft.UI.Xaml.Media.SolidColorBrush(Microsoft.UI.Colors.Transparent);
        foreach (var key in new[] { "TextControlBackground", "TextControlBackgroundPointerOver", "TextControlBackgroundFocused",
                                    "TextControlBorderBrush", "TextControlBorderBrushPointerOver", "TextControlBorderBrushFocused" })
            box.Resources[key] = clear;
    }

    public static bool IsPlaceholder(string? title) => Placeholder.IsMatch((title ?? "").Trim());

    /// Picks a title: the stored one unless it is a placeholder, else the
    /// first sentence of the cleaned preview, else "<Agent> chat".
    public static string Title(string? title, string? preview, string? agent)
    {
        // A stored title that is only wrapper ("codex-clipboard-….png: C:/…")
        // names nothing; fall through to the preview.
        var stored = HarnessMessage.IsWrapperLine(title) ? "" : Clean(title);
        if (stored.Length > 0 && !IsPlaceholder(stored)) return Shorten(stored, 80);
        var fromPreview = Clean(preview);
        if (fromPreview.Length > 0) return Shorten(FirstSentence(fromPreview), 70);
        var name = AgentName(agent);
        return name.Length > 0 ? name + " chat" : "Untitled chat";
    }

    public static string AgentName(string? agent) => (agent ?? "").Trim().ToLowerInvariant() switch
    {
        "claude" or "claude-code" => "Claude Code",
        "codex" => "Codex",
        "cursor" or "agent" => "Cursor",
        "opencode" => "OpenCode",
        "gemini" => "Gemini",
        "kimi" => "Kimi",
        "copilot" => "Copilot",
        "antigravity" or "agy" => "Antigravity",
        "windsurf" => "Windsurf",
        "" => "",
        var other => char.ToUpperInvariant(other[0]) + other[1..]
    };

    private static string FirstSentence(string text)
    {
        var end = text.IndexOfAny(new[] { '\n', '.', '?', '!' });
        return end is > 12 and < 90 ? text[..(end + (text[end] == '\n' ? 0 : 1))].Trim() : text;
    }

    private static string Shorten(string text, int max) =>
        text.Length <= max ? text : text[..(max - 1)].TrimEnd() + "…";
}
