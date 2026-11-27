using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Shapes;
using Nexus.Core;
using System.Text.Json;

namespace Nexus.Pages;

// The safety net for agent edits. Before each turn the core snapshots the
// project folder (internal/checkpoint), so:
//  - every message you sent from Nexus gets "Restore to before this
//    message" (action row and right-click): a confirm sheet lists the files
//    that will change, the files go back, the chat hides what came after and
//    shows a "Restored checkpoint" divider, and a toast offers Undo. The
//    agent's own conversation continues as it was.
//  - a turn that changed files gets a "Review changes" card (ReviewChangesView).
// RewindToMessageAsync is the entry point the /rewind command uses.
public sealed partial class SessionPage
{
    private CheckpointIndex _checkpoints = new();
    private string _checkpointsFor = "";
    private bool _checkpointRefreshQueued;
    private const string RewindDividerTag = "nexus-rewind-divider";

    /// Reads the chat's checkpoints once the transcript has painted: Restore
    /// becomes available, restores hide their messages, review cards appear.
    private void QueueCheckpointRefresh()
    {
        if (_checkpointRefreshQueued) return;
        _checkpointRefreshQueued = true;
        DispatcherQueue.TryEnqueue(Microsoft.UI.Dispatching.DispatcherQueuePriority.Low, () =>
        {
            _checkpointRefreshQueued = false;
            _ = LoadCheckpointsAsync();
        });
    }

    private async Task LoadCheckpointsAsync()
    {
        var sessionID = _id;
        if (sessionID.Length == 0) return;
        NxResponse response;
        try { response = await Call("live.checkpoints", new { session_id = sessionID }); }
        catch (Exception) { return; }
        if (_id != sessionID || !response.Ok || response.Result is not { } result) return;
        _checkpoints = CheckpointIndex.Parse(result);
        _checkpointsFor = sessionID;
        ApplyRewinds();
        // Restorable messages get their action row built, so Restore is
        // reachable without hovering (assistive tech, keyboard).
        foreach (var (key, anchor) in _messageAnchors)
            if (key.EndsWith(":user", StringComparison.Ordinal) && TurnIdxFromKey(key) is { } idx && _checkpoints.CanRestore(idx) &&
                s_messages.TryGetValue(anchor, out var info) && info.Realized)
                info.Actions.Prepare();
        // Review cards for the latest turns that changed files.
        foreach (var item in _checkpoints.Reviewable.TakeLast(12))
            PlaceReviewCard(item.Id, item.Changed, item.UserIdx);
    }

    // /rewind (SessionPage.Pickers): offered once this chat has a message
    // that can still be restored; it restores to before the latest one.
    partial void ComposerCanRewind(ref bool available) =>
        available = _checkpointsFor == _id && _id.Length > 0 && _checkpoints.Items.Any(i => !i.Rewound && i.UserIdx > 0);

    partial void ComposerRewind(ref bool handled)
    {
        _ = RewindToMessageAsync();
        handled = true;
    }

    private bool CanRestoreMessage(UIElement message) =>
        _checkpointsFor == _id && TurnIdxFromKey(MessageKey(message)) is { } idx && _checkpoints.CanRestore(idx);

    private void AddRestoreMenuItem(MenuFlyout menu, UIElement message)
    {
        var item = new MenuFlyoutItem { Text = "Restore to before this message", Icon = new FontIcon { Glyph = "", FontFamily = WorkLogVisuals.IconFont } };
        item.Click += async (_, _) => await RestoreFromMessageAsync(message);
        menu.Items.Add(item);
        menu.Opening += (_, _) => item.Visibility = CanRestoreMessage(message) ? Visibility.Visible : Visibility.Collapsed;
    }

    private Task RestoreFromMessageAsync(UIElement message)
    {
        var idx = TurnIdxFromKey(MessageKey(message));
        if (idx is null)
        {
            _notice.Text = "This message is still being saved. Try Restore again in a moment.";
            return Task.CompletedTask;
        }
        return RewindToMessageAsync(idx.Value.ToString(System.Globalization.CultureInfo.InvariantCulture));
    }

