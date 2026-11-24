using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// Finds the run behind a Task/Agent tool call. Claude Code keeps each run
/// next to its parent's transcript (…/&lt;parent&gt;/subagents/agent-&lt;hex&gt;.jsonl);
/// the server has no parent link, so this is matched locally by parent and
/// description (the prompt breaks ties), and on another computer by
/// searching the captured chats for the prompt.
/// </summary>
internal static class SubagentLinks
{
    /// <summary>Native ids a chat's sub-agents can hang off: its own, plus the agent sessions Nexus drove for it.</summary>
    public static async Task<HashSet<string>> ParentNativesAsync(string parentSessionId, string parentNativeId)
    {
        var parents = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
        if (parentNativeId.Length > 0) parents.Add(parentNativeId);
        if (parentSessionId.Length > 0)
        {
            parents.Add(parentSessionId);
            if ((await SubagentRuns.DrivenIndexAsync()).NativesBySession.TryGetValue(parentSessionId, out var driven)) parents.UnionWith(driven);
        }
        return parents;
    }

    /// <summary>This chat's runs on this computer, oldest first.</summary>
    public static async Task<List<SubagentRuns.LocalRun>> RunsForParentAsync(string parentSessionId, string parentNativeId)
    {
        var parents = await ParentNativesAsync(parentSessionId, parentNativeId);
        if (parents.Count == 0) return new();
        var index = await SubagentRuns.LocalIndexAsync();
        return index.Values.Where(r => parents.Contains(r.ParentNativeId)).OrderBy(r => r.Created).ToList();
    }

    private static readonly Dictionary<string, string> s_firstPrompt = new(StringComparer.OrdinalIgnoreCase);

    private static string FirstPrompt(SubagentRuns.LocalRun run)
    {
        lock (s_firstPrompt)
            if (s_firstPrompt.TryGetValue(run.NativeId, out var known)) return known;
        var prompt = "";
        try
        {
            if (run.Transcript.Length > 0)
                foreach (var line in File.ReadLines(run.Transcript).Take(6))
                    if (SubagentTranscript.ParseLine(line) is { Role: "user" } turn) { prompt = turn.Text; break; }
        }
        catch { /* unreadable: no tie-break */ }
        lock (s_firstPrompt) s_firstPrompt[run.NativeId] = prompt;
        return prompt;
    }

    /// <summary>The local run started by a call with this description and prompt.</summary>
    public static SubagentRuns.LocalRun? Match(IReadOnlyList<SubagentRuns.LocalRun> runs, string description, string prompt)
    {
        var wanted = description.Trim();
        var candidates = runs.Where(r => wanted.Length > 0 && r.Description.Trim().Equals(wanted, StringComparison.OrdinalIgnoreCase)).ToList();
        if (candidates.Count == 1) return candidates[0];
        var pool = candidates.Count > 0 ? candidates : runs.ToList();
        var head = Head(prompt);
        if (head.Length == 0) return candidates.FirstOrDefault();
        return pool.FirstOrDefault(r => Head(FirstPrompt(r)).Equals(head, StringComparison.Ordinal)) ?? candidates.FirstOrDefault();
    }

    private static string Head(string text)
    {
        var flat = string.Join(' ', text.Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries));
        return flat.Length > 160 ? flat[..160] : flat;
    }

    /// <summary>
    /// The captured chat of a run when its transcript is not on this computer:
    /// the run's chat is titled with its first request, so the timeline is
    /// searched for the start of the prompt among sub-agent chats.
    /// </summary>
    public static async Task<(string Id, string Title, long Turns)> FindRemoteAsync(NxClient core, string projectId, string prompt)
    {
        var head = Head(prompt);
        if (head.Length < 12) return ("", "", -1);
        var q = head.Length > 60 ? head[..60] : head;
        var response = await core.CallAsync(NxMethods.TimelineList, projectId.Length > 0 ? new { project_id = projectId, q, limit = 20 } : (object)new { q, limit = 20 });
        if (!response.Ok) return ("", "", -1);
        foreach (var item in NxJson.Items(response.Result))
        {
            if (!SessionLineage.IsSubagentSession(item)) continue;
            var title = string.Join(' ', NxJson.Text(item, "title", "summary").Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries));
            var probe = title.TrimEnd('…');
            if (probe.Length < 12 || !head.StartsWith(probe[..Math.Min(probe.Length, 40)], StringComparison.OrdinalIgnoreCase)) continue;
            return (NxJson.Text(item, "session_id", "id"), title, long.TryParse(NxJson.Text(item, "turn_count"), out var turns) ? turns : -1);
        }
        return ("", "", -1);
    }
}

