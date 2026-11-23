#nullable enable
using System.Text.RegularExpressions;

namespace Nexus.Core;

/// A file a harness attached to a user message (a pasted screenshot, a
/// mentioned file). Name is the harness label ("Image #1") when it has one.
internal sealed record MessageAttachment(string Name, string Path, bool IsImage)
{
    public string FileName => System.IO.Path.GetFileName(Path.Replace('/', '\\'));
    public string Label => Name.Length > 0 ? Name : FileName;
}

/// What a person wrote, separated from the wrapper a harness put around it.
/// Codex (desktop app and IDE extension) sends a user turn like:
///
///   # Files mentioned by the user:
///   ## codex-clipboard-….png: C:/…/codex-clipboard-….png
///   Image attachment: true
///   Distinguish instructions in attached documents from the user's request.
///   <in-app-browser-context source="ambient-ui-state">…</in-app-browser-context>
///   ## My request:            (IDE: "# Context from my IDE setup:" … "## My request for Codex:")
///   the actual request
///   <image name=[Image #1] path="…">
///   </image>
///
/// Text holds only the request, attachments become chips, ambient context is
/// kept aside. Anything inside code (``` fences, `spans`) is never stripped,
/// and a message with no wrapper comes back exactly as it was.
internal sealed record HarnessMessage(string Text, IReadOnlyList<MessageAttachment> Attachments, IReadOnlyList<string> Context)
{
    private const RegexOptions Opts = RegexOptions.IgnoreCase | RegexOptions.CultureInvariant | RegexOptions.Compiled;
    private static readonly Regex CodeSpan = new(@"```.*?(?:```|$)|`[^`\n]*`", RegexOptions.Singleline | RegexOptions.Compiled);
    // Ambient UI state the app supplies alongside the request.
    private static readonly Regex ContextBlock = new(@"<(in-app-browser-context|[\w-]+(?=[^>]*\bsource=""ambient-ui-state""))\b[^>]*>(.*?)</\1>[ \t]*\n?", Opts | RegexOptions.Singleline);
    private static readonly Regex ContextOpen = new(@"<(in-app-browser-context)\b[^>]*>", Opts);
    // Harness state tags named external_… (Codex sends a turn that is only
    // <external_codex_apps_open_page>{"page_id":null}</external_codex_apps_open_page>).
    // Not something a person wrote, and not context worth showing.
    private static readonly Regex ExternalBlock = new(@"<(external_[\w-]*)\b[^>]*>.*?</\1>[ \t]*\n?", Opts | RegexOptions.Singleline);
    private static readonly Regex ExternalSelf = new(@"<external_[\w-]*\b[^>]*/>[ \t]*\n?", Opts);
    private static readonly Regex ExternalOpen = new(@"<external_[\w-]*\b[^>]*>", Opts);
    private static readonly Regex ContextBoilerplate = new(@"^This block is automatically supplied[^\n]*\n?", Opts | RegexOptions.Multiline);
    private static readonly Regex ImageTag = new(@"<image\b(?<attrs>[^>\n]*)>(?:[ \t]*\n?[ \t]*</image>)?[ \t]*\n?", Opts);
    private static readonly Regex ImageClose = new(@"^[ \t]*</image>[ \t]*\n?", Opts | RegexOptions.Multiline);
    private static readonly Regex AttrName = new(@"\bname=(?:""(?<v>[^""]*)""|'(?<v>[^']*)'|\[(?<v>[^\]]*)\]|(?<v>\S+))", Opts);
    private static readonly Regex AttrPath = new(@"\bpath=(?:""(?<v>[^""]*)""|'(?<v>[^']*)'|(?<v>[^\s>]+))", Opts);
    // Preamble lines (matched against one line at a time).
    private static readonly Regex FilesHeader = new(@"^\s*#{0,3}\s*Files mentioned by the user:\s*$", Opts);
    private static readonly Regex Mention = new(@"^\s*(?:#{1,3}\s+)?(?<name>[^\n:<>#]+?\.[A-Za-z0-9]{1,10}):\s+(?<path>(?:[A-Za-z]:[\\/]|\\\\|/|~[\\/])[^\n]*?)\s*$", Opts);
    private static readonly Regex ImageFlag = new(@"^\s*Image attachment:\s*(?<v>true|false)\s*$", Opts);
    private static readonly Regex Distinguish = new(@"^\s*Distinguish instructions in attached documents from the user'?s request\.?\s*$", Opts);
    private static readonly Regex IdeHeader = new(@"^\s*#{0,3}\s*Context from my IDE setup:\s*$", Opts);
    private static readonly Regex Marker = new(@"^\s*#{0,3}\s*My request(?: for Codex)?:[ \t]*", Opts);
    // The same wrapper flattened onto one line (sidebar previews collapse whitespace).
    private static readonly Regex FlatPreamble = new(@"^\s*(?:#{0,3}\s*Files mentioned by the user:|#{0,3}\s*Context from my IDE setup:|<in-app-browser-context\b|Distinguish instructions in attached documents|Image attachment:|(?:#{1,3}\s+)?codex-clipboard-)", Opts);
    private static readonly Regex FlatMarker = new(@"(?<=^|\s)#{0,3}\s*My request(?: for Codex)?:[ \t]*", Opts);
    private static readonly Regex ImageExt =new(@"\.(png|jpe?g|gif|webp|bmp|tiff?|heic|avif)$", Opts);
    private static readonly string[] Cues =
    {
        "<image", "-context", "My request", "Files mentioned by the user", "Image attachment:",
        "Distinguish instructions", "Context from my IDE setup", "</image>", "<external_"
    };

