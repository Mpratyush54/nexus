using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;
using System.Text;
using System.Text.Json;

namespace Nexus.Pages;

// Live chat: one persistent agent process per open chat (core live.*), so the
// agent can ask for permission, be stopped mid-turn and keep its context
// between messages. Agents without a live driver keep using continue.here.
public sealed partial class SessionPage
{
    private string _liveID = "";
    private string _liveSessionID = "";
    private string _liveAgent = "";
    private string _liveModel = "";
    private string _liveWorkspace = "";
    private string _liveMode = "";
    private string _liveLastError = "";
    // Access mode, as in T3 Code's supervised/auto toggle. Only Claude Code
    // takes a permission mode; the picker is hidden for other agents.
    private readonly ComboBox _access = new()
    {
        MinWidth = 96, Visibility = Visibility.Collapsed,
        Items =
        {
            new ComboBoxItem { Content = "Ask before changes", Tag = "" },
            new ComboBoxItem { Content = "Auto-accept edits", Tag = "acceptEdits" }
        },
        SelectedIndex = 0
    };

    private string AccessMode() => SelectedHarness() == "claude" ? (_access.SelectedItem as ComboBoxItem)?.Tag as string ?? "" : "";
    private TaskCompletionSource<NxResponse>? _liveTurn;
    private readonly StringBuilder _liveText = new();
    private readonly Dictionary<string, string> _liveTools = new(StringComparer.Ordinal);
    private readonly Dictionary<string, (string Input, string Parent)> _liveToolInfo = new(StringComparer.Ordinal);
    private readonly SemaphoreSlim _approvalGate = new(1, 1);
    // Streaming render throttle: re-laying out a long reply on every delta
    // made the UI stutter and look frozen. First delta renders immediately,
    // later ones at most every ~66 ms.
    private DispatcherQueueTimer? _liveRenderTimer;
    private bool _liveDirty;

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

    // Agents with a persistent driver in the core (live.*). Claude Code uses its
    // stream-json SDK protocol; Cursor, OpenCode, Gemini and Kimi speak ACP;
    // Codex uses its app-server.
    private string LiveAgentName => ChatTitles.AgentName(_liveAgent);

    private static bool SupportsLive(string agent) => agent is "claude" or "cursor" or "opencode" or "gemini" or "kimi" or "codex";

    // Sends one message through the live session and completes once the core
    // has recorded the finished turn (same result shape as continue.here).
    private async Task<NxResponse> SendLiveAsync(string sessionID, string agent, string model, string prompt, string context, string resumeID, string workspace, string[]? images = null)
    {
        var mode = AccessMode();
        if (_liveID.Length > 0 && (_liveSessionID != sessionID || _liveAgent != agent || _liveModel != model || _liveWorkspace != workspace || _liveMode != mode))
            await CloseLiveAsync();
        if (_liveID.Length == 0)
        {
            SetComposerStatus("Starting " + ChatTitles.AgentName(agent) + "…");
            var started = await Call("live.start", new { session_id = sessionID, project_id = _projectID, agent, workspace_root = workspace, resume_id = resumeID, model, context, permission_mode = mode });
            if (!started.Ok || started.Result is not { } result) return started;
            _liveID = NxJson.Text(result, "live_id");
            (_liveSessionID, _liveAgent, _liveModel, _liveWorkspace, _liveMode) = (sessionID, agent, model, workspace, mode);
        }
        _liveText.Clear();
        _liveTools.Clear();
        _liveToolInfo.Clear();
        var turn = new TaskCompletionSource<NxResponse>(TaskCreationOptions.RunContinuationsAsynchronously);
        _liveTurn = turn;
        var sent = await Call("live.send", new { live_id = _liveID, text = prompt, images = images ?? Array.Empty<string>() });
        if (!sent.Ok)
        {
            _liveTurn = null;
            _liveID = "";
            return sent;
        }
        SetComposerStatus("Waiting for " + ChatTitles.AgentName(agent) + "…");
        var outcome = await turn.Task;
        var problem = outcome.Error ?? "";
        if (!outcome.Ok && resumeID.Length > 0 &&
            (problem.Contains("not found", StringComparison.OrdinalIgnoreCase) || problem.Contains("No conversation", StringComparison.OrdinalIgnoreCase)))
        {
            // The agent no longer knows that session (e.g. Claude Code exits on
            // an unknown --resume id). Continue once in a fresh session.
            _localNativeID = "";
            await CloseLiveAsync();
            SetComposerStatus("Starting a fresh " + ChatTitles.AgentName(agent) + " session with the conversation so far…");
            return await SendLiveAsync(sessionID, agent, model, prompt, context, "", workspace, images);
        }
        return outcome;
    }

    private async Task InterruptLiveAsync()
    {
        if (_liveID.Length == 0) return;
        var response = await Call("live.interrupt", new { live_id = _liveID });
        if (!response.Ok) SetComposerStatus(response.Error ?? "Could not stop this reply.");
    }