/// <summary>
/// A sub-agent run inside its parent chat: type, task, status, duration and
/// message count at a glance; its messages when expanded (read on demand, from
/// the local transcript or the captured chat).
/// </summary>
internal sealed class SubagentCard : UserControl
{
    private const int MaxTurnsShown = 60;
    private readonly string _projectId, _parentSessionId, _parentNativeId, _description, _prompt, _type;
    private readonly TextBlock _status = new() { FontSize = 11.5, Opacity = 0.7, VerticalAlignment = VerticalAlignment.Center, TextTrimming = TextTrimming.CharacterEllipsis };
    private readonly StackPanel _body = new() { Spacing = 8, Margin = new Thickness(4, 6, 4, 4) };
    private readonly Expander _expander;
    private SubagentRuns.LocalRun? _local;
    private Task? _resolve;
    private bool _bodyLoaded;
    private string _chatId = "";

    public SubagentCard(string projectId, string parentSessionId, string parentNativeId, string type, string description, string prompt)
    {
        _projectId = projectId; _parentSessionId = parentSessionId; _parentNativeId = parentNativeId;
        _type = type; _description = description; _prompt = prompt;
        var kind = type.Length > 0 ? "Sub-agent · " + WorkLogText.Humanize(type) : "Sub-agent";
        var title = description.Length > 0 ? description : WorkLogText.OneLine(prompt, 90);
        var header = new Grid { ColumnSpacing = 10 };
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        header.Children.Add(new FontIcon { Glyph = "", FontSize = 15, Foreground = NexusPalette.Accent, VerticalAlignment = VerticalAlignment.Center });
        var text = new StackPanel { Spacing = 1 };
        text.Children.Add(new TextBlock { Text = kind.ToUpperInvariant(), FontSize = 10, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, Opacity = 0.55, CharacterSpacing = 40 });
        text.Children.Add(new TextBlock { Text = NxJson.Scrub(title), FontSize = 13, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, TextTrimming = TextTrimming.CharacterEllipsis });
        text.Children.Add(_status);
        Grid.SetColumn(text, 1);
        header.Children.Add(text);
        _status.Text = "Looking for this run…";
        _expander = new Expander
        {
            Header = header, Content = _body, IsExpanded = false,
            HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch,
            Margin = new Thickness(0, 4, 0, 6)
        };
        AutomationProperties.SetName(_expander, kind + ": " + title);
        _expander.Expanding += (_, _) => { if (!_bodyLoaded) { _bodyLoaded = true; _ = LoadBodyAsync(); } Pages.Reveal.AfterExpand(_expander); };
        Content = _expander;
        // Status needs the local index (cached after the first card) and two
        // small file reads; it never blocks the transcript paint.
        Loaded += (_, _) => _resolve ??= ResolveAsync();
    }

    public string Description => _description;

    /// <summary>Opens the card and scrolls it into view (from the chat's sub-agent list).</summary>
    public void Reveal()
    {
        _expander.IsExpanded = true;
        StartBringIntoView(new BringIntoViewOptions { VerticalAlignmentRatio = 0.1, AnimationDesired = true });
    }

    private async Task ResolveAsync()
    {
        try
        {
            var runs = await SubagentLinks.RunsForParentAsync(_parentSessionId, _parentNativeId);
            _local = await Task.Run(() => SubagentLinks.Match(runs, _description, _prompt));
            if (_local is { } run)
            {
                var (start, end) = await Task.Run(() => SafeSpan(run));
                _end = end;
                await UiThread.Resume(DispatcherQueue);
                _status.Text = StatusLine(run, start, null, end);
                return;
            }
            var remote = await SubagentLinks.FindRemoteAsync(App.Core, _projectId, _prompt);
            await UiThread.Resume(DispatcherQueue);
            _chatId = remote.Id;
            _status.Text = remote.Id.Length > 0
                ? "Captured" + (remote.Turns > 0 ? " · " + Nexus.Core.FriendlyTime.Count(remote.Turns, "message") : "") + " · click to see its messages"
                : "Its steps were done on another computer; the result is in the reply below";
        }
        catch (Exception ex)
        {
            await UiThread.Resume(DispatcherQueue);
            Nexus.Core.Diagnostics.DiagLog.Warn("subagent", "could not read a sub-agent run", ex);
            _status.Text = "Couldn't open this sub-agent's steps right now.";
        }
    }

