using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;

namespace Nexus.Pages;

public sealed partial class SessionPage
{
    private readonly ConversationIndex _conversation = new();
    private readonly Dictionary<string, FrameworkElement> _messageAnchors = new(StringComparer.Ordinal);
    private readonly PromptNavigator _promptNavigator = new();
    private readonly TextBox _findText = new() { PlaceholderText = "Find in this chat", MinWidth = 130, HorizontalAlignment = HorizontalAlignment.Stretch };
    private readonly TextBlock _findCount = new() { FontSize = 12, Opacity = 0.7, VerticalAlignment = VerticalAlignment.Center };
    private readonly Button _findPrevious = new() { Content = "↑", MinWidth = 32 };
    private readonly Button _findNext = new() { Content = "↓", MinWidth = 32 };
    private readonly Grid _findBar = new() { ColumnSpacing = 6, Visibility = Visibility.Collapsed };
    private readonly ListView _findResults = new() { MaxHeight = 220, SelectionMode = ListViewSelectionMode.Single, Visibility = Visibility.Collapsed };
    private readonly Border _findContainer = new()
    {
        MaxWidth = 620, HorizontalAlignment = HorizontalAlignment.Right, Padding = new Thickness(10),
        CornerRadius = new CornerRadius(10), BorderThickness = new Thickness(1), Visibility = Visibility.Collapsed
    };
    private readonly Button _latest = new() { Content = "↓ Back to latest", Visibility = Visibility.Collapsed, HorizontalAlignment = HorizontalAlignment.Left };
    private IReadOnlyList<ConversationEntry> _findMatches = [];
    private int _findMatchIndex = -1;
    private string _findQuery = "";
    private bool _historyIndexed;
    private bool _indexingHistory = true;
    private bool _jumpInProgress;
    private bool _viewingHistoricalPage;
    private string _pendingFind = "";
    private DispatcherQueueTimer? _findDebounce;
    private NxResponse? _savedContinuationResponse;
    private Border? _navigationHighlight;
    private Brush? _priorHighlightBrush;
    private Thickness _priorHighlightThickness;

    private UIElement BuildConversationNavigation()
    {
        _findContainer.Background = NexusPalette.AccentFill;
        _findContainer.BorderBrush = NexusPalette.AccentBorder;
        _findContainer.Child = new StackPanel { Spacing = 6, Children = { _findBar, _findResults } };
        _findText.PlaceholderText = "Search this conversation…";
        _findCount.MinWidth = 72;
        _findCount.TextAlignment = TextAlignment.Center;
        _findResults.SelectionChanged += async (_, _) =>
        {
            if (_findResults.SelectedItem is ListViewItem { Tag: ConversationEntry entry })
            {
                _findMatchIndex = _findMatches.ToList().FindIndex(e => e.Key == entry.Key);
                _findCount.Text = $"{_findMatchIndex + 1} of {_findMatches.Count}";
                _findResults.Visibility = Visibility.Collapsed;
                await JumpToEntryAsync(entry);
            }
        };
        _findBar.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        for (var i = 0; i < 4; i++) _findBar.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var close = new Button { Content = "×", MinWidth = 32 };
        var controls = new FrameworkElement[] { _findText, _findCount, _findPrevious, _findNext, close };
        for (var i = 0; i < controls.Length; i++) { Grid.SetColumn(controls[i], i); _findBar.Children.Add(controls[i]); }
        close.Click += (_, _) => CloseFind();
        _findPrevious.Click += async (_, _) => await MoveFindAsync(-1);
        _findNext.Click += async (_, _) => await MoveFindAsync(1);
        ToolTipService.SetToolTip(_findPrevious, "Previous matching message · Shift+Enter");
        ToolTipService.SetToolTip(_findNext, "Next matching message · Enter");
        _findText.TextChanged += (_, _) =>
        {
            _findDebounce ??= DispatcherQueue.CreateTimer();
            _findDebounce.IsRepeating = false;
            _findDebounce.Interval = TimeSpan.FromMilliseconds(180);
            _findDebounce.Tick -= FindDebounce_Tick;
            _findDebounce.Tick += FindDebounce_Tick;
            _findDebounce.Stop(); _findDebounce.Start();
        };
        _findText.KeyDown += async (_, e) =>
        {
            if (e.Key == Windows.System.VirtualKey.Escape) { CloseFind(); e.Handled = true; }
            if (e.Key == Windows.System.VirtualKey.Enter)
            {
                e.Handled = true;
                _findDebounce?.Stop();
                if (!string.Equals(_findQuery, _findText.Text.Trim(), StringComparison.Ordinal))
                {
                    UpdateFindMatches();
                }
                var shift = Microsoft.UI.Input.InputKeyboardSource.GetKeyStateForCurrentThread(Windows.System.VirtualKey.Shift).HasFlag(Windows.UI.Core.CoreVirtualKeyStates.Down);
                await MoveFindAsync(shift ? -1 : 1);
            }
        };
        _latest.Click += async (_, _) => await ReturnToLatestAsync();
        return new StackPanel { Spacing = 6, Children = { _findContainer, _latest } };
    }