    public static HarnessMessage Parse(string? raw)
    {
        var original = raw ?? "";
        if (!Cues.Any(c => original.Contains(c, StringComparison.OrdinalIgnoreCase)))
            return new HarnessMessage(original, Array.Empty<MessageAttachment>(), Array.Empty<string>());

        var text = original.Replace("\r\n", "\n");
        var attachments = new List<MessageAttachment>();
        var context = new List<string>();
        var changed = false;

        // Ambient context blocks, wherever they sit outside code.
        text = ReplaceOutsideCode(text, ContextBlock, m =>
        {
            AddContext(context, m.Groups[2].Value);
            return "";
        }, ref changed);
        // Harness state tags (external_…), closed or self-closing; a preview
        // cut inside one leaves only its opening tag: drop the rest.
        text = ReplaceOutsideCode(text, ExternalBlock, _ => "", ref changed);
        text = ReplaceOutsideCode(text, ExternalSelf, _ => "", ref changed);
        if (FirstOutsideCode(text, ExternalOpen) is { } cut)
        {
            text = text[..cut.Index];
            changed = true;
        }
        // A preview cut mid-block leaves an opening tag: the rest is context.
        var open = FirstOutsideCode(text, ContextOpen);
        if (open is not null)
        {
            AddContext(context, text[(open.Index + open.Length)..]);
            text = text[..open.Index];
            changed = true;
        }
        // <image name=… path=…></image> tags become attachments.
        text = ReplaceOutsideCode(text, ImageTag, m =>
        {
            var path = Attr(AttrPath, m.Groups["attrs"].Value);
            var name = Attr(AttrName, m.Groups["attrs"].Value);
            if (path.Length > 0 || name.Length > 0) AddAttachment(attachments, name, path, image: true);
            return "";
        }, ref changed);
        text = ReplaceOutsideCode(text, ImageClose, _ => "", ref changed);

        // Leading preamble: file mentions, the attachment disclaimer, IDE
        // context, then the "My request:" marker. Only recognised wrapper
        // lines are consumed; the first other line ends the preamble.
        var lines = text.Split('\n');
        var at = 0;
        var head = "";
        var preamble = false;
        while (at < lines.Length)
        {
            var line = lines[at];
            if (line.Trim().Length == 0) { at++; continue; }
            if (FilesHeader.IsMatch(line))
            {
                preamble = true;
                at++;
                while (at < lines.Length)
                {
                    if (lines[at].Trim().Length == 0) { at++; continue; }
                    if (Mention.Match(lines[at]) is { Success: true } mention)
                    {
                        AddMention(attachments, mention, lines, at);
                        at++;
                        continue;
                    }
                    if (ImageFlag.IsMatch(lines[at])) { at++; continue; }
                    break;
                }
                continue;
            }
            if (Mention.Match(line) is { Success: true } bare && NextNonBlank(lines, at + 1) is { } next && ImageFlag.IsMatch(lines[next]))
            {
                preamble = true;
                AddMention(attachments, bare, lines, at);
                at = next + 1;
                continue;
            }
            if (ImageFlag.IsMatch(line) || Distinguish.IsMatch(line)) { preamble = true; at++; continue; }
            if (IdeHeader.IsMatch(line))
            {
                preamble = true;
                var marker = MarkerLine(lines, at + 1);
                var end = marker ?? lines.Length;
                AddContext(context, string.Join("\n", lines[(at + 1)..end]));
                at = end;
                continue;
            }
            if (Marker.Match(line) is { Success: true } request)
            {
                preamble = true;
                head = line[request.Length..];
                at++;
            }
            break;
        }
        if (preamble)
        {
            var body = lines[at..].ToList();
            if (head.Trim().Length > 0) body.Insert(0, head);
            text = string.Join("\n", body);
            changed = true;
        }
        else if (FlatPreamble.IsMatch(text) && FirstOutsideCode(text, FlatMarker) is { } flat)
        {
            // A preview with whitespace collapsed onto one line: the wrapper
            // and the request share a line, so cut at the marker.
            text = text[(flat.Index + flat.Length)..];
            changed = true;
        }

        if (!changed) return new HarnessMessage(original, attachments, context);
        return new HarnessMessage(text.Trim(), attachments, context);
    }