    private DateTimeOffset? _end;

    // First timestamp from the head, last from the final 64 KB: the run's span
    // without reading a transcript that can be tens of megabytes.
    private static (DateTimeOffset? Start, DateTimeOffset? End) SafeSpan(SubagentRuns.LocalRun run)
    {
        if (run.Transcript.Length == 0) return (null, null);
        try
        {
            var start = SubagentTranscript.FirstTimestamp(File.ReadLines(run.Transcript));
            using var stream = new FileStream(run.Transcript, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete);
            var size = (int)Math.Min(stream.Length, 64 * 1024);
            stream.Seek(-size, SeekOrigin.End);
            var buffer = new byte[size];
            stream.ReadExactly(buffer);
            return (start, SubagentTranscript.LastTimestamp(System.Text.Encoding.UTF8.GetString(buffer)));
        }
        catch { return (null, null); }
    }

    internal static string StatusLine(SubagentRuns.LocalRun run, DateTimeOffset? start, int? messages, DateTimeOffset? lastMessage = null)
    {
        var end = lastMessage ?? new DateTimeOffset(DateTime.SpecifyKind(run.Modified, DateTimeKind.Utc));
        var running = run.Finished != true && DateTimeOffset.UtcNow - end < TimeSpan.FromMinutes(2);
        var status = run.Finished == true ? "Done" : running ? "Running" : run.Finished == false ? "Stopped" : "Ended";
        var parts = new List<string> { status };
        if (start is { } began) parts.Add(SubagentTranscript.Duration((running ? DateTimeOffset.UtcNow : end) - began));
        if (messages is { } count) parts.Add(count + (count == 1 ? " message" : " messages"));
        parts.Add(end.ToLocalTime().ToString("MMM d, h:mm tt"));
        return string.Join(" · ", parts);
    }

    private async Task LoadBodyAsync()
    {
        _body.Children.Clear();
        _body.Children.Add(new ProgressRing { IsActive = true, Width = 18, Height = 18, HorizontalAlignment = HorizontalAlignment.Left });
        if (_resolve is not null) await _resolve; else await (_resolve = ResolveAsync());
        List<SubTurn> turns;
        int total;
        string error = "";
        var watch = System.Diagnostics.Stopwatch.StartNew();
        if (_local is { Transcript.Length: > 0 } run)
        {
            try
            {
                (turns, total) = await Task.Run(() => SubagentTranscript.Read(File.ReadLines(run.Transcript), MaxTurnsShown));
                var start = turns.FirstOrDefault()?.At;
                var end = turns.LastOrDefault()?.At ?? _end;
                await UiThread.Resume(DispatcherQueue);
                _status.Text = StatusLine(run, start, total, end);
            }
            catch (Exception ex) { turns = new(); total = 0; error = ex.Message; }
        }
        else if (_chatId.Length > 0)
        {
            var response = await App.Core.CallAsync(NxMethods.SessionsTurns, new { session_id = _chatId, id = _chatId, limit = MaxTurnsShown });
            turns = response.Ok ? NxJson.Items(response.Result).Select(RemoteTurn).Where(t => t is not null).Select(t => t!).ToList() : new();
            total = turns.Count;
            if (!response.Ok) error = response.Error ?? "Could not load this run.";
        }
        else { turns = new(); total = 0; }
        await UiThread.Resume(DispatcherQueue);
        System.Diagnostics.Trace.WriteLine($"[perf] sub-agent card: {total} messages {watch.ElapsedMilliseconds} ms");
        _body.Children.Clear();
        if (_prompt.Length > 0)
            _body.Children.Add(new Expander
            {
                Header = new TextBlock { Text = "Task given to the sub-agent", FontSize = 12, Opacity = 0.75 },
                Content = new TextBlock { Text = _prompt.Length > 4000 ? _prompt[..4000] + "…" : _prompt, TextWrapping = TextWrapping.Wrap, FontSize = 12.5, IsTextSelectionEnabled = true },
                HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch
            });
        if (error.Length > 0) _body.Children.Add(new TextBlock { Text = NxErrors.IsSignedOut(error) ? NxErrors.SignedOutMessage : error, Foreground = NexusPalette.Amber, FontSize = 12, TextWrapping = TextWrapping.Wrap });
        else if (turns.Count == 0) _body.Children.Add(new TextBlock { Text = "This sub-agent ran on another computer, so its step-by-step notes aren't here. What it found is in the chat's reply.", Opacity = 0.7, FontSize = 12, TextWrapping = TextWrapping.Wrap });
        // The task prompt is already shown above; skip it as the first message.
        var shown = turns.Count > 0 && turns[0].Role == "user" && _prompt.Length > 0 ? turns.Skip(1) : turns;
        var list = new StackPanel { Spacing = 6 };
        var items = shown.ToList();
        for (var i = 0; i < items.Count; i++)
        {
            // Read keeps the first messages and the last one (the outcome).
            if (total > MaxTurnsShown && i == items.Count - 1)
                list.Children.Add(new TextBlock { Text = $"… {total - MaxTurnsShown - 1} more messages (Open as a chat to read them) …", Opacity = 0.55, FontSize = 11.5 });
            list.Children.Add(TurnView(items[i]));
        }
        if (list.Children.Count > 0) _body.Children.Add(list);
        var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
        var open = new HyperlinkButton { Content = "Open as a chat", FontSize = 12, Padding = new Thickness(0) };
        open.Click += async (_, _) => await OpenChatAsync(open);
        actions.Children.Add(open);
        _body.Children.Add(actions);
    }

