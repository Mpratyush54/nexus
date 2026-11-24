using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

// Sub-agent runs in a chat: each Task/Agent call is a SubagentCard in the
// transcript, and the header's "Sub-agents (n)" lists every run of the chat
// (including ones on pages not loaded yet).
public sealed partial class SessionPage
{
    private readonly List<SubagentCard> _subagentCards = new();
    private readonly Button _subagentsButton = new() { Visibility = Visibility.Collapsed };
    private int _subagentListVersion;

    private static (string Type, string Description, string Prompt) SubagentArgs(string argsJson)
    {
        try
        {
            using var doc = JsonDocument.Parse(argsJson);
            var root = doc.RootElement;
            return (NxJson.Text(root, "subagent_type", "agent_type", "agent", "name"),
                NxJson.Text(root, "description", "task"), NxJson.Text(root, "prompt", "message", "task"));
        }
        catch (JsonException) { return ("", "", argsJson); }
    }

    private void AddSubagentCard(ParsedToolCall call)
    {
        var (type, description, prompt) = SubagentArgs(call.ArgsSummary);
        // Steps before the run stay in their own work log above it.
        FlushTurnActivity();
        var card = new SubagentCard(_projectID, _id, _nativeID, type, description, prompt);
        _subagentCards.Add(card);
        AddChatElement(card);
        _chatCommandsShown++;
    }

    private void InitSubagentsButton()
    {
        _subagentsButton.Content = new StackPanel
        {
            Orientation = Orientation.Horizontal, Spacing = 5,
            Children = { new FontIcon { Glyph = "", FontSize = 13 }, new TextBlock { FontSize = 12, VerticalAlignment = VerticalAlignment.Center } }
        };
        ToolTipService.SetToolTip(_subagentsButton, "Sub-agents this chat started");
        _subagentsButton.Click += async (_, _) => await ShowSubagentListAsync();
    }

    // After each paint: the count covers runs on this computer for this chat
    // and the cards on screen (runs captured elsewhere have cards only).
    private async Task RefreshSubagentCountAsync()
    {
        var version = ++_subagentListVersion;
        var sessionId = _id;
        List<SubagentRuns.LocalRun> runs;
        try { runs = await SubagentLinks.RunsForParentAsync(sessionId, _nativeID); }
        catch { runs = new(); }
        await UiThread.Resume(DispatcherQueue);
        if (version != _subagentListVersion || sessionId != _id) return;
        var count = Math.Max(runs.Count, _subagentCards.Count);
        // Icon and count keep the header narrow so the chat title has room.
        if (((StackPanel)_subagentsButton.Content).Children[1] is TextBlock label) label.Text = count.ToString();
        ToolTipService.SetToolTip(_subagentsButton, count == 1 ? "1 sub-agent ran in this chat" : $"{count} sub-agents ran in this chat");
        AutomationProperties.SetName(_subagentsButton, $"Sub-agents ({count})");
        _subagentsButton.Visibility = count > 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    private async Task ShowSubagentListAsync()
    {
        List<SubagentRuns.LocalRun> runs;
        try { runs = await SubagentLinks.RunsForParentAsync(_id, _nativeID); }
        catch { runs = new(); }
        await UiThread.Resume(DispatcherQueue);
        var list = new StackPanel { Spacing = 2, MinWidth = 340, MaxWidth = 460 };
        var flyout = new Flyout { Content = new ScrollViewer { Content = list, MaxHeight = 480 }, Placement = Microsoft.UI.Xaml.Controls.Primitives.FlyoutPlacementMode.BottomEdgeAlignedRight };
        list.Children.Add(new TextBlock { Text = "SUB-AGENTS IN THIS CHAT", FontSize = 10.5, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, Opacity = 0.55, Margin = new Thickness(8, 0, 0, 6) });
        var shownCards = new HashSet<SubagentCard>();
        void AddRow(string title, string detail, Action open)
        {
            var text = new StackPanel { Spacing = 1 };
            text.Children.Add(new TextBlock { Text = NxJson.Scrub(title), FontSize = 13, TextTrimming = TextTrimming.CharacterEllipsis });
            if (detail.Length > 0) text.Children.Add(new TextBlock { Text = detail, FontSize = 11.5, Opacity = 0.6, TextTrimming = TextTrimming.CharacterEllipsis });
            var row = new Button
            {
                Content = text, HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Left,
                Background = new Microsoft.UI.Xaml.Media.SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0),
                Padding = new Thickness(8, 6, 8, 6), CornerRadius = new CornerRadius(6)
            };
            row.Click += (_, _) => { flyout.Hide(); open(); };
            list.Children.Add(row);
        }
        foreach (var run in runs)
        {
            var card = _subagentCards.FirstOrDefault(c => !shownCards.Contains(c) && c.Description.Equals(run.Description, StringComparison.OrdinalIgnoreCase));
            if (card is not null) shownCards.Add(card);
            var title = run.Description.Length > 0 ? run.Description : run.NativeId;
            var detail = (run.AgentType.Length > 0 ? WorkLogText.Humanize(run.AgentType) + " · " : "") + SubagentCard.StatusLine(run, null, null);
            var native = run.NativeId;
            AddRow(title, detail, card is not null ? card.Reveal : () => _ = OpenSubagentChatAsync(native));
        }
        foreach (var card in _subagentCards.Where(c => !shownCards.Contains(c)))
            AddRow(card.Description.Length > 0 ? card.Description : "Sub-agent run", "In this chat", card.Reveal);
        if (list.Children.Count == 1) list.Children.Add(new TextBlock { Text = "No sub-agent runs found for this chat.", Opacity = 0.7, Margin = new Thickness(8, 0, 8, 4) });
        flyout.ShowAt(_subagentsButton);
    }

    private async Task OpenSubagentChatAsync(string nativeId)
    {
        string id;
        try { id = (await SubagentRuns.ChatForNativeAsync(App.Core, _projectID, nativeId)).Id; }
        catch { id = ""; }
        await UiThread.Resume(DispatcherQueue);
        if (id.Length > 0) MainWindow.Current?.OpenSession(id);
        else SetComposerStatus("That sub-agent run has not been captured as a chat yet.");
    }
}
