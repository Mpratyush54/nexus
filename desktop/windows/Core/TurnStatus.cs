#nullable enable
using System.Text.Json;

namespace Nexus.Core;

/// <summary>
/// The one status line inside a reply while the agent works:
/// "Running npm test · 12s · Esc to stop". It follows the turn's live events
/// (thinking_delta, tool_start, text_delta, approval, status) and replaces
/// the stream of sentences that used to churn under the composer.
/// </summary>
internal sealed class TurnStatus
{
    public enum Phase { Starting, Queued, Thinking, Tool, Writing, Approval, Subagent, Stopping }

    private readonly Func<DateTimeOffset> _now;

    public TurnStatus(string agentName, DateTimeOffset? startedAt = null, Func<DateTimeOffset>? now = null)
    {
        _now = now ?? (() => DateTimeOffset.Now);
        AgentName = agentName.Length > 0 ? agentName : "The agent";
        StartedAt = startedAt ?? _now();
    }

    public string AgentName { get; }
    public DateTimeOffset StartedAt { get; private set; }
    public Phase Current { get; private set; } = Phase.Starting;
    /// <summary>The tool step in words when <see cref="Current"/> is Tool ("Running npm test").</summary>
    public string Detail { get; private set; } = "";
    /// <summary>A notice from the agent worth reading ("Model x isn't available; using its default").</summary>
    public string Notice { get; private set; } = "";
    public bool HasText { get; private set; }
    /// <summary>
    /// A live "Thinking… Ns" row is on screen. While it ticks, it is the
    /// status: the line drops its own "Thinking" verb and time and keeps
    /// only "Esc to stop", so the reply never says Thinking twice.
    /// </summary>
    public bool ThinkingRowShown { get; set; }

    /// <summary>Sets the start time once it is known (a reopened chat's turn started earlier).</summary>
    public void StartedSince(DateTimeOffset startedAt)
    {
        if (startedAt <= _now()) StartedAt = startedAt;
    }

    public void Queued() => Current = Phase.Queued;
    public void Stopping() => Current = Phase.Stopping;
    public void Approval() { if (Current != Phase.Stopping) Current = Phase.Approval; }

    /// <summary>Applies one live event (kind, text and the tool's name and JSON input). Returns true when the line changed.</summary>
    public bool Apply(string kind, string text = "", string tool = "", string toolInput = "")
    {
        var before = (Current, Detail, Notice, HasText);
        if (Current == Phase.Stopping && kind is not ("status" or "error")) return false;
        switch (kind)
        {
            case "thinking_delta":
                if (!HasText || Current != Phase.Writing) Current = Phase.Thinking;
                break;
            case "text_delta":
            case "text":
                HasText = true;
                Current = Phase.Writing;
                break;
            case "tool_start":
                Current = Phase.Tool;
                Detail = DescribeTool(tool, toolInput);
                break;
            case "tool_end":
                if (Current == Phase.Tool) Current = Phase.Thinking;
                break;
            case "subagent_text":
                Current = Phase.Subagent;
                break;
            case "approval":
                Current = Phase.Approval;
                break;
            case "approval_resolved":
                if (Current == Phase.Approval) Current = Phase.Thinking;
                break;
            case "status":
                if (IsReadyNotice(text)) { if (Current is Phase.Starting or Phase.Queued) Current = Phase.Thinking; }
                else if (text.Trim().Length > 0) Notice = text.Trim();
                break;
        }
        return before != (Current, Detail, Notice, HasText);
    }

    /// <summary>Agent "I'm up" notices carry nothing to read: they just end Starting.</summary>
    public static bool IsReadyNotice(string text)
    {
        var t = text.Trim().TrimEnd('.', '…');
        return t.Length == 0 || t.Equals("Connected", StringComparison.OrdinalIgnoreCase) ||
               t.EndsWith(" is ready", StringComparison.OrdinalIgnoreCase) || t.Equals("ready", StringComparison.OrdinalIgnoreCase);
    }