    private async Task CloseLiveAsync()
    {
        var id = _liveID;
        _liveID = "";
        _liveTurn?.TrySetResult(new NxResponse(false, "The chat was closed.", null));
        _liveTurn = null;
        if (id.Length > 0) await Core.CallAsync("live.close", new { live_id = id });
    }

    // Returns true when the event was a live event (handled or ignored here).
    private bool HandleLiveEvent(string json)
    {
        if (!json.Contains("\"type\":\"live.", StringComparison.Ordinal)) return false;
        try
        {
            using var doc = JsonDocument.Parse(json);
            var root = doc.RootElement;
            if (NxJson.Text(root, "live_id") != _liveID || _liveID.Length == 0) return true;
            if (NxJson.Text(root, "type") == "live.recorded")
            {
                var turn = _liveTurn;
                _liveTurn = null;
                if (root.TryGetProperty("result", out var result) && result.ValueKind == JsonValueKind.Object)
                    turn?.TrySetResult(new NxResponse(true, null, result.Clone()));
                else
                    turn?.TrySetResult(new NxResponse(false, NxJson.Text(root, "error") is { Length: > 0 } e ? AppDiagnostics.Friendly("live", e) : "The reply could not be saved.", null));
                return true;
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
                    _ = AskApprovalAsync(_liveID, NxJson.Text(root, "request_id"), NxJson.Text(root, "tool_name"), NxJson.Text(root, "tool_input"));
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
                    _liveID = "";
                    var why = _liveLastError.Length > 0 ? _liveLastError + " · Send again to restart." : "The agent stopped. Send again to restart it.";
                    _liveLastError = "";
                    pending?.TrySetResult(new NxResponse(false, why, null));
                    break;
            }
        }
        catch (JsonException) { }
        return true;
    }

    private void ShowLiveText()
    {
        if (_streamText is null) return;
        var text = _liveText.ToString().TrimEnd();
        if (text.Length == 0) return;
        _streamHasText = true;
        var nearBottom = _chatScroll.ScrollableHeight - _chatScroll.VerticalOffset < 120;
        // Rendered as Markdown while it arrives; the saved reply keeps this view.
        StreamView(_streamText).Update(_liveText.ToString(), streaming: true);
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
                "memory_search" => "Memory · searched" + (query.Length > 0 ? " \u201c" + query + "\u201d" : ""),
                "memory_write" => "Memory · saved" + (query.Length > 0 ? " \u201c" + query + "\u201d" : ""),
                "project_knowledge" => "Memory · read project knowledge",
                "session_fetch" or "session_summary_get" => "Memory · read an earlier session",
                _ => "Memory · " + name.Replace('_', ' ')
            };
        }
        if (mcp is { Length: 2 }) return mcp[0] + " · " + mcp[1].Replace('_', ' ');
        return "";
    }

    private async Task AskApprovalAsync(string liveID, string requestID, string tool, string input)
    {
        if (requestID.Length == 0) return;
        await _approvalGate.WaitAsync();
        try
        {
            var details = new StackPanel { Spacing = 10 };
            details.Children.Add(new TextBlock
            {
                Text = (_liveAgent.Length > 0 ? LiveAgentName : "The agent") + " wants to use " + (tool.Length > 0 ? tool : "a tool") + " in " + _liveWorkspace + ".",
                TextWrapping = TextWrapping.Wrap
            });
            var preview = PrettyInput(input);
            if (preview.Length > 0) details.Children.Add(SessionChatRender.CodeBlock(preview));
            var dialog = new ContentDialog
            {
                XamlRoot = XamlRoot,
                Title = "Allow " + (tool.Length > 0 ? tool : "this action") + "?",
                Content = new ScrollViewer { Content = details, MaxHeight = 420 },
                PrimaryButtonText = "Allow",
                CloseButtonText = "Deny",
                DefaultButton = ContentDialogButton.Primary
            };
            SetComposerStatus("Waiting for your approval…");
            var choice = ContentDialogResult.None;
            try { choice = await dialog.ShowThemedAsync(); }
            catch (Exception) { /* another dialog is open: treat as deny */ }
            var allow = choice == ContentDialogResult.Primary;
            var response = await Call("live.approve", new { live_id = liveID, request_id = requestID, allow });
            SetComposerStatus(!response.Ok ? response.Error ?? "Could not send your answer." : allow ? "Allowed · " + tool : "Denied · " + tool);
        }
        finally { _approvalGate.Release(); }
    }

    private static string PrettyInput(string input)
    {
        if (input.Length == 0) return "";
        try
        {
            using var doc = JsonDocument.Parse(input);
            var pretty = JsonSerializer.Serialize(doc.RootElement, new JsonSerializerOptions { WriteIndented = true });
            return pretty.Length <= 4000 ? pretty : pretty[..4000] + "\n…";
        }
        catch (JsonException) { return input.Length <= 4000 ? input : input[..4000] + "\n…"; }
    }
}