    private Grid BuildConversationViewport()
    {
        var viewport = new Grid { ColumnSpacing = 6 };
        viewport.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        viewport.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        viewport.Children.Add(_chatScroll);
        Grid.SetColumn(_promptNavigator, 1);
        viewport.Children.Add(_promptNavigator);
        _promptNavigator.JumpRequested += entry => _ = JumpToEntryAsync(entry);
        _chatScroll.ViewChanged += (_, _) => UpdateActivePrompt();
        // Selectable message text takes focus and swallows Home/End, so the
        // chat handles them even when a child already did.
        _chatScroll.AddHandler(UIElement.KeyDownEvent, new KeyEventHandler((_, e) =>
        {
            if (e.OriginalSource is TextBox) return;
            if (e.Key is Windows.System.VirtualKey.Up or Windows.System.VirtualKey.PageUp) NoteUserScroll(upward: true);
            else if (e.Key is Windows.System.VirtualKey.Down or Windows.System.VirtualKey.PageDown) NoteUserScroll(upward: false);
            if (e.Key is not (Windows.System.VirtualKey.Home or Windows.System.VirtualKey.End)) return;
            var home = e.Key == Windows.System.VirtualKey.Home;
            SetStickToBottom(!home, home ? "Home key" : "End key");
            _chatScroll.ChangeView(null, home ? 0 : _chatScroll.ScrollableHeight, null);
            e.Handled = true;
        }), handledEventsToo: true);
        WireStickToBottom();
        return viewport;
    }

    // Stick-to-bottom. A chat opened without a find match lands on its newest
    // message and stays there while late layout grows the transcript
    // (markdown and tables measured after first paint, images, command and
    // sub-agent chips from the grace re-render, a page attached to the tree
    // after its first paint), until the user scrolls up. The flag, not a
    // "near bottom" measurement, decides: ChangeView is asynchronous, so the
    // live offset right after a paint is stale (often still 0), and a quiet
    // re-render that trusted it restored the chat to the top.
    private const double StickThreshold = 48;
    private bool _stickToBottom = true;
    private bool _pinQueued;
    private bool _scrollDetached = true;
    private long _programmaticScrollUntil;
    private long _userScrollUntil;
    private double _lastSeenOffset;
    private static readonly Dictionary<string, double> s_scrolledUpOffsets = new(StringComparer.OrdinalIgnoreCase);