    /// <summary>
    /// Restore to before a message: the files the agent changed from that
    /// message on go back, and the chat goes back to that point. messageId
    /// is the saved user message index (the "turn:&lt;idx&gt;:user" key's idx),
    /// a checkpoint id, or null/"" for the latest message that has a
    /// checkpoint (/rewind). Shows the confirm sheet first.
    /// </summary>
    internal async Task RewindToMessageAsync(string? messageId = null)
    {
        var sessionID = _id;
        if (sessionID.Length == 0) return;
        if (LiveTurnActive || _sendInFlight)
        {
            Toast.Show("Wait for this reply to finish, or press Stop, then restore.", key: "rewind");
            return;
        }
        var preview = await Call("live.rewind.preview", new { session_id = sessionID, message_id = messageId ?? "" });
        if (_id != sessionID) return;
        if (!preview.Ok || preview.Result is not { } planJson)
        {
            Toast.Show(preview.Error ?? "Nexus couldn't read that checkpoint.", key: "rewind");
            return;
        }
        var plan = RewindPlanView.Parse(planJson);
        if (!await ConfirmRewindAsync(plan) || _id != sessionID) return;
        var done = await Call("live.rewind", new { session_id = sessionID, message_id = plan.CheckpointId });
        if (_id != sessionID) return;
        if (!done.Ok || done.Result is not { } result)
        {
            Toast.Show(done.Error ?? "Nexus couldn't restore those files.", key: "rewind");
            return;
        }
        var failed = result.TryGetProperty("failed", out var f) && f.ValueKind == JsonValueKind.Array ? f.GetArrayLength() : 0;
        var rewind = result.TryGetProperty("rewind", out var rw) ? rw : default;
        var undoID = NxJson.Text(rewind, "undo_id");
        await LoadCheckpointsAsync();
        RefreshConversationNavigation();
        var restored = result.TryGetProperty("restored", out var r) && r.ValueKind == JsonValueKind.Array ? r.GetArrayLength() : 0;
        var message = restored switch { 0 => "Went back to before your message", 1 => "Restored 1 file to before your message", _ => "Restored " + restored + " files to before your message" };
        if (failed > 0) message += " · " + failed + " couldn't be restored";
        Toast.Show(message, onUndo: undoID.Length == 0 ? null : () => _ = UndoRewindAsync(sessionID, undoID), key: "rewind");
    }

    private async Task UndoRewindAsync(string sessionID, string undoID)
    {
        var response = await Call("live.rewind.undo", new { session_id = sessionID, undo_id = undoID });
        if (_id != sessionID) return;
        if (!response.Ok)
        {
            Toast.Show(response.Error ?? "Nexus couldn't undo the restore.", key: "rewind");
            return;
        }
        // The hidden messages come back with a fresh render.
        await RevalidateAsync();
        Toast.Show("Undid the restore", key: "rewind");
    }