    /// <summary>The verb part: "Starting Claude Code", "Thinking", "Running npm test", "Writing".</summary>
    public string Verb => Current switch
    {
        Phase.Starting => "Starting " + AgentName,
        Phase.Queued => "Waiting for a free slot",
        Phase.Thinking => "Thinking",
        Phase.Tool => Detail.Length > 0 ? Detail : "Working",
        Phase.Writing => "Writing",
        Phase.Approval => "Waiting for your approval",
        Phase.Subagent => "Sub-agent working",
        Phase.Stopping => "Stopping",
        _ => "Working"
    };

    /// <summary>False while the thinking row speaks for the line (see <see cref="ThinkingRowShown"/>).</summary>
    public bool ShowsVerb => !(ThinkingRowShown && Current == Phase.Thinking);

    public TimeSpan Elapsed => _now() - StartedAt;

    /// <summary>"4s", "1m 05s", "1h 02m".</summary>
    public static string FormatElapsed(TimeSpan span)
    {
        if (span < TimeSpan.Zero) span = TimeSpan.Zero;
        if (span.TotalHours >= 1) return $"{(int)span.TotalHours}h {span.Minutes:00}m";
        if (span.TotalMinutes >= 1) return $"{(int)span.TotalMinutes}m {span.Seconds:00}s";
        return $"{(int)span.TotalSeconds}s";
    }

    /// <summary>Whether "Esc to stop" is worth saying now.</summary>
    public bool ShowsStopHint => Current is not (Phase.Stopping or Phase.Approval);

    /// <summary>The whole line, for screen readers and tests: "Thinking · 12s · Esc to stop".</summary>
    public string Line() => !ShowsVerb
        ? (ShowsStopHint ? "Esc to stop" : "")
        : Verb + "… · " + FormatElapsed(Elapsed) + (ShowsStopHint ? " · Esc to stop" : "");

    /// <summary>"Running npm test", "Editing Program.cs", "Reading README.md", "Searching for TODO".</summary>
    public static string DescribeTool(string tool, string inputJson)
    {
        string Arg(JsonElement e, params string[] keys)
        {
            if (e.ValueKind != JsonValueKind.Object) return "";
            foreach (var k in keys)
                if (e.TryGetProperty(k, out var v) && v.ValueKind == JsonValueKind.String && v.GetString() is { Length: > 0 } s) return s;
            return "";
        }
        var input = default(JsonElement);
        JsonDocument? doc = null;
        try
        {
            if (inputJson.Trim().StartsWith('{')) { doc = JsonDocument.Parse(inputJson); input = doc.RootElement; }
        }
        catch (JsonException) { }
        try
        {
            static string Clip(string s, int max)
            {
                s = s.Replace("\r", " ").Replace('\n', ' ').Trim();
                return s.Length > max ? s[..(max - 1)] + "…" : s;
            }
            static string FileName(string path) => Path.GetFileName(path.Replace('\\', '/').TrimEnd('/')) is { Length: > 0 } leaf ? leaf : path;
            var command = Arg(input, "command", "cmd");
            if (command.Length > 0) return "Running " + Clip(command, 48);
            var path = Arg(input, "file_path", "path", "filePath", "notebook_path");
            var name = tool.ToLowerInvariant();
            if (path.Length > 0)
            {
                var file = Clip(FileName(path), 48);
                if (name.Contains("edit") || name.Contains("write") || name.Contains("patch") || name.Contains("replace")) return "Editing " + file;
                if (name.Contains("read") || name.Contains("view") || name.Contains("open")) return "Reading " + file;
                return Clip(tool, 24) + " · " + file;
            }
            var pattern = Arg(input, "pattern", "query", "regex");
            if (pattern.Length > 0 && (name.Contains("grep") || name.Contains("search") || name.Contains("glob") || name.Contains("find")))
                return "Searching for " + Clip(pattern, 40);
            var url = Arg(input, "url");
            if (url.Length > 0) return "Fetching " + Clip(url, 48);
            if (tool is "Task" or "Agent") return "Running a sub-agent";
            if (tool.StartsWith("mcp__", StringComparison.Ordinal))
            {
                var parts = tool[5..].Split("__", 2);
                return parts.Length == 2 ? "Using " + parts[0] + " · " + parts[1].Replace('_', ' ') : "Using " + tool[5..];
            }
            return tool.Length > 0 ? "Running " + Clip(tool, 32) : "Working";
        }
        finally { doc?.Dispose(); }
    }
}
