using System.Text.Json;
using System.Text.RegularExpressions;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed partial class SessionPage
{
    private LiveActivityView? _streamActivity;
    private Border? _streamFileChanges;

    // Live tool events (and replayed saved Nexus activity) feed the same
    // work log a captured transcript turn renders into.
    private sealed class LiveActivityView
    {
        private readonly LiveActivityLog _log = new();
        public WorkLogView View { get; } = new(live: true, expanded: true);
        public int Count => _log.Count;

        public void Apply(string json)
        {
            if (!_log.Apply(json) || _log.Latest is not { } activity) return;
            var step = StepFromActivity(activity);
            if (step.Kind == StepKind.Agent)
            {
                // Saved and live sub-agent steps get "Open sub-agent chat" too;
                // the page is found from the log when the step is opened.
                var view = View;
                var spawned = new ChildSessionEntry("", step.Target, activity.Tool);
                var description = JsonField(activity.Input, "description");
                step.Extra = () => OwnerOf(view)?.SubagentActions(spawned, description);
            }
            View.Upsert(step);
        }
    }

    private static SessionPage? OwnerOf(DependencyObject? node)
    {
        for (; node is not null; node = Microsoft.UI.Xaml.Media.VisualTreeHelper.GetParent(node))
            if (node is SessionPage page) return page;
        return null;
    }

    private static string JsonField(string json, string name)
    {
        if (!json.TrimStart().StartsWith('{')) return "";
        try
        {
            using var doc = System.Text.Json.JsonDocument.Parse(json);
            return NxJson.Text(doc.RootElement, name);
        }
        catch (System.Text.Json.JsonException) { return ""; }
    }

    // ── Harness markup inside turns ──────────────────────────────────────
    // Codex imports another agent's chat with each tool step written as text
    // ([external_agent_tool_call: …] / [external_agent_tool_result]); those
    // become work-log steps. A sub-agent's hand-back or a task notification
    // arrives as a "user" turn; it becomes a collapsed card, not the person's
    // bubble. Returns null for an ordinary turn, which RenderTurn shows.
    private bool? RenderHarnessTurn(JsonElement item, string role, List<ParsedToolCall> toolCalls)
    {
        var raw = NxJson.Text(item, "text_preview", "text", "content", "message");
        var markers = ExternalToolMarkers.Contains(raw);
        if (!markers && !HarnessEnvelope.Contains(raw)) return null;
        var (envelopes, rest) = HarnessEnvelope.Extract(raw);
        if (!markers && envelopes.Count == 0) return null;
        var rendered = false;
        foreach (var envelope in envelopes)
        {
            // Each hand-back / notification is recorded twice by the harness.
            if (!_shownEnvelopes.Add(envelope.Fingerprint)) continue;
            FlushTurnActivity();
            AddChatElement(SessionChatRender.EnvelopeCard(envelope));
            rendered = true;
        }
        var part = 0;
        foreach (var segment in ExternalToolMarkers.Split(rest))
        {
            switch (segment.Kind)
            {
                case ExternalSegmentKind.Call:
                    AddCapturedStep(new ParsedToolCall(segment.Tool, segment.Input, ""), dedupe: false);
                    rendered = true;
                    break;
                case ExternalSegmentKind.Result:
                    // Results follow their calls in order; an empty one still
                    // closes its call so later results land on the right step.
                    AttachToolResult(segment.Text, "", segment.IsError);
                    rendered = true;
                    break;
                default:
                    rendered |= RenderProse(item, role, segment.Text, part++);
                    break;
            }
        }
        foreach (var call in toolCalls)
        {
            AddCapturedStep(call);
            rendered = true;
        }
        return rendered;
    }

    // One piece of a turn's prose as a chat message (the same treatment
    // RenderTurn gives a whole turn).
    private bool RenderProse(JsonElement item, string role, string raw, int part)
    {
        var parsed = role.Equals("user", StringComparison.OrdinalIgnoreCase) ? ReadableUserMessage(raw) : null;
        var text = parsed?.Text ?? ReadableTurn(raw);
        var extras = parsed is { } p && (p.Attachments.Count > 0 || p.Context.Count > 0) ? p : null;
        if (text.Length == 0 && !(extras?.Attachments.Count > 0)) return false;
        FlushTurnActivity();
        // A prompt stored again with a queued notification wrapped around it
        // comes through here, not RenderTurn: the same duplicate rules apply.
        var signature = TranscriptDedupe.Signature(role, text + (extras is null ? "" : "\n" + string.Join("\n", extras.Attachments.Select(a => a.Path))));
        if (!AdmitTurn(item, signature, out var superseded)) return true;
        var agent = role.Equals("assistant", StringComparison.OrdinalIgnoreCase) &&
            _localAgentByTurnIndex.TryGetValue(NxJson.Text(item, "idx"), out var local) ? local : null;
        var element = IndexedChatMessage(role, text, TurnKey(item) + (part == 0 ? "" : ":" + part), agent, extras);
        AddChatElement(element);
        ShownTurn(item, signature, element, superseded);
        return true;
    }

    // Captured turns carry the time the harness recorded them; a row stored
    // again by a later import has none (see TranscriptDedupe).
    private static bool IsSourcedTurn(JsonElement item) => NxJson.Text(item, "source_ts").Length > 0;

    private bool AdmitTurn(JsonElement item, string signature, out object? superseded)
    {
        // A batch being rendered (e.g. an older page to prepend) only
        // compares adjacency against its own elements, never the chat's bottom.
        var last = _chatRenderSink is { } sink ? (sink.Count > 0 ? sink[^1] : null) : _chat.Children.Count > 0 ? _chat.Children[^1] : null;
        return _turnDedupe.Admit(signature, IsSourcedTurn(item), last, out superseded);
    }

    private void ShownTurn(JsonElement item, string signature, UIElement element, object? superseded)
    {
        _turnDedupe.Shown(signature, IsSourcedTurn(item), element);
        // An older page brought the original of a copy already on screen.
        if (superseded is not UIElement copy) return;
        if (!(_chatRenderSink?.Remove(copy) ?? false)) _chat.Children.Remove(copy);
        foreach (var key in _messageAnchors.Where(a => ReferenceEquals(a.Value, copy)).Select(a => a.Key).ToList())
            _messageAnchors.Remove(key);
    }

    // ── Captured transcript turns ────────────────────────────────────────
    // Tool calls between two pieces of prose collect here and render as one
    // work log when the next prose (or the end of the page) arrives.
    private readonly List<WorkStep> _turnSteps = new();
    private int _turnStepSeq;
    // Envelopes already on screen (cleared with the chat), see HarnessEnvelope.Fingerprint.
    private readonly HashSet<string> _shownEnvelopes = new(StringComparer.Ordinal);

    // dedupe: off for calls whose results follow as separate text blocks (a
    // skipped call would hand its result to the wrong step).
    private void AddCapturedStep(ParsedToolCall call, bool dedupe = true)
    {
        var step = DescribeStep(call.Name, call.ArgsSummary, call.Result);
        step.Id = "turn-step:" + ++_turnStepSeq;
        step.AwaitingResult = call.Result.Length == 0;
        MarkOutcome(step);
        if (step.Kind == StepKind.Run)
        {
            var command = ExtractCommandFromTool(call);
            if (command.Length == 0) command = step.Command;
            var key = CommandKey(command);
            if (key.Length > 0 && !_shownCommandKeys.Add(key) && dedupe) return;
            if (TakeMatchingCommandOp(command, call) is { } op)
            {
                if (int.TryParse(NxJson.Text(op, "exit_code", "exitCode", "code"), out var exit))
                {
                    step.ExitCode = exit;
                    if (exit != 0) step.Status = "error";
                }
                if (step.Output.Length == 0 && NxJson.Text(op, "output", "output_snippet", "stdout", "stderr") is { Length: > 0 } opOutput)
                {
                    step.Output = opOutput;
                    step.AwaitingResult = false;
                }
            }
        }
        else if (step.Kind == StepKind.Agent)
        {
            // Each run is its own card in the transcript (SessionPage.Subagents.cs),
            // not a row buried in the collapsed work log.
            if (TryResolveSubagentFromTool(call) is { Id.Length: > 0 } spawned) _inlineSubagentIds.Add(spawned.Id);
            AddSubagentCard(call);
            return;
        }
        _turnSteps.Add(step);
    }

    // The sub-agent step's detail: its chip (preview dialog) and a direct
    // "Open sub-agent chat". Captured Claude Code runs are not linked to the
    // parent on the server, so the chat is found on click from the local
    // transcript folders (SubagentRuns).
    private static string JsonUnquote(string raw)
    {
        try { return System.Text.Json.JsonSerializer.Deserialize<string>("\"" + raw + "\"") ?? raw; }
        catch (System.Text.Json.JsonException) { return raw; }
    }

    private UIElement SubagentActions(ChildSessionEntry spawned, string description)
    {
        var row = new StackPanel { Spacing = 6, Margin = new Thickness(0, 2, 0, 0) };
        if (spawned.Id.Length > 0) row.Children.Add(InlineSubagentChip(spawned));
        var open = new HyperlinkButton { Content = "Open sub-agent chat", FontSize = 12, Padding = new Thickness(0), MinHeight = 0 };
        var note = new TextBlock { FontSize = 11.5, Foreground = NexusPalette.Muted, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };
        open.Click += async (_, _) =>
        {
            open.IsEnabled = false;
            var id = spawned.Id;
            if (id.Length == 0)
            {
                try { id = await SubagentRuns.FindChildChatAsync(App.Core, _projectID, _id, _nativeID, description); }
                catch { id = ""; }
                await UiThread.Resume(DispatcherQueue);
            }
            open.IsEnabled = true;
            if (id.Length > 0)
            {
                MainWindow.Current?.OpenSession(id);
                return;
            }
            note.Text = "This sub-agent's chat has not been captured yet. Sub-agent runs are listed under Agents → Sub-agent runs once captured.";
            note.Visibility = Visibility.Visible;
        };
        row.Children.Add(open);
        row.Children.Add(note);
        return row;
    }

    // A tool-role message: its output belongs to the call that asked for it.
    private void AttachToolResult(string result, string toolName, bool failed = false)
    {
        var step = _turnSteps.FirstOrDefault(s => s.AwaitingResult && s.Tool.Equals(toolName, StringComparison.OrdinalIgnoreCase))
            ?? _turnSteps.FirstOrDefault(s => s.AwaitingResult);
        if (step is null)
        {
            if (result.Length == 0) return;
            step = new WorkStep
            {
                Id = "turn-step:" + ++_turnStepSeq, Tool = toolName, Kind = StepKind.Tool,
                Verb = "Tool output", Target = toolName is "tool" or "" ? "" : toolName
            };
            _turnSteps.Add(step);
        }
        step.Output = result;
        step.AwaitingResult = false;
        if (failed) step.Status = "error";
        MarkOutcome(step);
    }

    private void FlushTurnActivity()
    {
        if (_turnSteps.Count == 0) return;
        var log = new WorkLogView(live: false, expanded: false);
        foreach (var step in _turnSteps) log.Upsert(step);
        AddChatElement(log);
        _chatCommandsShown += _turnSteps.Count;
        _turnSteps.Clear();
    }
}
