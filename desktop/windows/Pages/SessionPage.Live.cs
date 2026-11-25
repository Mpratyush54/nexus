using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using System.Text;
using System.Text.Json;

namespace Nexus.Pages;

// Live chat. The core owns each chat's agent process and its running turn
// (internal/agentrun): this page only shows it. Leaving the chat detaches
// the view and the agent keeps working; coming back replays the turn from
// the core's buffer and keeps streaming. Permission requests show as a card
// in the reply, after the chat's mode and the project's allow rules have
// answered what they cover.
public sealed partial class SessionPage
{
    private string _liveAgent = "";
    private string _liveLastError = "";
    // The chat's permission mode. Every live agent takes one; the core maps
    // it to the agent's own flags and answers the rest by the same rules.
    private readonly ComboBox _access = new()
    {
        MinWidth = 96, Visibility = Visibility.Collapsed,
        Items =
        {
            new ComboBoxItem { Content = "Ask every time", Tag = "ask" },
            new ComboBoxItem { Content = "Auto-approve edits", Tag = "edits" },
            new ComboBoxItem { Content = "Full access", Tag = "full" }
        },
        SelectedIndex = 1
    };
    private bool _accessLoading;
    // Full access is shown with a warning beside the picker.
    private readonly FontIcon _accessWarning = new() { Glyph = "\uE7BA", FontSize = 14, VerticalAlignment = VerticalAlignment.Center, Visibility = Visibility.Collapsed };

    private string PermissionMode() => (_access.SelectedItem as ComboBoxItem)?.Tag as string ?? "edits";

    // The turn this page is showing, completed by live.recorded (or by
    // Detached when the page lets go of it).
    private TaskCompletionSource<NxResponse>? _liveTurn;
    // Events up to this sequence number are already on screen.
    private ulong _liveSeq;
    private bool _attaching;
    private readonly List<string> _attachBuffer = new();
    private readonly StringBuilder _liveText = new();
    private readonly Dictionary<string, string> _liveTools = new(StringComparer.Ordinal);
    private readonly Dictionary<string, (string Input, string Parent)> _liveToolInfo = new(StringComparer.Ordinal);
    private readonly Dictionary<string, Border> _approvalCards = new(StringComparer.Ordinal);
    private readonly StackPanel _followupBar = new() { Spacing = 4, Visibility = Visibility.Collapsed };
    private readonly Border _resumeBanner = new() { Visibility = Visibility.Collapsed, CornerRadius = new CornerRadius(10), Padding = new Thickness(12, 9, 12, 9), BorderThickness = new Thickness(1) };
    // Streaming render throttle: re-laying out a long reply on every delta
    // made the UI stutter and look frozen. First delta renders immediately,
    // later ones at most every ~66 ms.
    private DispatcherQueueTimer? _liveRenderTimer;
    private bool _liveDirty;

    /// <summary>A live turn is streaming into this page: the composer queues follow-ups.</summary>
    private bool LiveTurnActive => _liveTurn is not null;