    /// For titles and previews: the request text, or "" when what is left is
    /// still wrapper (a preview truncated before the request).
    public static string RequestText(string? raw)
    {
        var text = SkipCompaction(Parse(raw).Text);
        if (HarnessTurn.IsHarnessOnly(text)) return "";
        return FlatPreamble.IsMatch(text) ? "" : text;
    }

    // "This session is being continued from a p…": also a title cut short.
    private static readonly Regex CompactionLead = new(@"^\s*This session is being continued\b", Opts);
    // The closing lines Claude Code (and Codex imports of it) put after the
    // summary; what follows the last one is the person's next message.
    private static readonly Regex CompactionEnd = new(
        @"(?:Pick up the last task as if the break never happened\.|may already have happened in them\.|" +
        @"Continue with the last task that you were asked to work on\.|without asking the user any further questions\.)[^\n]*",
        Opts);

    public static bool IsCompactionSummary(string? text) => text is not null && CompactionLead.IsMatch(text);

    /// A compaction summary is never a title or preview: keep only the real
    /// request after it, or "" when the text holds nothing else.
    public static string SkipCompaction(string? text)
    {
        var value = text ?? "";
        if (!IsCompactionSummary(value)) return value;
        var ends = CompactionEnd.Matches(value);
        if (ends.Count == 0) return "";
        var last = ends[^1];
        var rest = value[(last.Index + last.Length)..].Trim();
        // The note after the summary about verbatim messages is harness text too.
        rest = Regex.Replace(rest, @"^The messages after this summary are the most recent messages[^\n]*?(?:in them\.|\n|$)", "", Opts).Trim();
        return IsCompactionSummary(rest) ? SkipCompaction(rest) : rest;
    }

    /// True for a single line that is only harness wrapper (a stored title
    /// such as "codex-clipboard-….png: C:/…" or "Files mentioned by the user:").
    public static bool IsWrapperLine(string? line)
    {
        var text = (line ?? "").Trim();
        if (text.Length == 0) return false;
        return FilesHeader.IsMatch(text) || Mention.IsMatch(text) || ImageFlag.IsMatch(text) ||
               Distinguish.IsMatch(text) || IdeHeader.IsMatch(text) || IsCompactionSummary(text) ||
               text.StartsWith("codex-clipboard-", StringComparison.OrdinalIgnoreCase) ||
               text.StartsWith("<in-app-browser-context", StringComparison.OrdinalIgnoreCase) ||
               text.StartsWith("<external_", StringComparison.OrdinalIgnoreCase) ||
               Regex.IsMatch(text, @"^#{0,3}\s*My request(?: for Codex)?:\s*$", RegexOptions.IgnoreCase);
    }

    private static int? NextNonBlank(string[] lines, int from)
    {
        for (var i = from; i < lines.Length; i++)
            if (lines[i].Trim().Length > 0) return i;
        return null;
    }