    private void WireStickToBottom()
    {
        _chat.SizeChanged += (_, e) =>
        {
            if (Math.Abs(e.NewSize.Height - e.PreviousSize.Height) >= 0.5) QueuePinToBottom("content " + Math.Round(e.PreviousSize.Height) + "->" + Math.Round(e.NewSize.Height));
        };
        _chatScroll.SizeChanged += (_, e) =>
        {
            if (Math.Abs(e.NewSize.Width - e.PreviousSize.Width) >= 0.5) FitTranscriptWidth("viewport " + Math.Round(e.PreviousSize.Width) + "->" + Math.Round(e.NewSize.Width));
            QueuePinToBottom("viewport resized");
        };
        _chatScroll.ViewChanged += (_, e) => TrackStickToBottom(e.IsIntermediate);
        _chatScroll.AddHandler(UIElement.PointerWheelChangedEvent, new PointerEventHandler((_, e) =>
            NoteUserScroll(upward: e.GetCurrentPoint(_chatScroll).Properties.MouseWheelDelta > 0)), handledEventsToo: true);
        // Scrollbar drags and touch pans start with a press inside the viewer.
        _chatScroll.AddHandler(UIElement.PointerPressedEvent, new PointerEventHandler((_, _) => NoteUserScroll(upward: null)), handledEventsToo: true);
        _chatScroll.Loaded += (_, _) =>
        {
            _scrollDetached = false;
            // A cached page re-attached after the window or sidebar changed width.
            FitTranscriptWidth("attached");
            // Re-attaching a cached page can reset the viewer's offset.
            if (_stickToBottom) QueuePinToBottom("attached");
            else if (_lastSeenOffset > 0 && Math.Abs(_chatScroll.VerticalOffset - _lastSeenOffset) > 1) RestoreScrollOffset(_lastSeenOffset);
        };
        // Leaving the chat (its page may then drop out of PageCache).
        _chatScroll.Unloaded += (_, _) => { _scrollDetached = true; RememberScrollPosition(); };
    }

    // The transcript column follows the viewport width explicitly. Left to
    // Stretch + MaxWidth inside the ScrollViewer, a width change (sidebar
    // collapse, window resize) could leave the column arranged at a stale
    // width: the reply drifted to the middle and the right-aligned prompt
    // bubble fell outside the viewport until the chat was reopened.
    private const double TranscriptMaxWidth = 736;
    private bool _transcriptRefitQueued;

    internal void RelayoutTranscript(string reason) => FitTranscriptWidth(reason);

    private void FitTranscriptWidth(string reason)
    {
        // A cached page re-attached may have a reset offset: nothing to keep.
        if (reason != "attached") CaptureReadingAnchor(reason);
        ApplyTranscriptWidth(_chatScroll.ActualWidth - _chatScroll.Padding.Left - _chatScroll.Padding.Right, reason);
        // Once more after the pane animation or resize has settled, from the
        // viewport the ScrollViewer actually arranged.
        if (_transcriptRefitQueued || DispatcherQueue is null) return;
        _transcriptRefitQueued = true;
        DispatcherQueue.TryEnqueue(DispatcherQueuePriority.Low, () =>
        {
            _transcriptRefitQueued = false;
            var viewport = _chatScroll.ViewportWidth > 0 ? _chatScroll.ViewportWidth : _chatScroll.ActualWidth - _chatScroll.Padding.Left - _chatScroll.Padding.Right;
            ApplyTranscriptWidth(viewport, reason + " (settled)");
            RestoreReadingAnchor(reason);
        });
    }

    // Reading position across a width change (sidebar collapse or expand,
    // window resize). Text re-wraps at the new width, so the old pixel offset
    // points somewhere else: the first message on screen is kept at the same
    // distance from the top instead. Only a view that was at the bottom stays
    // pinned there.
    private (FrameworkElement Element, double FromTop)? _readingAnchor;