    private static SubTurn? RemoteTurn(JsonElement item)
    {
        var role = NxJson.Text(item, "role");
        if (role is not ("user" or "assistant")) return null;
        var tools = new List<string>();
        if (item.TryGetProperty("tool_calls", out var calls) && calls.ValueKind == JsonValueKind.Array)
            foreach (var call in calls.EnumerateArray()) tools.Add(NxJson.Text(call, "name") is { Length: > 0 } name ? name : "tool");
        var text = NxJson.Text(item, "text_preview", "text", "content").Trim();
        if (text.Length == 0 && tools.Count == 0) return null;
        DateTimeOffset? at = DateTimeOffset.TryParse(NxJson.Text(item, "source_ts", "ts"), out var when) ? when : null;
        return new SubTurn(role, text, tools, at);
    }

    private static UIElement TurnView(SubTurn turn)
    {
        var assistant = turn.Role == "assistant";
        var panel = new StackPanel { Spacing = 2, Padding = new Thickness(10, 6, 10, 6), CornerRadius = new CornerRadius(8), Background = assistant ? null : SessionChatRender.SubtleFill };
        var label = (assistant ? "Sub-agent" : "Task") + (turn.At is { } at ? " · " + at.ToLocalTime().ToString("h:mm:ss tt") : "");
        panel.Children.Add(new TextBlock { Text = label, FontSize = 10.5, Opacity = 0.5 });
        if (turn.Text.Length > 0)
        {
            // Plain text, capped: a sub-agent's report can be long; the full
            // chat (Open as a chat) renders markdown.
            var body = turn.Text.Length > 1500 ? turn.Text[..1500] + "…" : turn.Text;
            panel.Children.Add(new TextBlock { Text = body, TextWrapping = TextWrapping.Wrap, FontSize = 12.5, IsTextSelectionEnabled = true, MaxLines = 14, TextTrimming = TextTrimming.CharacterEllipsis });
        }
        if (turn.Tools.Count > 0)
            panel.Children.Add(new TextBlock { Text = "Used " + SubagentTranscript.ToolSummary(turn.Tools), FontSize = 11.5, Opacity = 0.6, TextWrapping = TextWrapping.Wrap });
        return panel;
    }

    private async Task OpenChatAsync(HyperlinkButton button)
    {
        button.IsEnabled = false;
        var id = _chatId;
        try
        {
            if (id.Length == 0 && _local is { } run) id = (await SubagentRuns.ChatForNativeAsync(App.Core, _projectId, run.NativeId)).Id;
        }
        catch { id = ""; }
        await UiThread.Resume(DispatcherQueue);
        button.IsEnabled = true;
        if (id.Length > 0) { MainWindow.Current?.OpenSession(id); return; }
        button.Content = "Not captured as a chat yet";
    }
}