    // The request marker that ends an IDE context section, skipping fenced code.
    private static int? MarkerLine(string[] lines, int from)
    {
        var fenced = false;
        for (var i = from; i < lines.Length; i++)
        {
            if (lines[i].TrimStart().StartsWith("```", StringComparison.Ordinal)) { fenced = !fenced; continue; }
            if (!fenced && Marker.IsMatch(lines[i])) return i;
        }
        return null;
    }

    private static void AddMention(List<MessageAttachment> attachments, Match mention, string[] lines, int at)
    {
        var path = mention.Groups["path"].Value.Trim().Trim('"');
        var image = ImageExt.IsMatch(path) || ImageExt.IsMatch(mention.Groups["name"].Value);
        if (NextNonBlank(lines, at + 1) is { } next && ImageFlag.Match(lines[next]) is { Success: true } flag)
            image = flag.Groups["v"].Value.Equals("true", StringComparison.OrdinalIgnoreCase);
        AddAttachment(attachments, "", path, image);
    }

    // One chip per file: a mention and an <image> tag for the same path merge,
    // keeping the harness label ("Image #1").
    private static void AddAttachment(List<MessageAttachment> attachments, string name, string path, bool image)
    {
        name = name.Trim();
        path = path.Trim();
        var key = Normalize(path);
        var existing = key.Length > 0 ? attachments.FindIndex(a => Normalize(a.Path) == key) : -1;
        if (existing >= 0)
        {
            var prior = attachments[existing];
            attachments[existing] = prior with { Name = prior.Name.Length > 0 ? prior.Name : name, IsImage = prior.IsImage || image };
            return;
        }
        attachments.Add(new MessageAttachment(name, path, image));
    }

    private static string Normalize(string path) => path.Replace('/', '\\').TrimEnd('\\').ToLowerInvariant();

    private static void AddContext(List<string> context, string block)
    {
        var text = ContextBoilerplate.Replace(block, "").Trim();
        if (text.Length > 0) context.Add(text);
    }

    private static string Attr(Regex attr, string attrs) => attr.Match(attrs) is { Success: true } m ? m.Groups["v"].Value.Trim() : "";

    internal static List<(int Start, int End)> CodeRanges(string text) =>
        CodeSpan.Matches(text).Select(m => (m.Index, m.Index + m.Length)).ToList();

    internal static bool InCode(List<(int Start, int End)> code, int at) => code.Any(c => at >= c.Start && at < c.End);

    private static Match? FirstOutsideCode(string text, Regex regex)
    {
        var code = CodeRanges(text);
        foreach (Match m in regex.Matches(text))
            if (!InCode(code, m.Index)) return m;
        return null;
    }

    // Replaces matches that start outside code; a tag a person quotes in
    // code is text, not a wrapper.
    private static string ReplaceOutsideCode(string text, Regex regex, Func<Match, string> replace, ref bool changed)
    {
        var code = CodeRanges(text);
        var any = false;
        var result = regex.Replace(text, m =>
        {
            if (InCode(code, m.Index)) return m.Value;
            any = true;
            return replace(m);
        });
        changed |= any;
        return result;
    }
}

/// A user-role turn the harness wrote, not the person: Claude Code sends tool
/// results back as user messages, and a deferred-tool load (ToolSearch) adds
/// the text "Tool loaded." beside its tool_reference result. Captures keep
/// only the text, so the stored turn reads "Tool loaded."; a raw capture
/// keeps the content blocks. Neither is a chat bubble.
internal static class HarnessTurn
{
    private const RegexOptions Opts = RegexOptions.IgnoreCase | RegexOptions.CultureInvariant | RegexOptions.Compiled;
    // Whole lines the harness writes into a user turn on its own.
    private static readonly Regex NoteLine = new(@"^\s*Tools? loaded\.?\s*$", Opts);
    private static readonly Regex Caveat = new(@"^\s*Caveat: The messages below were generated by the user while running local commands\b", Opts);
    private static readonly HashSet<string> MachineBlocks = new(StringComparer.OrdinalIgnoreCase)
    {
        "tool_result", "tool_reference", "tool_use", "server_tool_use", "web_search_tool_result",
        "mcp_tool_result", "function_call", "function_call_output", "custom_tool_call_output"
    };