    private void CaptureReadingAnchor(string reason)
    {
        if (_readingAnchor is not null || _scrollDetached || _chatScroll.XamlRoot is null || _chatScroll.ScrollableHeight <= 0) return;
        var offset = _chatScroll.VerticalOffset;
        var atBottom = _chatScroll.ScrollableHeight - offset <= StickThreshold;
        // Right after a paint the pin to the bottom may not have landed yet;
        // the stick flag is right then, not the offset.
        var settling = Environment.TickCount64 - _paintedAt < 1500 || _pinQueued;
        if (atBottom || (_stickToBottom && settling)) return;
        if (_stickToBottom) SetStickToBottom(false, "width change away from the bottom (" + reason + ")");
        foreach (var child in _chat.Children.OfType<FrameworkElement>())
        {
            if (child.Visibility != Visibility.Visible || child.ActualHeight <= 0) continue;
            var top = child.TransformToVisual(_chat).TransformPoint(new Windows.Foundation.Point(0, 0)).Y;
            if (top + child.ActualHeight <= offset) continue;
            _readingAnchor = (child, top - offset);
            // The re-wrap clamps and moves the offset; that is not the user
            // reaching the bottom.
            MarkProgrammaticScroll(800);
            return;
        }
    }

    private void RestoreReadingAnchor(string reason)
    {
        if (_readingAnchor is not { } anchor) return;
        _readingAnchor = null;
        if (_stickToBottom || anchor.Element.XamlRoot is null || !_chat.Children.Contains(anchor.Element)) return;
        _chatScroll.UpdateLayout();
        var top = anchor.Element.TransformToVisual(_chat).TransformPoint(new Windows.Foundation.Point(0, 0)).Y;
        var target = Math.Clamp(top - anchor.FromTop, 0, Math.Max(0, _chatScroll.ScrollableHeight));
        if (Math.Abs(target - _chatScroll.VerticalOffset) < 0.5) return;
        MarkProgrammaticScroll();
        _chatScroll.ChangeView(null, target, null, disableAnimation: true);
        _lastSeenOffset = target;
        System.Diagnostics.Trace.WriteLine($"[scroll] session {_id}: kept reading position across width change ({reason}) -> {target:0}");
    }

    private void ApplyTranscriptWidth(double viewport, string reason)
    {
        if (viewport <= 0 || double.IsNaN(viewport) || double.IsInfinity(viewport)) return;
        var width = Math.Floor(Math.Min(TranscriptMaxWidth, viewport));
        var changed = double.IsNaN(_chat.Width) || Math.Abs(_chat.Width - width) >= 0.5;
        if (changed)
        {
            _chat.Width = width;
            System.Diagnostics.Trace.WriteLine($"[layout] session {_id}: transcript width {width:0} ({reason})");
        }
        // Children measured at the old width (wrapped text, right-aligned
        // prompt bubbles) are re-measured and re-arranged at the new one.
        _chat.InvalidateMeasure();
        _chat.InvalidateArrange();
        foreach (var child in _chat.Children) { child.InvalidateMeasure(); child.InvalidateArrange(); }
    }

    /// <summary>Something was expanded on purpose: stop pinning the transcript to its end so it can be shown.</summary>
    internal void StopFollowingBottom(string reason) => SetStickToBottom(false, reason);

    private void SetStickToBottom(bool stick, string reason)
    {
        if (_stickToBottom != stick)
            System.Diagnostics.Trace.WriteLine($"[scroll] session {_id}: stick-to-bottom {(stick ? "on" : "off")} ({reason}) offset={_chatScroll.VerticalOffset:0} scrollable={_chatScroll.ScrollableHeight:0}");
        _stickToBottom = stick;
    }

    private void MarkProgrammaticScroll(int milliseconds = 350) =>
        _programmaticScrollUntil = Math.Max(_programmaticScrollUntil, Environment.TickCount64 + milliseconds);

    // upward: true/false for wheel and keys, null when the direction is not
    // known yet (scrollbar drag, touch); the offset change decides then.
    private void NoteUserScroll(bool? upward)
    {
        _userScrollUntil = Environment.TickCount64 + 700;
        _programmaticScrollUntil = 0;
        if (upward == true && _chatScroll.ScrollableHeight > 0) SetStickToBottom(false, "user scrolled up");
    }