    /// <summary>Constructor hook: the permission picker, follow-up chips, resume banner and keys.</summary>
    private void InitLiveComposer()
    {
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_access, "Permission mode");
        ToolTipService.SetToolTip(_access,
            "Ask every time: approve each file edit and command.\n" +
            "Auto-approve edits: edits in this project folder run; commands still ask.\n" +
            "Full access: nothing asks. Use only for projects you trust.");
        _access.SelectionChanged += (_, _) => OnPermissionModeChanged();
        ToolTipService.SetToolTip(_accessWarning, "Full access: agents edit files and run commands without asking.");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_accessWarning, "Full access is on");
        _composer.Children.Insert(0, _followupBar);
        _composer.Children.Insert(0, _resumeBanner);
        // Ctrl/Cmd+Enter sends now: while the agent works, it stops the
        // current reply and goes next. Send (the button) queues instead.
        _prompt.AddHandler(UIElement.PreviewKeyDownEvent, new KeyEventHandler(async (_, e) =>
        {
            if (e.Key != Windows.System.VirtualKey.Enter || !Shortcuts.IsPrimaryDown()) return;
            e.Handled = true;
            if (LiveTurnActive) await SendFollowupAsync(now: true);
            else await SendHereAsync();
        }), true);
    }

    private void OnPermissionModeChanged()
    {
        var mode = PermissionMode();
        ApplyAccessStyle(mode);
        if (_accessLoading || _id.Length == 0) return;
        AppPrefs.Data.ChatPermissionModes ??= new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        AppPrefs.Data.ChatPermissionModes[_id] = mode;
        AppPrefs.Save();
        // Applies at once: waiting requests the new mode allows are answered.
        _ = Core.CallAsync("live.set_mode", new { session_id = _id, mode });
    }

    private void ApplyAccessStyle(string mode)
    {
        var full = mode == "full";
        _accessWarning.Foreground = NexusPalette.Amber;
        _accessWarning.Visibility = full ? Visibility.Visible : Visibility.Collapsed;
        if (full)
        {
            _access.BorderBrush = NexusPalette.Amber;
            _access.BorderThickness = new Thickness(1);
        }
        else
        {
            _access.ClearValue(Control.BorderBrushProperty);
            _access.ClearValue(Control.BorderThicknessProperty);
        }
    }

    /// <summary>Selects the chat's saved mode, or the default from Settings.</summary>
    private void LoadPermissionMode()
    {
        var mode = AppPrefs.Data.ChatPermissionModes is { } modes && _id.Length > 0 && modes.TryGetValue(_id, out var saved) ? saved : LiveSessions.Current.DefaultMode;
        _accessLoading = true;
        try
        {
            foreach (var item in _access.Items.OfType<ComboBoxItem>())
                if (item.Tag as string == mode) _access.SelectedItem = item;
        }
        finally { _accessLoading = false; }
        ApplyAccessStyle(PermissionMode());
    }

    private void QueueLiveRender()
    {
        _liveDirty = true;
        if (_liveRenderTimer is null)
        {
            _liveRenderTimer = DispatcherQueue.CreateTimer();
            _liveRenderTimer.Interval = TimeSpan.FromMilliseconds(66);
            _liveRenderTimer.IsRepeating = true;
            _liveRenderTimer.Tick += (timer, _) =>
            {
                if (!_liveDirty) { timer.Stop(); return; }
                _liveDirty = false;
                ShowLiveText();
            };
        }
        if (!_liveRenderTimer.IsRunning)
        {
            _liveDirty = false;
            ShowLiveText();
            _liveRenderTimer.Start();
        }
    }

    private string LiveAgentName => ChatTitles.AgentName(_liveAgent);

    private static bool SupportsLive(string agent) => agent is "claude" or "cursor" or "opencode" or "gemini" or "kimi" or "codex";

    private void ResetLiveStream(string agent)
    {
        _liveAgent = agent;
        _liveText.Clear();
        _liveTools.Clear();
        _liveToolInfo.Clear();
        _liveLastError = "";
        ClearApprovalCards();
    }

    // Sends one message to the chat's agent and completes once the core has
    // recorded the finished turn (same result shape as continue.here), or
    // with Detached when the person leaves the chat; the agent keeps going.
    private async Task<NxResponse> SendLiveAsync(TurnView view, string sessionID, string agent, string model, string prompt, string context, string resumeID, string workspace, string[]? images = null)
    {
        ResetLiveStream(agent);
        var turn = new TaskCompletionSource<NxResponse>(TaskCreationOptions.RunContinuationsAsynchronously);
        _liveTurn = turn;
        _liveSeq = 0;
        ApplyLiveComposerState();
        SetComposerStatus("Starting " + ChatTitles.AgentName(agent) + "…");
        var title = _header.Text is "New chat" or "Opening chat…" or "Session" ? "" : _header.Text;
        var sent = await Call("live.send", new
        {
            session_id = sessionID, project_id = _projectID, agent, workspace_root = workspace, resume_id = resumeID,
            model, context, mode = PermissionMode(), text = prompt, images = images ?? Array.Empty<string>(), title
        });
        if (!sent.Ok)
        {
            if (ReferenceEquals(_liveTurn, turn)) _liveTurn = null;
            return sent;
        }
        if (sent.Result is { } r && r.TryGetProperty("queued", out var q) && q.ValueKind == JsonValueKind.True)
            SetComposerStatus("Waiting for a free slot · " + LiveSessions.Current.Running + " agents are working. This starts when one finishes.");
        else
            SetComposerStatus("Waiting for " + ChatTitles.AgentName(agent) + "…");
        var outcome = await turn.Task;
        var problem = outcome.Error ?? "";
        if (!ReferenceEquals(outcome, Detached) && !outcome.Ok && resumeID.Length > 0 && _id == sessionID &&
            (problem.Contains("not found", StringComparison.OrdinalIgnoreCase) || problem.Contains("No conversation", StringComparison.OrdinalIgnoreCase)))
        {
            // The agent no longer knows that session (e.g. Claude Code exits on
            // an unknown --resume id). Continue once in a fresh session.
            _localNativeID = "";
            await Core.CallAsync("live.close", new { session_id = sessionID });
            SetComposerStatus("Starting a fresh " + ChatTitles.AgentName(agent) + " session with the conversation so far…");
            return await SendLiveAsync(view, sessionID, agent, model, prompt, context, "", workspace, images);
        }
        return outcome;
    }

    /// <summary>A message sent while the agent works waits for its turn, or stops it with "now".</summary>
    private async Task SendFollowupAsync(bool now)
    {
        var text = ComposerAttachments.WithFiles(_prompt.Text.Trim(), _files);
        if (_prompt.Text.Trim().Length == 0) { SetComposerStatus("Write a follow-up first."); return; }
        var images = _images.ToArray();
        var response = await Call("live.send", new
        {
            session_id = _id, project_id = _projectID, agent = _liveAgent.Length > 0 ? _liveAgent : SelectedHarness(),
            workspace_root = _workspace.Text.Trim(), model = SelectedModel(), mode = PermissionMode(),
            context = _continuationContext, text, images, when = now ? "now" : "queue"
        });
        if (!response.Ok) { SetComposerStatus(response.Error ?? "Could not queue that message."); return; }
        _prompt.Text = "";
        ClearImageAttachments();
        SetComposerStatus(now ? "Stopping this reply to send yours next…" : "Queued · it sends when this reply finishes.");
    }

    private async Task InterruptLiveAsync()
    {
        var response = await Call("live.interrupt", new { session_id = _id });
        if (!response.Ok) SetComposerStatus(response.Error ?? "Could not stop this reply.");
    }

    /// <summary>
    /// The page stops showing the live turn (the chat closed, another chat
    /// opened). Nothing stops in the core: the agent keeps working.
    /// </summary>
    private void DetachLiveView()
    {
        var turn = _liveTurn;
        _liveTurn = null;
        _attaching = false;
        _attachBuffer.Clear();
        turn?.TrySetResult(Detached);
        ClearApprovalCards();
    }

    /// <summary>Re-shows the chat's live state when the page comes back.</summary>
    private async Task ReattachLiveAsync()
    {
        if (!_sessionLoaded || _id.Length == 0 || _sendInFlight || _attaching) return;
        if (_loadedAt == DateTime.MinValue) await RevalidateAsync();
        await AttachLiveAsync();
    }

    /// <summary>
    /// Shows what the core is doing in this chat: a running turn is rebuilt
    /// from its buffered events and keeps streaming; a turn stopped by a
    /// restart offers Resume.
    /// </summary>
    private async Task AttachLiveAsync()
    {
        if (_id.Length == 0 || _sendInFlight || _attaching) return;
        var sessionID = _id;
        _attaching = true;
        _attachBuffer.Clear();
        NxResponse response;
        try { response = await Call("live.attach", new { session_id = sessionID }); }
        catch (Exception) { _attaching = false; return; }
        if (!_attaching || _id != sessionID || response.Result is not { } snap || _sendInFlight)
        {
            _attaching = false;
            return;
        }
        var status = NxJson.Text(snap, "status");
        RenderFollowups(snap.TryGetProperty("followups", out var f) ? f : default);
        ShowResumeBanner(status == "interrupted" && snap.TryGetProperty("interrupted", out var rec) ? rec : default);
        if (status is not ("running" or "approval" or "queued"))
        {
            _attaching = false;
            return;
        }
        var agent = NxJson.Text(snap, "agent");
        var images = snap.TryGetProperty("images", out var im) && im.ValueKind == JsonValueKind.Array
            ? im.EnumerateArray().Select(i => i.GetString() ?? "").Where(p => p.Length > 0).ToArray() : Array.Empty<string>();
        _sendInFlight = true;
        SetSending(true);
        var view = BeginTurnView(agent, NxJson.Text(snap, "prompt"), images, Guid.NewGuid().ToString("N"));
        ResetLiveStream(agent);
        var turn = new TaskCompletionSource<NxResponse>(TaskCreationOptions.RunContinuationsAsynchronously);
        _liveTurn = turn;
        ApplyLiveComposerState();
        SetComposerStatus(status == "queued" ? "Waiting for a free slot · this starts when another agent finishes." : ChatTitles.AgentName(agent) + " is still working…");
        _liveSeq = snap.TryGetProperty("seq", out var seq) && seq.TryGetUInt64(out var s) ? s : 0;
        if (snap.TryGetProperty("events", out var events) && events.ValueKind == JsonValueKind.Array)
            foreach (var e in events.EnumerateArray()) ApplyLiveEvent(e, replay: true);
        _attaching = false;
        foreach (var json in _attachBuffer.ToList()) HandleLiveEvent(json);
        _attachBuffer.Clear();
        ScrollChatToEnd();
        NxResponse outcome;
        try { outcome = await turn.Task; }
        finally { EndTurnStreaming(view); }
        FinishTurnView(view, outcome, sessionID, "");
        if (!ReferenceEquals(outcome, Detached) && _id == sessionID) _ = Core.CallAsync("live.seen", new { session_id = sessionID });
    }

    /// <summary>Keeps the composer usable while the agent works: Send queues.</summary>
    private void ApplyLiveComposerState()
    {
        if (!LiveTurnActive) return;
        _prompt.IsEnabled = true;
        _sendHere.IsEnabled = !ShellState.Current.Offline;
        _attachImage.IsEnabled = _sendHere.IsEnabled;
        ToolTipService.SetToolTip(_sendHere, "Queue · sends when this reply finishes (" + Shortcuts.Label("Enter") + " sends now)");
    }

    // Returns true when the event was a live event (handled or ignored here).
    private bool HandleLiveEvent(string json)
    {
        if (!json.Contains("\"type\":\"live.", StringComparison.Ordinal)) return false;
        try
        {
            using var doc = JsonDocument.Parse(json);
            var root = doc.RootElement;
            if (_id.Length == 0 || NxJson.Text(root, "session_id") != _id) return true;
            switch (NxJson.Text(root, "type"))
            {
                case "live.followups":
                    RenderFollowups(root.TryGetProperty("followups", out var f) ? f : default);
                    return true;
                case "live.status":
                    if (NxJson.Text(root, "status") != "interrupted") ShowResumeBanner(default);
                    return true;
                case "live.turn_started":
                    // A follow-up, a queued start or a resume began on its own.
                    if (!LiveTurnActive && !_sendInFlight) _ = AttachLiveAsync();
                    return true;
            }
            if (_attaching) { _attachBuffer.Add(json); return true; }
            if (!LiveTurnActive) return true;
            if (root.TryGetProperty("seq", out var seq) && seq.TryGetUInt64(out var n))
            {
                if (n <= _liveSeq) return true; // already replayed
                _liveSeq = n;
            }
            ApplyLiveEvent(root, replay: false);
        }
        catch (JsonException) { }
        return true;
    }

    private void ApplyLiveEvent(JsonElement root, bool replay)
    {
        if (NxJson.Text(root, "type") == "live.recorded")
        {
            var turn = _liveTurn;
            _liveTurn = null;
            if (root.TryGetProperty("result", out var result) && result.ValueKind == JsonValueKind.Object)
                turn?.TrySetResult(new NxResponse(true, null, result.Clone()));
            else
                turn?.TrySetResult(new NxResponse(false, NxJson.Text(root, "error") is { Length: > 0 } e ? AppDiagnostics.Friendly("live", e) : "The reply could not be saved.", null));
            return;
        }
        var kind = NxJson.Text(root, "kind");
        var text = NxJson.Text(root, "text");
        switch (kind)
        {
            case "text_delta":
                _liveText.Append(text);
                QueueLiveRender();
                break;
            case "text":
                // Deltas already rendered this block; separate it from the next one.
                if (!_streamHasText) _liveText.Append(text);
                _liveText.Append("\n\n");
                QueueLiveRender();
                break;
            case "thinking_delta":
                if (!_streamHasText && _streamText is not null) _streamText.Text = "Thinking…";
                SetComposerStatus(LiveAgentName + " is thinking…");
                break;
            case "tool_start":
            {
                var id = NxJson.Text(root, "tool_id");
                var tool = NxJson.Text(root, "tool_name");
                if (id.Length == 0 || tool.Length == 0) break;
                _liveTools[id] = tool;
                var input = NxJson.Text(root, "tool_input");
                var parent = NxJson.Text(root, "parent_tool_id");
                _liveToolInfo[id] = (input, parent);
                _streamActivity?.Apply(ActivityJson(id, tool, "running", input, "", parent));
                SetComposerStatus(LiveAgentName + " is running " + tool + "…");
                if (!_streamHasText && _streamText is not null) _streamText.Text = "Working in your project…";
                break;
            }
            case "tool_end":
            {
                var id = NxJson.Text(root, "tool_id");
                if (!_liveTools.TryGetValue(id, out var tool)) break;
                var failed = root.TryGetProperty("is_error", out var err) && err.ValueKind == JsonValueKind.True;
                var info = _liveToolInfo.GetValueOrDefault(id);
                _streamActivity?.Apply(ActivityJson(id, tool, failed ? "error" : "completed", info.Input, NxJson.Text(root, "tool_output"), info.Parent));
                break;
            }
            case "subagent_text":
            {
                // A sub-agent's own words: show them as its latest progress.
                var id = NxJson.Text(root, "tool_id");
                if (!_liveTools.TryGetValue(id, out var tool)) break;
                var info = _liveToolInfo.GetValueOrDefault(id);
                _streamActivity?.Apply(ActivityJson(id, tool, "running", info.Input, text, info.Parent));
                SetComposerStatus("Sub-agent working…");
                break;
            }
            case "approval":
                ShowApprovalCard(NxJson.Text(root, "request_id"), root.TryGetProperty("action", out var action) ? action : default, focus: !replay);
                break;
            case "approval_resolved":
                ResolveApprovalCard(NxJson.Text(root, "request_id"), NxJson.Text(root, "decision"));
                break;
            case "auto_approved":
                if (NxJson.Text(root, "summary") is { Length: > 0 } summary) SetComposerStatus("Allowed by this chat's permissions · " + summary);
                break;
            case "status":
            case "error":
                if (kind == "error" && text.Length > 0) text = AppDiagnostics.Friendly("live", text);
                if (text.Length > 0) SetComposerStatus(text);
                if (kind == "error" && text.Length > 0) _liveLastError = text;
                break;
            case "exited":
                var pending = _liveTurn;
                _liveTurn = null;
                var why = _liveLastError.Length > 0 ? _liveLastError + " · Send again to restart." : "The agent stopped. Send again to restart it.";
                _liveLastError = "";
                pending?.TrySetResult(new NxResponse(false, why, null));
                break;
        }
    }

    private void ShowLiveText()
    {
        if (_streamText is null) return;
        var text = _liveText.ToString().TrimEnd();
        if (text.Length == 0) return;
        _streamHasText = true;
        var nearBottom = _chatScroll.ScrollableHeight - _chatScroll.VerticalOffset < 120;
        _streamText.Text = text.Length <= 24000 ? text : text[^24000..];
        SetComposerStatus("Replying with " + (_liveAgent.Length > 0 ? LiveAgentName : ChatTitles.AgentName(_streamAgent)) + "…");
        // Stuck to the bottom, the SizeChanged re-pin follows the stream.
        if (nearBottom && !_stickToBottom) ScrollChatToEnd();
    }

    private static string ActivityJson(string id, string tool, string status, string input, string output, string parent = "")
    {
        var files = new List<string>();
        var command = "";
        var title = "";
        if (input.Length > 0)
        {
            try
            {
                using var doc = JsonDocument.Parse(input);
                var root = doc.RootElement;
                var path = NxJson.Text(root, "file_path");
                if (path.Length > 0) files.Add(path);
                command = NxJson.Text(root, "command");
                title = DescribeTool(tool, root);
            }
            catch (JsonException) { }
        }
        if (title.Length == 0) title = DescribeTool(tool, default);
        return JsonSerializer.Serialize(new { id, tool, status, input, output, command, files, title, parent, error = status == "error" ? output : "" });
    }

    // Human labels for steps that are not plain file/command tools.
    internal static string DescribeTool(string tool, JsonElement input)
    {
        string Arg(params string[] keys) => input.ValueKind == JsonValueKind.Object ? NxJson.Text(input, keys) : "";
        if (tool is "Task" or "Agent")
        {
            var kind = Arg("subagent_type", "agent_type", "name");
            var what = Arg("description", "prompt", "task");
            if (what.Length > 90) what = what[..87] + "…";
            return "Sub-agent · " + (kind.Length > 0 ? kind : "agent") + (what.Length > 0 ? ": " + what : "");
        }
        var mcp = tool.StartsWith("mcp__", StringComparison.Ordinal) ? tool[5..].Split("__", 2) : null;
        if (mcp is { Length: 2 } && mcp[0] == "nexus" || tool is "memory_search" or "memory_write" or "project_knowledge" or "session_fetch" or "session_summary_get")
        {
            var name = mcp is { Length: 2 } ? mcp[1] : tool;
            var query = Arg("query", "key", "content");
            if (query.Length > 70) query = query[..67] + "…";
            return name switch
            {
                "memory_search" => "Memory · searched" + (query.Length > 0 ? " “" + query + "”" : ""),
                "memory_write" => "Memory · saved" + (query.Length > 0 ? " “" + query + "”" : ""),
                "project_knowledge" => "Memory · read project knowledge",
                "session_fetch" or "session_summary_get" => "Memory · read an earlier session",
                _ => "Memory · " + name.Replace('_', ' ')
            };
        }
        if (mcp is { Length: 2 }) return mcp[0] + " · " + mcp[1].Replace('_', ' ');
        return "";
    }

    // ── Inline approval card ─────────────────────────────────────────────
    // "Run `npm test`" with Allow once (Enter), Deny (Esc), Allow for this
    // chat, and Always allow <command prefix or tool> in this project.

    private void ShowApprovalCard(string requestID, JsonElement action, bool focus)
    {
        if (requestID.Length == 0 || _approvalCards.ContainsKey(requestID) || _turnView is not { } view) return;
        var summary = NxJson.Text(action, "summary");
        if (summary.Length == 0) summary = "Use a tool";
        var ruleLabel = NxJson.Text(action, "rule_label");
        var detail = NxJson.Text(action, "detail");
        var kind = NxJson.Text(action, "kind");
        var agentName = _liveAgent.Length > 0 ? LiveAgentName : "The agent";

        var body = new StackPanel { Spacing = 10 };
        var head = new Grid { ColumnSpacing = 10 };
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        head.Children.Add(new FontIcon
        {
            Glyph = kind == "edit" ? "" : kind == "command" ? "" : "", FontSize = 16,
            Foreground = NexusPalette.Amber, VerticalAlignment = VerticalAlignment.Top, Margin = new Thickness(0, 2, 0, 0)
        });
        var words = new StackPanel { Spacing = 2 };
        words.Children.Add(new TextBlock { Text = agentName + " wants to", FontSize = 12, Foreground = NexusPalette.Muted });
        words.Children.Add(new TextBlock { Text = summary, FontSize = 14.5, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true });
        Grid.SetColumn(words, 1);
        head.Children.Add(words);
        body.Children.Add(head);
        if (detail.Length > 0)
        {
            body.Children.Add(new Expander
            {
                Header = kind == "edit" ? "Show the changes" : "Show details",
                Content = new ScrollViewer { MaxHeight = 320, Content = SessionChatRender.CodeBlock(detail) },
                HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch
            });
        }
        var allow = new Button { Content = "Allow once", Style = Application.Current.Resources["AccentButtonStyle"] as Style, MinWidth = 104 };
        var deny = new Button { Content = "Deny", MinWidth = 76 };
        ToolTipService.SetToolTip(allow, "Allow once · Enter");
        ToolTipService.SetToolTip(deny, "Deny · Esc");
        body.Children.Add(new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8, Children = { allow, deny } });
        var chat = new HyperlinkButton { Content = "Allow for this chat", Padding = new Thickness(4, 2, 4, 2), FontSize = 12.5 };
        var always = new HyperlinkButton
        {
            Content = new TextBlock { Text = "Always allow " + (ruleLabel.Length > 0 ? ruleLabel : "this") + " in this project", TextWrapping = TextWrapping.Wrap },
            Padding = new Thickness(4, 2, 4, 2), FontSize = 12.5
        };
        body.Children.Add(new StackPanel
        {
            Orientation = Orientation.Horizontal, Spacing = 2,
            Children = { chat, new TextBlock { Text = "·", Opacity = 0.5, VerticalAlignment = VerticalAlignment.Center }, always }
        });

        var card = new Border
        {
            Child = body, Padding = new Thickness(14, 12, 14, 12), CornerRadius = new CornerRadius(10),
            BorderThickness = new Thickness(1), BorderBrush = NexusPalette.Amber, Background = SessionChatRender.SubtleFill,
            Margin = new Thickness(0, 4, 0, 4), MaxWidth = 820, HorizontalAlignment = HorizontalAlignment.Left, Tag = summary
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(card, "Approval needed: " + summary);
        _approvalCards[requestID] = card;

        async Task Answer(string decision)
        {
            foreach (var b in new Control[] { allow, deny, chat, always }) b.IsEnabled = false;
            var response = await Call("live.approve", new { session_id = _id, request_id = requestID, decision });
            if (!response.Ok)
            {
                SetComposerStatus(response.Error ?? "Could not send your answer.");
                ResolveApprovalCard(requestID, "gone");
                return;
            }
            ResolveApprovalCard(requestID, decision);
        }
        allow.Click += async (_, _) => await Answer("once");
        deny.Click += async (_, _) => await Answer("deny");
        chat.Click += async (_, _) => await Answer("chat");
        always.Click += async (_, _) => await Answer("project");
        card.KeyDown += async (_, e) =>
        {
            if (!allow.IsEnabled) return;
            if (e.Key == Windows.System.VirtualKey.Enter) { e.Handled = true; await Answer("once"); }
            else if (e.Key == Windows.System.VirtualKey.Escape) { e.Handled = true; await Answer("deny"); }
        };

        // Inside the reply it belongs to, above its file changes.
        var at = Math.Max(0, view.StreamStack.Children.Count - 1);
        view.StreamStack.Children.Insert(at, card);
        if (focus) Motion.FadeSlideIn(card, dy: 8, durationMs: 260);
        SetComposerStatus("Waiting for your approval · " + summary);
        if (focus && IsLoaded && _prompt.Text.Length == 0) allow.Focus(FocusState.Programmatic);
        if (focus) ScrollChatToEnd();
    }

    /// <summary>Collapses an answered card to one quiet line.</summary>
    private void ResolveApprovalCard(string requestID, string decision)
    {
        if (!_approvalCards.Remove(requestID, out var card)) return;
        var summary = card.Tag as string ?? "";
        var (label, brush) = decision switch
        {
            "deny" => ("Denied", NexusPalette.Amber),
            "chat" => ("Allowed for this chat", NexusPalette.Teal),
            "project" => ("Always allowed in this project", NexusPalette.Teal),
            "rule" => ("Allowed by a project rule", NexusPalette.Teal),
            "mode" => ("Allowed by this chat's permission mode", NexusPalette.Teal),
            "gone" => ("No longer waiting", NexusPalette.Muted),
            _ => ("Allowed once", NexusPalette.Teal)
        };
        card.Child = new TextBlock
        {
            Text = (decision == "deny" ? "✕ " : decision == "gone" ? "" : "✓ ") + label + " · " + summary,
            FontSize = 12.5, Foreground = brush, TextWrapping = TextWrapping.Wrap
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(card, label + ": " + summary);
        card.Padding = new Thickness(2, 0, 2, 0);
        card.BorderThickness = new Thickness(0);
        card.Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        if (_approvalCards.Count == 0 && LiveTurnActive) SetComposerStatus(LiveAgentName + " is working…");
    }

    private void ClearApprovalCards()
    {
        foreach (var id in _approvalCards.Keys.ToList()) ResolveApprovalCard(id, "gone");
    }

    // ── Follow-ups waiting behind the running reply ──────────────────────

    private void RenderFollowups(JsonElement list)
    {
        _followupBar.Children.Clear();
        var items = list.ValueKind == JsonValueKind.Array ? list.EnumerateArray().ToList() : new List<JsonElement>();
        for (var i = 0; i < items.Count; i++)
        {
            var index = i;
            var first = NxJson.Text(items[i], "text").Split('\n', 2)[0].Trim();
            if (first.Length > 90) first = first[..89] + "…";
            var row = new Grid { ColumnSpacing = 6 };
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            row.Children.Add(new FontIcon { Glyph = "", FontSize = 12, Opacity = 0.7, VerticalAlignment = VerticalAlignment.Center });
            var label = new TextBlock { Text = (index == 0 ? "Next · " : "Queued · ") + first, FontSize = 12.5, TextTrimming = TextTrimming.CharacterEllipsis, VerticalAlignment = VerticalAlignment.Center };
            Grid.SetColumn(label, 1);
            row.Children.Add(label);
            var edit = new Button { Content = new FontIcon { Glyph = "", FontSize = 11 }, Padding = new Thickness(6, 3, 6, 3), Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0) };
            var remove = new Button { Content = new FontIcon { Glyph = "", FontSize = 11 }, Padding = new Thickness(6, 3, 6, 3), Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0) };
            ToolTipService.SetToolTip(edit, "Edit · puts it back in the message box");
            ToolTipService.SetToolTip(remove, "Remove");
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(edit, "Edit queued message");
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(remove, "Remove queued message");
            Grid.SetColumn(edit, 2);
            Grid.SetColumn(remove, 3);
            row.Children.Add(edit);
            row.Children.Add(remove);
            edit.Click += async (_, _) =>
            {
                var taken = await Call("live.followup_remove", new { session_id = _id, index });
                if (taken.Ok && taken.Result is { } r && _prompt.Text.Trim().Length == 0)
                {
                    _prompt.Text = NxJson.Text(r, "text");
                    _prompt.Focus(FocusState.Programmatic);
                    _prompt.SelectionStart = _prompt.Text.Length;
                }
            };
            remove.Click += async (_, _) => await Call("live.followup_remove", new { session_id = _id, index });
            _followupBar.Children.Add(new Border
            {
                Child = row, Padding = new Thickness(10, 4, 4, 4), CornerRadius = new CornerRadius(8),
                Background = SessionChatRender.SubtleFill, BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1)
            });
        }
        _followupBar.Visibility = items.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    // ── A turn Nexus closed on ───────────────────────────────────────────

    private void ShowResumeBanner(JsonElement record)
    {
        if (record.ValueKind != JsonValueKind.Object)
        {
            _resumeBanner.Visibility = Visibility.Collapsed;
            return;
        }
        var prompt = NxJson.Text(record, "prompt").Split('\n', 2)[0].Trim();
        if (prompt.Length > 80) prompt = prompt[..79] + "…";
        var agent = ChatTitles.AgentName(NxJson.Text(record, "agent"));
        var grid = new Grid { ColumnSpacing = 10 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        grid.Children.Add(new FontIcon { Glyph = "\uE7BA", FontSize = 15, Foreground = NexusPalette.Amber, VerticalAlignment = VerticalAlignment.Center });
        var words = new StackPanel { Spacing = 1 };
        words.Children.Add(new TextBlock { Text = agent + " stopped when Nexus closed", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, FontSize = 13 });
        if (prompt.Length > 0) words.Children.Add(new TextBlock { Text = "It was working on: " + prompt, FontSize = 12, Foreground = NexusPalette.Muted, TextTrimming = TextTrimming.CharacterEllipsis });
        Grid.SetColumn(words, 1);
        grid.Children.Add(words);
        var resume = new Button { Content = "Resume", Style = Application.Current.Resources["AccentButtonStyle"] as Style };
        var dismiss = new Button { Content = "Dismiss" };
        var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6, VerticalAlignment = VerticalAlignment.Center, Children = { resume, dismiss } };
        Grid.SetColumn(actions, 2);
        grid.Children.Add(actions);
        resume.Click += async (_, _) =>
        {
            resume.IsEnabled = dismiss.IsEnabled = false;
            var response = await Call("live.resume", new { session_id = _id });
            if (!response.Ok) { SetComposerStatus(response.Error ?? "Could not resume."); resume.IsEnabled = dismiss.IsEnabled = true; return; }
            _resumeBanner.Visibility = Visibility.Collapsed;
        };
        dismiss.Click += async (_, _) =>
        {
            await Call("live.dismiss", new { session_id = _id });
            _resumeBanner.Visibility = Visibility.Collapsed;
        };
        _resumeBanner.Child = grid;
        _resumeBanner.BorderBrush = NexusPalette.Amber;
        _resumeBanner.Background = SessionChatRender.SubtleFill;
        _resumeBanner.Visibility = Visibility.Visible;
    }
}