    /// True when nothing in the turn was typed by a person.
    public static bool IsHarnessOnly(string? raw)
    {
        var text = (raw ?? "").Trim();
        if (text.Length == 0) return false;
        if (text[0] is '{' or '[')
        {
            try
            {
                using var doc = System.Text.Json.JsonDocument.Parse(text);
                if (Blocks(doc.RootElement) is { } verdict) return verdict;
            }
            catch (System.Text.Json.JsonException) { }
        }
        return IsNoteText(text);
    }

    private static bool IsNoteText(string text)
    {
        if (Caveat.IsMatch(text)) return true;
        var lines = text.Replace("\r\n", "\n").Split('\n').Where(l => l.Trim().Length > 0).ToList();
        return lines.Count > 0 && lines.All(l => NoteLine.IsMatch(l));
    }

    // A content-block list (bare, or under content / message.content): true
    // when every block is a tool block or harness note, false when any is
    // something a person wrote, null when the JSON holds no blocks.
    private static bool? Blocks(System.Text.Json.JsonElement root)
    {
        var value = root;
        for (var depth = 0; depth < 3 && value.ValueKind == System.Text.Json.JsonValueKind.Object; depth++)
        {
            if (value.TryGetProperty("type", out var type) && type.ValueKind == System.Text.Json.JsonValueKind.String &&
                MachineBlocks.Contains(type.GetString() ?? "")) return true;
            if (value.TryGetProperty("content", out var content)) value = content;
            else if (value.TryGetProperty("message", out var message)) value = message;
            else return null;
        }
        if (value.ValueKind != System.Text.Json.JsonValueKind.Array) return null;
        var any = false;
        foreach (var block in value.EnumerateArray())
        {
            if (block.ValueKind != System.Text.Json.JsonValueKind.Object ||
                !block.TryGetProperty("type", out var kind) || kind.ValueKind != System.Text.Json.JsonValueKind.String) return null;
            var name = kind.GetString() ?? "";
            if (MachineBlocks.Contains(name)) { any = true; continue; }
            if (name.Equals("text", StringComparison.OrdinalIgnoreCase) &&
                block.TryGetProperty("text", out var t) && t.ValueKind == System.Text.Json.JsonValueKind.String)
            {
                var s = t.GetString() ?? "";
                if (s.Trim().Length == 0 || IsNoteText(s)) { any = true; continue; }
            }
            return false;
        }
        return any;
    }
}

internal enum EnvelopeKind { SubagentReport, AgentMessage, TaskNotification }

/// A message the harness put into the user's turn on an agent's behalf: a
/// sub-agent handing back its report (Claude Code wraps it as
/// "Another Claude session sent a message: <agent-message from=…>…</agent-message>"
/// plus a paragraph addressed to the model) or a background task's
/// <task-notification>. None of it was typed by the person.
internal sealed record HarnessEnvelope(EnvelopeKind Kind, string Title, string Body, string From, string Status)
{
    private const RegexOptions Opts = RegexOptions.Singleline | RegexOptions.IgnoreCase | RegexOptions.CultureInvariant | RegexOptions.Compiled;
    private static readonly Regex Block = new(@"<(?<tag>agent-message|task-notification)\b(?<attrs>[^>]*)>(?<body>.*?)(?:</\k<tag>>|\z)", Opts);
    private static readonly Regex Lead = new(@"(?:^|\n)[ \t]*Another Claude session sent a message:[ \t]*(?=\s*<agent-message\b)", Opts);
    private static readonly Regex Trailer = new(@"(?<=</agent-message>)\s*That [""“]?other Claude session[""”]?.*?(?=\n[ \t]*\n|\z)", Opts);
    private static readonly Regex HandBack = new(@"^\s*\[Subagent hand-back\].*?The report follows:[ \t]*\n?", Opts);
    private static readonly Regex FromAttr = new(@"\bfrom=(?:""(?<v>[^""]*)""|'(?<v>[^']*)'|(?<v>[^\s>]+))", Opts);

    public string Label => Kind switch
    {
        EnvelopeKind.SubagentReport => "Sub-agent report",
        EnvelopeKind.AgentMessage => "Agent message",
        _ => "Task notification"
    };

    public bool IsError => Status is "failed" or "error" or "killed";