    private void TrackStickToBottom(bool intermediate)
    {
        if (_scrollDetached || _chatScroll.XamlRoot is null) return;
        var offset = _chatScroll.VerticalOffset;
        var distance = _chatScroll.ScrollableHeight - offset;
        var now = Environment.TickCount64;
        var userDriven = now < _userScrollUntil || now >= _programmaticScrollUntil;
        if (userDriven)
        {
            if (distance <= StickThreshold) { if (!intermediate) SetStickToBottom(true, "reached bottom"); }
            else if (offset < _lastSeenOffset - 1) SetStickToBottom(false, "scrolled up");
        }
        _lastSeenOffset = offset;
    }

    private void QueuePinToBottom(string reason)
    {
        if (!_stickToBottom || _pinQueued || _jumpInProgress || DispatcherQueue is null) return;
        _pinQueued = true;
        DispatcherQueue.TryEnqueue(DispatcherQueuePriority.Low, () =>
        {
            _pinQueued = false;
            if (!_stickToBottom || _jumpInProgress || _chatScroll.XamlRoot is null) return;
            var target = _chatScroll.ScrollableHeight;
            if (target - _chatScroll.VerticalOffset < 0.5) return;
            MarkProgrammaticScroll();
            _chatScroll.ChangeView(null, target, null, disableAnimation: true);
            System.Diagnostics.Trace.WriteLine($"[scroll] session {_id}: re-pin to bottom ({reason}) {_chatScroll.VerticalOffset:0} -> {target:0}");
        });
    }

    // Called before this page switches to another chat: a chat the user had
    // scrolled up in reopens (from the snapshot cache) where they left it.
    private void RememberScrollPosition()
    {
        if (_id.Length == 0) return;
        // A position reached through find (search hit) is not remembered.
        if (!_stickToBottom && _backwardPaging && _latestPagesShown == 1 && !_viewingHistoricalPage && _lastSeenOffset > 0 &&
            _findContainer.Visibility != Visibility.Visible)
            s_scrolledUpOffsets[_id] = _lastSeenOffset;
        else
            s_scrolledUpOffsets.Remove(_id);
        foreach (var stale in s_scrolledUpOffsets.Keys.Where(k => !s_snapshots.ContainsKey(k)).ToList()) s_scrolledUpOffsets.Remove(stale);
    }

    // After a snapshot paint: back to the remembered offset if the user had
    // scrolled up last time; otherwise the paint already pinned the bottom.
    private void RestoreRememberedScroll()
    {
        if (_pendingFind.Length > 0 || !s_scrolledUpOffsets.TryGetValue(_id, out var offset)) return;
        SetStickToBottom(false, "reopen at remembered offset");
        _lastSeenOffset = offset;
        System.Diagnostics.Trace.WriteLine($"[scroll] session {_id}: reopen from cache at remembered offset {offset:0}");
        RestoreScrollOffset(offset);
    }

    private void ResetConversationNavigation()
    {
        _conversation.Clear(); _messageAnchors.Clear();
        _historyIndexed = false; _indexingHistory = true; _viewingHistoricalPage = false;
        _savedContinuationResponse = null; _pendingFind = "";
        _findMatches = []; _findMatchIndex = -1; _findQuery = "";
        _findText.Text = "";
        _findDebounce?.Stop();
        _findBar.Visibility = Visibility.Collapsed;
        _findContainer.Visibility = Visibility.Collapsed;
        _findResults.Items.Clear();
        _latest.Visibility = Visibility.Collapsed;
        _promptNavigator.SetPrompts([]);
        ClearNavigationHighlight();
    }

    // A prompt and its reply can share one turn index (Claude Code records
    // both under the same idx), so the prompt's key carries its role; the
    // reply keeps "turn:<idx>", which saved replies use to find it.
    private static string TurnKey(string idx, string role) =>
        "turn:" + idx + (role.Equals("user", StringComparison.OrdinalIgnoreCase) ? ":user" : "");