    /// The confirm sheet: which files change and how.
    private async Task<bool> ConfirmRewindAsync(RewindPlanView plan)
    {
        if (XamlRoot is null) return false;
        var body = new StackPanel { Spacing = 12, MaxWidth = 520 };
        body.Children.Add(new TextBlock { Text = plan.Lead, TextWrapping = TextWrapping.Wrap, FontSize = 13.5 });
        if (plan.Rows.Count > 0)
        {
            var list = new StackPanel { Spacing = 2 };
            foreach (var row in plan.Rows.Take(40))
            {
                var line = new Grid { ColumnSpacing = 10, Padding = new Thickness(8, 5, 8, 5) };
                line.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
                line.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
                var glyph = !row.CanRestore ? "" : row.Action == "delete" ? "" : row.Action == "recreate" ? "" : "";
                line.Children.Add(new FontIcon
                {
                    Glyph = glyph, FontFamily = WorkLogVisuals.IconFont, FontSize = 13, VerticalAlignment = VerticalAlignment.Top, Margin = new Thickness(0, 3, 0, 0),
                    Foreground = !row.CanRestore ? NexusPalette.Amber : row.Action == "delete" ? NexusPalette.DiffRemoved : row.Action == "recreate" ? NexusPalette.DiffAdded : NexusPalette.Accent,
                });
                var words = new StackPanel { Spacing = 1 };
                var path = SessionChatRender.Mono(row.Path, 12.5);
                path.TextTrimming = TextTrimming.CharacterEllipsis;
                words.Children.Add(path);
                words.Children.Add(new TextBlock { Text = row.Label, FontSize = 11.5, Foreground = NexusPalette.Muted, TextWrapping = TextWrapping.Wrap });
                Grid.SetColumn(words, 1);
                line.Children.Add(words);
                AutomationProperties.SetName(line, row.Path + ": " + row.Label);
                list.Children.Add(line);
            }
            if (plan.Rows.Count > 40)
                list.Children.Add(new TextBlock { Text = "and " + (plan.Rows.Count - 40) + " more", FontSize = 12, Foreground = NexusPalette.Muted, Margin = new Thickness(8, 4, 0, 0) });
            body.Children.Add(new Border
            {
                Child = new ScrollViewer { Content = list, MaxHeight = 300 },
                BorderThickness = new Thickness(1), BorderBrush = SessionChatRender.SubtleBorder, CornerRadius = new CornerRadius(8),
                Background = SessionChatRender.CodeFill,
            });
        }
        body.Children.Add(new TextBlock
        {
            Text = "You can undo this right after.", FontSize = 12, Foreground = NexusPalette.Muted, TextWrapping = TextWrapping.Wrap,
        });
        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot, Title = plan.Title, Content = body,
            PrimaryButtonText = plan.ConfirmLabel, CloseButtonText = "Cancel", DefaultButton = ContentDialogButton.Primary,
        };
        AutomationProperties.SetName(dialog, plan.Title);
        try { return await dialog.ShowAsync() == ContentDialogResult.Primary; }
        catch (Exception) { return false; } // another dialog is open
    }

    /// Hides the messages each restore went back over and puts a divider
    /// where the chat continues.
    private void ApplyRewinds()
    {
        if (_checkpoints.Rewinds.Count == 0) return;
        var changed = false;
        foreach (var rewind in _checkpoints.Rewinds.OrderBy(r => r.At))
        {
            var tag = RewindDividerTag + ":" + rewind.UndoId + ":" + rewind.CutoffIdx;
            var elements = ChatElementOrders();
            if (ChatRewinds.Range(elements, rewind) is not { } range) continue;
            for (var i = range.Start; i < range.End; i++)
            {
                var element = _chat.Children[i];
                if (element is FrameworkElement { Tag: string t } && t.StartsWith(RewindDividerTag, StringComparison.Ordinal)) continue;
                if (element.Visibility == Visibility.Collapsed) continue;
                element.Visibility = Visibility.Collapsed;
                changed = true;
                if (elements[i].Key is { } key) _conversation.Remove(key);
            }
            if (_chat.Children.OfType<FrameworkElement>().Any(e => e.Tag as string == tag)) continue;
            var divider = RewindDivider(rewind, tag);
            _chat.Children.Insert(range.End, divider);
            Motion.FadeIn(divider, rise: 4);
            changed = true;
        }
        // Restores done back to back: one divider, not a stack of them.
        var stacked = ChatRewinds.StackedDividers(_chat.Children
            .Select(e => (e is FrameworkElement { Tag: string t } && t.StartsWith(RewindDividerTag, StringComparison.Ordinal), e.Visibility == Visibility.Visible))
            .ToList());
        foreach (var i in stacked) { _chat.Children[i].Visibility = Visibility.Collapsed; changed = true; }
        if (changed) RefreshConversationNavigation();
    }

    // Each chat element's message key and order (turn index), if it is a message.
    private List<(string? Key, long? Order)> ChatElementOrders()
    {
        var byElement = new Dictionary<UIElement, string>(ReferenceEqualityComparer.Instance);
        foreach (var (key, anchor) in _messageAnchors) byElement[anchor] = key;
        var list = new List<(string? Key, long? Order)>(_chat.Children.Count);
        foreach (var element in _chat.Children)
        {
            if (byElement.TryGetValue(element, out var key))
            {
                // A message still being sent ("live:…") is always after any restore.
                long? order = key.StartsWith("live:", StringComparison.Ordinal) ? long.MaxValue : TurnIdxFromKey(key) ?? _conversation.Find(key)?.Order;
                list.Add((key, order));
            }
            else list.Add((null, null));
        }
        return list;
    }

    private static FrameworkElement RewindDivider(ChatRewind rewind, string tag)
    {
        var grid = new Grid { ColumnSpacing = 10, Margin = new Thickness(8, 10, 8, 14), Tag = tag };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        Rectangle Line() => new() { Height = 1, Fill = NexusPalette.Hairline, VerticalAlignment = VerticalAlignment.Center };
        var left = Line();
        grid.Children.Add(left);
        var label = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6, VerticalAlignment = VerticalAlignment.Center };
        label.Children.Add(new FontIcon { Glyph = "", FontFamily = WorkLogVisuals.IconFont, FontSize = 11, Foreground = NexusPalette.Muted });
        var text = ChatRewinds.Divider(rewind);
        var when = rewind.At > DateTimeOffset.MinValue ? rewind.At.ToLocalTime().ToString("t", System.Globalization.CultureInfo.CurrentCulture) : "";
        label.Children.Add(new TextBlock { Text = text + (when.Length > 0 ? " · " + when : ""), FontSize = 11.5, Foreground = NexusPalette.Muted });
        Grid.SetColumn(label, 1);
        grid.Children.Add(label);
        var right = Line();
        Grid.SetColumn(right, 2);
        grid.Children.Add(right);
        AutomationProperties.SetName(grid, text);
        ToolTipService.SetToolTip(grid, "Files went back to before this point. The agent still remembers the earlier messages.");
        return grid;
    }

    // ── Review card ──────────────────────────────────────────────────────

    /// The card for a turn that just finished here (null when the turn has
    /// no checkpoint or changed nothing). Its message becomes restorable.
    private FrameworkElement? ReviewCardForTurn(JsonElement result)
    {
        var id = NxJson.Text(result, "checkpoint_id");
        if (id.Length == 0) return null;
        var files = ChangeReview.Int(result, "checkpoint_files");
        var userIdx = long.TryParse(NxJson.Text(result, "user_idx"), out var u) ? u : ChangeReview.Long(result, "user_idx");
        if (_checkpointsFor != _id) { _checkpoints = new CheckpointIndex(); _checkpointsFor = _id; }
        _checkpoints.Note(id, userIdx, files);
        QueueCheckpointRefresh();
        if (files <= 0) return null;
        var view = new ReviewChangesView(id, files, Call);
        ReviewIds.AddOrUpdate(view.View, id);
        _ = view.LoadAsync();
        Motion.FadeIn(view.View, rise: 6);
        return view.View;
    }

    /// A saved turn's card goes after its reply (and replaces the read-only
    /// "Files changed" list there). Nothing happens when the reply isn't on
    /// screen or the card is already there.
    private void PlaceReviewCard(string checkpointId, int files, long userIdx)
    {
        if (_chat.Children.OfType<FrameworkElement>().Any(e => e.Tag as string == ReviewChangesView.ReviewTag && ReviewIds.TryGetValue(e, out var cid) && cid == checkpointId))
            return;
        var answerKey = "turn:" + (userIdx + 1).ToString(System.Globalization.CultureInfo.InvariantCulture);
        if (!_messageAnchors.TryGetValue(answerKey, out var anchor)) return;
        var at = _chat.Children.IndexOf(anchor);
        if (at < 0 || anchor.Visibility == Visibility.Collapsed) return;
        var messages = new HashSet<UIElement>(_messageAnchors.Values, ReferenceEqualityComparer.Instance);
        var insert = at + 1;
        Expander? replaced = null;
        // Past the reply's own steps and file list, up to the next message.
        while (insert < _chat.Children.Count && !messages.Contains(_chat.Children[insert]))
        {
            var next = _chat.Children[insert];
            if (next is FrameworkElement { Tag: string t } && (t == ReviewChangesView.ReviewTag || t.StartsWith(RewindDividerTag, StringComparison.Ordinal))) break;
            if (next is Expander { Header: string h } ex && h.StartsWith("Files changed", StringComparison.Ordinal)) { replaced = ex; break; }
            insert++;
        }
        var view = new ReviewChangesView(checkpointId, files, Call);
        ReviewIds.AddOrUpdate(view.View, checkpointId);
        if (replaced is not null)
        {
            var index = _chat.Children.IndexOf(replaced);
            _chat.Children.RemoveAt(index);
            _chat.Children.Insert(index, view.View);
        }
        else _chat.Children.Insert(insert, view.View);
        _ = view.LoadAsync();
    }

    private static readonly System.Runtime.CompilerServices.ConditionalWeakTable<FrameworkElement, string> ReviewIds = new();
}