    /// Same envelope, same fingerprint. Claude Code records a message that
    /// arrived mid-turn twice (queued inside a system reminder, then on its
    /// own), so a transcript repeats each hand-back and notification.
    public string Fingerprint => string.Join("\u001f", Kind, From, Status, Title,
        Regex.Replace(Body, @"\s+", " ").Trim());

    private static readonly Regex ToolsUsed = new(@"[ \t]*<tools-used-before-summary\b[^>]*>.*?</tools-used-before-summary>[ \t]*\n?", Opts);
    private static readonly Regex ResumeTrailer = new(@"\n[ \t]*agentId:\s*[0-9a-f]{8,}\b[^\n]*(?:\n[ \t]*<usage>.*?</usage>)?\s*\z", Opts);

    /// A sub-agent's result as its parent's tool step shows it. Claude Code
    /// returns the Agent/Task tool result as
    ///
    ///   &lt;tools-used-before-summary&gt;Bash&lt;/tools-used-before-summary&gt;
    ///   [Subagent hand-back] … The report follows:
    ///     the report, every line indented
    ///   agentId: … &lt;usage&gt;…&lt;/usage&gt;          (some versions)
    ///
    /// Only the report is for people: the framing goes, the indent comes out.
    /// Text without that framing comes back unchanged.
    public static string CleanToolResult(string? raw)
    {
        var original = raw ?? "";
        if (!original.Contains("<tools-used-before-summary", StringComparison.OrdinalIgnoreCase) &&
            !original.Contains("[Subagent hand-back]", StringComparison.Ordinal))
            return original;
        var text = ToolsUsed.Replace(original.Replace("\r\n", "\n"), "");
        if (HandBack.Match(text) is { Success: true } report)
        {
            text = text[report.Length..];
            text = ResumeTrailer.Replace(text, "");
            text = Dedent(text.Trim('\n'));
        }
        return StripAnnotations(text).Trim();
    }

    // "[harness: subagent output matched …]" and a bare "[Subagent hand-back] …"
    // line: notes the harness puts above a report, for the model.
    private static readonly Regex Annotation = new(@"\A\s*(?:\[harness:[^\]]*\][^\n]*|\[Subagent hand-back\][^\n]*)(?:\n|\z)", Opts);

    /// The report without the harness notes in front of it.
    public static string StripAnnotations(string? text)
    {
        var rest = (text ?? "").Replace("\r\n", "\n");
        for (Match m; (m = Annotation.Match(rest)).Success;) rest = rest[m.Length..];
        return rest.Length == (text ?? "").Replace("\r\n", "\n").Length ? text ?? "" : rest.TrimStart('\n', ' ', '\t').TrimEnd();
    }

    public static bool Contains(string? text) => text is not null &&
        (text.Contains("<agent-message", StringComparison.OrdinalIgnoreCase) ||
         text.Contains("<task-notification", StringComparison.OrdinalIgnoreCase));

    /// Removes the lead line and the model-facing paragraph Claude Code puts
    /// around an <agent-message> block (the block itself stays).
    public static string StripFrames(string? raw)
    {
        var text = raw ?? "";
        if (!text.Contains("<agent-message", StringComparison.OrdinalIgnoreCase)) return text;
        return Trailer.Replace(Lead.Replace(text.Replace("\r\n", "\n"), "\n"), "");
    }

    /// The envelopes in a message and what is left of it once they (and
    /// their frames) are gone. Blocks quoted in code stay text.
    public static (IReadOnlyList<HarnessEnvelope> Envelopes, string Remainder) Extract(string? raw)
    {
        var original = raw ?? "";
        var found = new List<HarnessEnvelope>();
        if (!Contains(original)) return (found, original);
        var text = StripFrames(original.Replace("\r\n", "\n"));
        var code = HarnessMessage.CodeRanges(text);
        var rest = Block.Replace(text, m =>
        {
            if (HarnessMessage.InCode(code, m.Index)) return m.Value;
            found.Add(Parse(m.Groups["tag"].Value, m.Groups["attrs"].Value, m.Groups["body"].Value));
            return "\n";
        });
        if (found.Count == 0) return (found, original);
        return (found, Regex.Replace(rest, @"\n{3,}", "\n\n").Trim());
    }