    private static string TurnKey(JsonElement turn)
    {
        var idx = NxJson.Text(turn, "idx");
        if (idx.Length > 0) return TurnKey(idx, NxJson.Text(turn, "role"));
        var hash = SHA256.HashData(Encoding.UTF8.GetBytes(turn.GetRawText()));
        return "turn:" + Convert.ToHexString(hash, 0, 16);
    }

    // keepExisting: turns from a backward (newest-first) page are indexed
    // with page -1 ("latest"); they must not replace an entry the history
    // walk already placed on its forward page.
    private void IndexTurnPage(NxResponse response, int page, bool keepExisting = false)
    {
        var offset = 0L;
        foreach (var turn in NxJson.Items(response.Result))
        {
            var role = NxJson.Text(turn, "role").ToLowerInvariant();
            if (role is not ("user" or "assistant")) continue;
            var key = TurnKey(turn);
            if (keepExisting && _conversation.Contains(key)) { offset++; continue; }
            var text = ReadableTurn(NxJson.Text(turn, "text_preview", "text", "content", "message"), role);
            var order = long.TryParse(NxJson.Text(turn, "idx"), out var idx) ? idx : page * TurnPageSize + offset;
            _conversation.Add(new ConversationEntry(key, role, text, NxJson.Text(turn, "source_ts", "ts", "at"), page, order),
                copy: NxJson.Text(turn, "source_ts").Length == 0);
            offset++;
        }
    }

    private UIElement IndexedChatMessage(string role, string text, string key, string? agent = null, HarnessMessage? extras = null)
    {
        // Harness text in a user turn: a compaction summary is a collapsed
        // card, "[Request interrupted by user]" a small note; neither is a bubble.
        var fromUser = role.Equals("user", StringComparison.OrdinalIgnoreCase);
        var message = fromUser && TranscriptDedupe.IsCompactionSummary(text) ? SessionChatRender.SummaryCard(text)
            : fromUser && TranscriptDedupe.IsInterruptNote(text) ? SessionChatRender.SystemNote(text.Trim().Trim('[', ']'))
            : ChatMessage(role, text, agent, extras);
        if (message is FrameworkElement anchor) _messageAnchors[key] = anchor;
        return message;
    }

    private void RefreshConversationNavigation()
    {
        _promptNavigator.SetPrompts(_conversation.Prompts);
        UpdateFindMatches();
        if (_pendingFind.Length > 0 && !_indexingHistory)
        {
            _findDebounce?.Stop();
            _pendingFind = "";
            _ = MoveFindAsync(1);
        }
    }

    internal void FindText(string query)
    {
        OpenFind();
        _pendingFind = query;
        _findText.Text = query;
        _findDebounce?.Stop();
        UpdateFindMatches();
        if (_sessionLoaded && _historyIndexed) { _pendingFind = ""; _ = MoveFindAsync(1); }
    }

    // Opened from the chat list (not from a search hit): drop any find state
    // left from an earlier search jump and land on the newest message.
    internal void ClearFindForPlainOpen()
    {
        if (_findContainer.Visibility != Visibility.Visible && _findQuery.Length == 0 && _pendingFind.Length == 0) return;
        CloseFind();
        _findText.Text = "";
        _findQuery = "";
        _findMatches = [];
        _findMatchIndex = -1;
        _findResults.Items.Clear();
        _findResults.Visibility = Visibility.Collapsed;
        if (_id.Length > 0) s_scrolledUpOffsets.Remove(_id);
        if (_viewingHistoricalPage && _sessionLoaded) { _ = ReturnToLatestAsync(); return; }
        _lastSeenOffset = 0;
        SetStickToBottom(true, "opened from the chat list");
        QueuePinToBottom("opened from the chat list");
    }

    private void OpenFind()
    {
        _findContainer.Visibility = Visibility.Visible;
        _findBar.Visibility = Visibility.Visible;
        _findText.Focus(FocusState.Programmatic);
        _findText.SelectAll();
        UpdateFindMatches();
    }

    private void CloseFind()
    {
        _findDebounce?.Stop();
        _pendingFind = "";
        _findBar.Visibility = Visibility.Collapsed;
        _findContainer.Visibility = Visibility.Collapsed;
        ClearNavigationHighlight();
        _chatScroll.Focus(FocusState.Programmatic);
    }

    private void FindDebounce_Tick(DispatcherQueueTimer sender, object args)
    {
        sender.Stop();
        UpdateFindMatches();
    }

    private void UpdateFindMatches()
    {
        _findQuery = _findText.Text.Trim();
        _findMatches = _conversation.Search(_findQuery);
        _findMatchIndex = -1;
        _findNext.IsEnabled = _findPrevious.IsEnabled = _findMatches.Count > 0;
        _findCount.Text = _indexingHistory ? "Indexing history…" : _findText.Text.Trim().Length == 0 ? "" : _findMatches.Count == 0 ? "No matches" : _findMatches.Count + (_findMatches.Count == 1 ? " message" : " messages");
        _findResults.Items.Clear();
        foreach (var entry in _findMatches.Take(8))
        {
            _findResults.Items.Add(new ListViewItem
            {
                Tag = entry,
                HorizontalContentAlignment = HorizontalAlignment.Stretch,
                Content = new StackPanel
                {
                    Spacing = 3,
                    Children = { Muted(entry.Role == "user" ? "You" : "Assistant"), Line(ConversationIndex.Preview(entry.Text, _findQuery, 140)) }
                }
            });
        }
        _findResults.Visibility = _findQuery.Length > 0 && _findMatches.Count > 0 && _findContainer.Visibility == Visibility.Visible ? Visibility.Visible : Visibility.Collapsed;
        ToolTipService.SetToolTip(_findCount, !_historyIndexed && !_indexingHistory ? "Some history could not be indexed. Results cover the messages that were loaded." : null);
    }

    private async Task MoveFindAsync(int direction)
    {
        if (_findMatches.Count == 0 || _jumpInProgress) return;
        _findResults.Visibility = Visibility.Collapsed;
        _findMatchIndex = _findMatchIndex < 0 ? (direction > 0 ? 0 : _findMatches.Count - 1) : (_findMatchIndex + direction + _findMatches.Count) % _findMatches.Count;
        _findCount.Text = (_findMatchIndex + 1) + " of " + _findMatches.Count;
        await JumpToEntryAsync(_findMatches[_findMatchIndex]);
    }

    private async Task JumpToEntryAsync(ConversationEntry entry)
    {
        if (_jumpInProgress) return;
        System.Diagnostics.Trace.WriteLine($"[scroll] session {_id}: jump to {entry.Key} (page {entry.Page})");
        if (_messageAnchors.TryGetValue(entry.Key, out var existing)) { ScrollToMessage(existing); return; }
        if (_sendInFlight) { SetComposerStatus("You can open older pages after this reply finishes."); return; }
        if (entry.Page < 0) { await ReturnToLatestAsync(); if (_messageAnchors.TryGetValue(entry.Key, out var local)) ScrollToMessage(local); return; }
        if (entry.Page >= _turnPageStartCursors.Count) return;
        _jumpInProgress = true;
        var session = _id;
        var generation = _loadGeneration;
        try
        {
            var response = await CallTurnPageAsync(session, _turnPageStartCursors[entry.Page]);
            if (_id != session || generation != _loadGeneration) return;
            if (!response.Ok) { _notice.Text = response.Error ?? "Could not open this part of the conversation."; return; }
            FillChat(response, initial: true);
            // A jumped-to page is a forward page; "Load earlier" walks the
            // forward cursors from here.
            _backwardPaging = false;
            _earlierPageIndex = entry.Page;
            _viewingHistoricalPage = entry.Page < _turnPageStartCursors.Count - 1;
            _latest.Visibility = _viewingHistoricalPage ? Visibility.Visible : Visibility.Collapsed;
            if (!_viewingHistoricalPage && _savedContinuationResponse is { } saved) RenderSavedContinuations(saved, true, appendContext: false);
            UpdateEarlierTurnsButton();
            _chat.UpdateLayout();
            if (_messageAnchors.TryGetValue(entry.Key, out var anchor)) ScrollToMessage(anchor);
        }
        finally { _jumpInProgress = false; }
    }