    private static HarnessEnvelope Parse(string tag, string attrs, string body)
    {
        if (tag.Equals("task-notification", StringComparison.OrdinalIgnoreCase))
        {
            string Field(string name) =>
                Regex.Match(body, "<" + name + @"\b[^>]*>(.*?)(?:</" + name + @">|\z)", Opts).Groups[1].Value.Trim();
            var summary = Field("summary");
            var status = Field("status").ToLowerInvariant();
            var result = Field("result");
            if (summary.Length == 0 && result.Length == 0) result = Regex.Replace(body, @"<[^>]+>", " ").Trim();
            var title = summary.Length > 0 ? summary : "Background task " + (status.Length > 0 ? status : "update");
            return new HarnessEnvelope(EnvelopeKind.TaskNotification, title, result, Field("task-id"), status);
        }
        var from = FromAttr.Match(attrs) is { Success: true } f ? f.Groups["v"].Value.Trim() : "";
        var text = body.Trim('\n', '\r');
        var report = HandBack.Match(text);
        if (report.Success) text = text[report.Length..];
        // A hand-back without "The report follows:" still starts with its tag.
        var handBack = report.Success || text.TrimStart().StartsWith("[Subagent hand-back]", StringComparison.Ordinal);
        text = StripAnnotations(Dedent(text).Trim()).Trim();
        return new HarnessEnvelope(handBack ? EnvelopeKind.SubagentReport : EnvelopeKind.AgentMessage, FirstLine(text), text, from, "");
    }

    // The harness indents every line of a hand-back report; take that back out.
    private static string Dedent(string text)
    {
        var lines = text.Split('\n');
        var indent = lines.Where(l => l.Trim().Length > 0).Select(l => l.Length - l.TrimStart(' ', '\t').Length).DefaultIfEmpty(0).Min();
        return indent == 0 ? text : string.Join("\n", lines.Select(l => l.Length >= indent ? l[indent..] : l.TrimStart()));
    }

    // The report's first heading, else its first sentence.
    private static string FirstLine(string text)
    {
        foreach (var raw in text.Split('\n'))
        {
            var heading = Regex.IsMatch(raw, @"^\s*#{1,6}\s");
            var line = Regex.Replace(raw.Replace("`", "").Replace("**", ""), @"^\s*(?:#{1,6}\s+|[-*]\s+)", "").Trim();
            if (line.Length == 0 || Annotation.IsMatch(line)) continue;
            if (!heading && Regex.Match(line, @"^.{12,}?[.!?](?=\s)") is { Success: true } sentence) line = sentence.Value;
            return line.Length <= 100 ? line : line[..99].TrimEnd() + "…";
        }
        return "";
    }
}

internal enum ExternalSegmentKind { Prose, Call, Result }

internal sealed record ExternalToolSegment(ExternalSegmentKind Kind, string Text, string Tool = "", string Input = "", bool IsError = false);

/// Codex imports another agent's history (e.g. a Claude Code chat) as plain
/// assistant text, one marker block per tool step:
///
///   [external_agent_tool_call: Bash]
///   description: List project files
///   command: ls -la
///   [/external_agent_tool_call]
///   [external_agent_tool_result: error]     (": error" only on failure)
///   Exit code 1
///   [/external_agent_tool_result]
///
/// Split turns that text back into prose, calls (with JSON arguments the
/// work log understands) and results. A block cut off by a preview limit
/// runs to the end of the text.
internal static class ExternalToolMarkers
{
    private static readonly Regex Block = new(
        @"^[ \t]*\[external_agent_tool_(?<kind>call|result)(?:[ \t]*:[ \t]*(?<label>[^\]\n]*))?\][ \t]*\n?" +
        @"(?<body>(?:(?!\[/?external_agent_tool_(?:call|result)\b).)*)" +
        @"(?:\[/external_agent_tool_\k<kind>\][ \t]*|\z|(?=\[/?external_agent_tool_))",
        RegexOptions.Singleline | RegexOptions.Multiline | RegexOptions.CultureInvariant | RegexOptions.Compiled);
    private static readonly Regex Cue = new(@"^[ \t]*\[/?external_agent_tool_(?:call|result)\b", RegexOptions.Multiline | RegexOptions.CultureInvariant | RegexOptions.Compiled);
    private static readonly Regex KeyLine = new(@"^(?<k>[A-Za-z_][\w-]{0,40}):(?:[ \t](?<v>.*))?$", RegexOptions.CultureInvariant | RegexOptions.Compiled);
    // After the first field, only these start a new one: a command or a file
    // body may itself hold "name: value" lines.
    private static readonly HashSet<string> Fields = new(StringComparer.OrdinalIgnoreCase)
    {
        "description", "command", "file", "file_path", "path", "input", "pattern", "query", "url", "prompt",
        "subagent_type", "content", "old_string", "new_string", "timeout", "glob", "output_mode", "skill",
        "message", "to", "task_id", "notebook_path", "run_in_background"
    };

    public static bool Contains(string? text) =>
        text is not null && text.Contains("external_agent_tool_", StringComparison.Ordinal) && Cue.IsMatch(text);

    public static List<ExternalToolSegment> Split(string? raw)
    {
        var text = (raw ?? "").Replace("\r\n", "\n");
        var segments = new List<ExternalToolSegment>();
        var at = 0;
        if (Contains(text))
            foreach (Match m in Block.Matches(text))
            {
                AddProse(segments, text[at..m.Index]);
                at = m.Index + m.Length;
                var body = m.Groups["body"].Value.Trim('\n', '\r');
                var label = m.Groups["label"].Value.Trim();
                if (m.Groups["kind"].Value == "call")
                    segments.Add(new ExternalToolSegment(ExternalSegmentKind.Call, body, label.Length > 0 ? label : "tool", ArgsJson(body)));
                else
                    segments.Add(new ExternalToolSegment(ExternalSegmentKind.Result, body.Trim(), IsError: label.Equals("error", StringComparison.OrdinalIgnoreCase)));
            }
        AddProse(segments, text[at..]);
        return segments;
    }

    /// The prose of a message without its tool blocks (titles, previews).
    public static string Strip(string? raw) =>
        Contains(raw) ? string.Join("\n\n", Split(raw).Where(s => s.Kind == ExternalSegmentKind.Prose).Select(s => s.Text)) : raw ?? "";

    private static void AddProse(List<ExternalToolSegment> segments, string text)
    {
        // A stray closing marker (its block began in an earlier turn) is not prose.
        text = Regex.Replace(text, @"^[ \t]*\[/external_agent_tool_(?:call|result)\][ \t]*$", "", RegexOptions.Multiline).Trim();
        if (text.Length > 0) segments.Add(new ExternalToolSegment(ExternalSegmentKind.Prose, text));
    }

    /// "key: value" lines → a JSON object; "file" becomes file_path and an
    /// "input" JSON object contributes its own fields.
    internal static string ArgsJson(string body)
    {
        var args = new System.Text.Json.Nodes.JsonObject();
        string? key = null;
        var value = new System.Text.StringBuilder();
        foreach (var line in body.Replace("\r\n", "\n").Split('\n'))
        {
            var m = KeyLine.Match(line);
            if (m.Success && (key is null || Fields.Contains(m.Groups["k"].Value)))
            {
                if (key is not null) Put(args, key, value.ToString().TrimEnd());
                key = m.Groups["k"].Value;
                value.Clear().Append(m.Groups["v"].Value);
                continue;
            }
            if (key is null)
            {
                key = "input";
                value.Append(line);
                continue;
            }
            value.Append('\n').Append(line);
        }
        if (key is not null) Put(args, key, value.ToString().TrimEnd());
        return args.ToJsonString();
    }

    private static void Put(System.Text.Json.Nodes.JsonObject args, string key, string value)
    {
        if (key.Equals("input", StringComparison.OrdinalIgnoreCase) && value.TrimStart().StartsWith('{'))
        {
            try
            {
                if (System.Text.Json.Nodes.JsonNode.Parse(value) is System.Text.Json.Nodes.JsonObject fields)
                {
                    foreach (var pair in fields.ToList())
                    {
                        fields.Remove(pair.Key);
                        args[pair.Key] = pair.Value;
                    }
                    return;
                }
            }
            catch (System.Text.Json.JsonException) { }
        }
        args[key.Equals("file", StringComparison.OrdinalIgnoreCase) ? "file_path" : key] = value;
    }
}