    private async Task ReturnToLatestAsync()
    {
        if (_sendInFlight || _jumpInProgress) return;
        _jumpInProgress = true;
        var session = _id;
        var generation = _loadGeneration;
        try
        {
            // One backward request for the newest turns, instead of walking
            // forward page by page from the last known cursor.
            var responseTask = CallLatestTurnsAsync(session, "", LatestPageSize);
            var savedTask = Call(NxMethods.ContinueLocalList, new { session_id = session });
            await Task.WhenAll(responseTask, savedTask);
            if (_id != session || generation != _loadGeneration) return;
            var response = await responseTask;
            if (!response.Ok)
            {
                _latest.Visibility = _viewingHistoricalPage ? Visibility.Visible : Visibility.Collapsed;
                _notice.Text = response.Error ?? "Could not load the latest messages.";
                return;
            }
            var saved = await savedTask;
            _savedContinuationResponse = saved;
            _backwardPaging = true;
            _latestPagesShown = 1;
            _latestSignature = "";
            _olderCursor = response.Result is { } body ? NxJson.Text(body, "next_cursor") : "";
            _viewingHistoricalPage = false;
            _latest.Visibility = Visibility.Collapsed;
            IndexTurnPage(response, -1, keepExisting: true);
            FillChat(response, initial: true);
            RenderSavedContinuations(saved, true, appendContext: false);
            UpdateEarlierTurnsButton();
            RefreshConversationNavigation();
            ScrollChatToEnd();
            _paintedAt = Environment.TickCount64;
        }
        finally { _jumpInProgress = false; }
    }

    private void ScrollToMessage(FrameworkElement anchor)
    {
        // An anchor jump (find match, prompt rail) owns the position; late
        // layout must not pull the chat back to the bottom.
        SetStickToBottom(false, "anchor jump");
        void Apply()
        {
            if (anchor.XamlRoot is null) return;
            _chatScroll.UpdateLayout();
            MarkProgrammaticScroll();
            var point = anchor.TransformToVisual(_chat).TransformPoint(new Windows.Foundation.Point(0, 0));
            _chatScroll.ChangeView(null, Math.Max(0, point.Y - 24), null, disableAnimation: true);
            ClearNavigationHighlight();
            if (anchor is Border border)
            {
                _navigationHighlight = border;
                _priorHighlightBrush = border.BorderBrush;
                _priorHighlightThickness = border.BorderThickness;
                border.BorderBrush = Surface("SystemControlHighlightAccentBrush", Windows.UI.Color.FromArgb(255, 88, 178, 255));
                border.BorderThickness = new Thickness(2);
            }
        }
        DispatcherQueue.TryEnqueue(() => DispatcherQueue.TryEnqueue(Apply));
    }

    private void ClearNavigationHighlight()
    {
        if (_navigationHighlight is null) return;
        _navigationHighlight.BorderBrush = _priorHighlightBrush;
        _navigationHighlight.BorderThickness = _priorHighlightThickness;
        _navigationHighlight = null;
    }

    private void UpdateActivePrompt()
    {
        string active = "";
        var target = _chatScroll.VerticalOffset + Math.Min(100, _chatScroll.ViewportHeight / 4);
        foreach (var prompt in _conversation.Prompts)
        {
            if (!_messageAnchors.TryGetValue(prompt.Key, out var anchor) || anchor.XamlRoot is null) continue;
            var y = anchor.TransformToVisual(_chat).TransformPoint(new Windows.Foundation.Point(0, 0)).Y;
            if (y <= target) active = prompt.Key;
        }
        if (active.Length > 0) _promptNavigator.SetActive(active);
    }
}
