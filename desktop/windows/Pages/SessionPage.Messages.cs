using System.Runtime.CompilerServices;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using Windows.ApplicationModel.DataTransfer;

namespace Nexus.Pages;

// Chat messages: the bubble around a rendered reply or prompt, its action
// row (Copy, Retry, Edit, Fork from here) and the streaming reply that
// becomes the saved message without being rebuilt.
public sealed partial class SessionPage
{
    // Composer hooks. The composer owns how text gets into it and is sent;
    // these default to filling the prompt box when nothing handles them.
    partial void ComposerEditHook(string text, ref bool handled);
    partial void ComposerRetryHook(string text, ref bool handled);

    private sealed class MessageInfo(string role, string text, Func<(UIElement Content, MessageActionRow Actions)> build, bool canRelease)
    {
        public string Role { get; } = role;
        public string Text { get; } = text;
        public MessageActionRow Actions { get; set; } = null!;
        public bool FromUser => Role.Equals("user", StringComparison.OrdinalIgnoreCase);
        /// Builds the message's visuals (again, after they were released).
        public Func<(UIElement Content, MessageActionRow Actions)> Build { get; } = build;
        /// Messages with attachment chips keep their visuals.
        public bool CanRelease { get; } = canRelease;
        public bool Realized { get; set; } = true;
    }

    private static readonly ConditionalWeakTable<UIElement, MessageInfo> s_messages = new();
    private static readonly ConditionalWeakTable<Panel, MarkdownView> s_streamViews = new();
    private static readonly ConditionalWeakTable<TextBlock, MarkdownView> s_streamByStatus = new();
    private static readonly ConditionalWeakTable<MarkdownView, Panel> s_streamHosts = new();
    private const string StreamViewTag = "nexus-stream-markdown";
    private MessageActionRow? _latestReplyActions;
    private bool _latestReplyQueued;
    // Set while the saved reply is built from a streamed one (AdoptingStream).
    private MarkdownView? _adoptStream;

    private UIElement ChatMessage(string role, string content, string? assistantAgent = null, HarnessMessage? extras = null)
    {
        var strip = extras is null ? null : MessageExtras(extras);
        if (strip is not null && content.Trim().Length == 0)
        {
            // Only attachments (an image sent without text): no empty bubble.
            strip.HorizontalAlignment = role.Equals("user", StringComparison.OrdinalIgnoreCase) ? HorizontalAlignment.Right : HorizontalAlignment.Left;
            return new Border { Child = strip, HorizontalAlignment = strip.HorizontalAlignment, BorderThickness = new Thickness(0) };
        }
        return ChatBubble(role, content, assistantAgent, strip);
    }

    // The message (a filled bubble for the person, open text for the agent),
    // attachment chips under it, then the action row. The outer Border is the
    // anchor that find and prompt jumps highlight.
    private UIElement ChatBubble(string role, string content, string? assistantAgent, FrameworkElement? strip = null)
    {
        var fromUser = role.Equals("user", StringComparison.OrdinalIgnoreCase);
        var label = fromUser ? "You" : Nonempty(ChatTitles.AgentName(assistantAgent ?? _agent), "Assistant");
        Border root = new()
        {
            CornerRadius = new CornerRadius(14),
            HorizontalAlignment = fromUser ? HorizontalAlignment.Right : HorizontalAlignment.Stretch,
            BorderThickness = new Thickness(0),
        };
        var adopted = !fromUser && _adoptStream is { } streamed ? AdoptStreamView(streamed, content) : null;
        (UIElement, MessageActionRow) Build()
        {
            var view = adopted ?? new MarkdownView(content);
            adopted = null;
            var selectMenu = new MenuFlyout();
            var copySelection = new MenuFlyoutItem { Text = "Copy selection" };
            copySelection.Click += (_, _) => { if (view.SelectedText.Length > 0) SetClipboard(view.SelectedText); };
            selectMenu.Opening += (_, _) => copySelection.IsEnabled = view.SelectedText.Length > 0;
            var select = new MenuFlyoutItem { Text = "Select whole message" };
            select.Click += (_, _) => view.SelectAll();
            var copyItem = new MenuFlyoutItem { Text = "Copy whole message" };
            copyItem.Click += (_, _) => SetClipboard(content);
            selectMenu.Items.Add(copySelection);
            selectMenu.Items.Add(select);
            selectMenu.Items.Add(copyItem);
            if (fromUser) AddRestoreMenuItem(selectMenu, root);
            view.ContextFlyout = selectMenu;
            // Like T3 Code: no "You"/agent label on every turn; alignment and the
            // user block's fill say who wrote it. The name stays for screen readers.
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(view.Root, label);
            var bubble = fromUser
                ? new Border
                {
                    Child = view.Root, Padding = SessionChatRender.UserBubblePad, CornerRadius = new CornerRadius(14),
                    MaxWidth = 600, HorizontalAlignment = HorizontalAlignment.Right, Background = NexusPalette.UserFill,
                }
                : new Border { Child = view.Root, Padding = SessionChatRender.AssistantPad, HorizontalAlignment = HorizontalAlignment.Stretch };
            var actions = new MessageActionRow(fromUser, () => content,
                edit: fromUser ? () => EditMessage(content) : null,
                retry: fromUser ? null : () => RetryReplyAsync(root),
                fork: () => ForkFromMessageAsync(root),
                restore: fromUser ? () => RestoreFromMessageAsync(root) : null,
                canRestore: fromUser ? () => CanRestoreMessage(root) : null);
            // Transparent, not null: the whole message (gaps too) takes hover.
            var stack = new StackPanel { HorizontalAlignment = fromUser ? HorizontalAlignment.Right : HorizontalAlignment.Stretch, Background = TransparentBrush };
            stack.Children.Add(bubble);
            if (strip is not null)
            {
                strip.HorizontalAlignment = bubble.HorizontalAlignment;
                strip.Margin = new Thickness(0, 6, 0, 0);
                stack.Children.Add(strip);
            }
            stack.Children.Add(actions.View);
            actions.HoverOn(stack);
            actions.Time = () => MessageTime(root);
            return (stack, actions);
        }
        var info = new MessageInfo(role, content, Build, canRelease: strip is null);
        var (first, firstActions) = Build();
        root.Child = first;
        info.Actions = firstActions;
        s_messages.AddOrUpdate(root, info);
        root.Loaded += (_, _) =>
        {
            QueueLatestReplyActions();
            WatchTranscriptViewport();
        };
        return root;
    }

    private static Brush? s_transparent;
    private static Brush TransparentBrush => s_transparent ??= new SolidColorBrush(Microsoft.UI.Colors.Transparent);

    // ── Long chats: visuals only near the viewport ───────────────────────
    //
    // A chat keeps every message element (anchors, find and jumps use
    // them), but a message more than a few screens away gives up its visual
    // tree and keeps only its measured height; it is rebuilt before it
    // scrolls back into view. The height does not change, so nothing moves.

    private const double KeepScreensAbove = 2, KeepScreensBelow = 3;
    private const double ReleaseScreensAbove = 5, ReleaseScreensBelow = 6;
    private bool _viewportWatched;
    private Microsoft.UI.Dispatching.DispatcherQueueTimer? _viewportTimer;

    private void WatchTranscriptViewport()
    {
        if (!_viewportWatched)
        {
            _viewportWatched = true;
            _chatScroll.ViewChanged += (_, _) => QueueViewportPass();
            _chat.SizeChanged += (_, _) => QueueViewportPass();
        }
        QueueViewportPass();
    }

    private void QueueViewportPass()
    {
        if (_viewportTimer is null)
        {
            _viewportTimer = DispatcherQueue.CreateTimer();
            _viewportTimer.Interval = TimeSpan.FromMilliseconds(80);
            _viewportTimer.IsRepeating = false;
            _viewportTimer.Tick += (_, _) => ViewportPass();
        }
        if (!_viewportTimer.IsRunning) _viewportTimer.Start();
    }

    private void ViewportPass()
    {
        var screen = _chatScroll.ViewportHeight;
        if (screen <= 0 || _chat.XamlRoot is null) return;
        double chatTop;
        try { chatTop = _chat.TransformToVisual(_chatScroll).TransformPoint(new Windows.Foundation.Point(0, 0)).Y; }
        catch (ArgumentException) { return; }
        HashSet<DependencyObject>? focusPath = null;
        foreach (var child in _chat.Children)
        {
            if (child is not Border element || !s_messages.TryGetValue(child, out var info)) continue;
            var top = chatTop + element.ActualOffset.Y;
            var bottom = top + element.ActualHeight;
            if (info.Realized)
            {
                if (!info.CanRelease || element.ActualHeight <= 0) continue;
                if (bottom > -ReleaseScreensAbove * screen && top < (1 + ReleaseScreensBelow) * screen) continue;
                focusPath ??= FocusPath();
                if (focusPath.Contains(element)) continue;
                element.Height = element.ActualHeight;
                element.Child = null;
                info.Realized = false;
            }
            else if (bottom > -KeepScreensAbove * screen && top < (1 + KeepScreensBelow) * screen)
            {
                var (content, actions) = info.Build();
                element.Child = content;
                info.Actions = actions;
                element.ClearValue(FrameworkElement.HeightProperty);
                info.Realized = true;
                QueueLatestReplyActions();
            }
        }
    }

    // The focused element and its ancestors: a message holding focus keeps its visuals.
    private HashSet<DependencyObject> FocusPath()
    {
        var path = new HashSet<DependencyObject>();
        var focused = XamlRoot is { } xamlRoot ? Microsoft.UI.Xaml.Input.FocusManager.GetFocusedElement(xamlRoot) as DependencyObject : null;
        for (var node = focused; node is not null; node = VisualTreeHelper.GetParent(node)) path.Add(node);
        return path;
    }

    private static void SetClipboard(string text)
    {
        try
        {
            var data = new DataPackage();
            data.SetText(text);
            Clipboard.SetContent(data);
        }
        catch (Exception)
        {
            // The clipboard can be held by another app for a moment.
        }
    }

    // ── Latest reply: its actions stay shown and it offers Retry ─────────

    private void QueueLatestReplyActions()
    {
        if (_latestReplyQueued) return;
        _latestReplyQueued = true;
        DispatcherQueue.TryEnqueue(() =>
        {
            _latestReplyQueued = false;
            UpdateLatestReplyActions();
        });
    }

    private void UpdateLatestReplyActions()
    {
        MessageActionRow? latest = null;
        for (var i = _chat.Children.Count - 1; i >= 0; i--)
        {
            if (!s_messages.TryGetValue(_chat.Children[i], out var info)) continue;
            if (!info.FromUser) latest = info.Actions;
            break;
        }
        if (ReferenceEquals(latest, _latestReplyActions)) return;
        if (_latestReplyActions is { } previous)
        {
            previous.Pinned = false;
            previous.RetryVisible = false;
        }
        _latestReplyActions = latest;
        if (latest is null) return;
        latest.RetryVisible = true;
        latest.Pinned = true;
    }

    // ── Actions ───────────────────────────────────────────────────────────

    /// Puts a sent message back in the composer to change and send again.
    private void EditMessage(string text)
    {
        var handled = false;
        ComposerEditHook(text, ref handled);
        if (handled) return;
        _prompt.Text = text;
        _prompt.SelectionStart = text.Length;
        _prompt.Focus(FocusState.Programmatic);
    }

    /// Asks again with the prompt before this reply (a new turn; agents keep
    /// their history, so this is "try again", not a rewrite).
    private async Task RetryReplyAsync(UIElement reply)
    {
        var index = _chat.Children.IndexOf(reply);
        string? prompt = null;
        for (var i = index - 1; i >= 0; i--)
        {
            if (s_messages.TryGetValue(_chat.Children[i], out var info) && info.FromUser) { prompt = info.Text; break; }
        }
        if (string.IsNullOrWhiteSpace(prompt)) return;
        var handled = false;
        ComposerRetryHook(prompt, ref handled);
        if (handled || _sendInFlight || _preparingHarness) return;
        var draft = _prompt.Text;
        _prompt.Text = prompt;
        await SendHereAsync();
        // A draft typed before Retry comes back once the retry has gone.
        if (draft.Length > 0 && _prompt.Text.Length == 0) _prompt.Text = draft;
    }

    /// A new chat with the conversation up to and including this message.
    private async Task ForkFromMessageAsync(UIElement message)
    {
        if (_id.Length == 0) return;
        var key = MessageKey(message);
        var idx = TurnIdxFromKey(key);
        if (idx is null)
        {
            _notice.Text = "This message is still being saved. Try Fork from here again in a moment.";
            return;
        }
        var parentID = _id;
        var response = await Call(NxMethods.SessionsFork, new { session_id = parentID, through_idx = idx.Value });
        if (_id != parentID) return;
        if (!response.Ok || response.Result is not { } child)
        {
            _notice.Text = response.Error ?? "Could not fork this chat.";
            return;
        }
        var childID = NxJson.Text(child, "id", "session_id");
        if (childID.Length == 0)
        {
            _notice.Text = "The new chat was made, but Nexus couldn't open it. Find it at the top of the sidebar.";
            return;
        }
        MainWindow.Current?.OpenSession(childID);
        MainWindow.Current?.RefreshChats();
    }

    // The conversation key ("turn:12", "turn:12:user", "live:…") of a shown message.
    private string MessageKey(UIElement message)
    {
        foreach (var (key, anchor) in _messageAnchors)
        {
            if (ReferenceEquals(anchor, message)) return key;
            if (anchor is Border { Child: StackPanel stack } && stack.Children.Contains(message)) return key;
        }
        return "";
    }

    /// The turn index in a "turn:&lt;idx&gt;[:user|:part]" key; null for unsaved ones.
    internal static long? TurnIdxFromKey(string key)
    {
        if (!key.StartsWith("turn:", StringComparison.Ordinal)) return null;
        var rest = key["turn:".Length..];
        var colon = rest.IndexOf(':');
        if (colon >= 0) rest = rest[..colon];
        return long.TryParse(rest, System.Globalization.NumberStyles.None, System.Globalization.CultureInfo.InvariantCulture, out var idx) ? idx : null;
    }

    private string MessageTime(UIElement message)
    {
        var key = MessageKey(message);
        if (key.Length == 0 || _conversation.Find(key) is not { } entry) return "";
        if (!DateTimeOffset.TryParse(entry.Timestamp, System.Globalization.CultureInfo.InvariantCulture, System.Globalization.DateTimeStyles.AssumeUniversal, out var at)) return "";
        var local = at.ToLocalTime();
        var time = local.ToString("t", System.Globalization.CultureInfo.CurrentCulture);
        return local.Date == DateTime.Today ? time
            : local.Year == DateTime.Today.Year ? local.ToString("MMM d", System.Globalization.CultureInfo.CurrentCulture) + ", " + time
            : local.ToString("d", System.Globalization.CultureInfo.CurrentCulture) + ", " + time;
    }

    // ── Streaming reply ───────────────────────────────────────────────────

    /// The Markdown view a live reply streams into, placed where the status
    /// line was. It reserves the action row's height so the saved message
    /// (which adopts this view) lines up exactly.
    private MarkdownView StreamView(TextBlock status)
    {
        if (s_streamByStatus.TryGetValue(status, out var existing)) return existing;
        if (status.Parent is not Panel stack) return new MarkdownView(streaming: true);
        var view = new MarkdownView(streaming: true);
        var host = new StackPanel { Tag = StreamViewTag };
        host.Children.Add(view.Root);
        // The action row (2 + 28) the saved message adds under its text; the
        // stream bubble's own bottom padding stands in for the message's.
        host.Children.Add(new Border { Height = 30 });
        // The role label and the "Thinking…" line give way to the reply.
        foreach (var label in stack.Children.OfType<TextBlock>().Where(t => !ReferenceEquals(t, status)).ToList())
            stack.Children.Remove(label);
        var at = stack.Children.IndexOf(status);
        stack.Children.RemoveAt(at);
        stack.Children.Insert(at, host);
        s_streamViews.AddOrUpdate(stack, view);
        s_streamByStatus.AddOrUpdate(status, view);
        s_streamHosts.AddOrUpdate(view, host);
        return view;
    }

    // ── Live thinking ─────────────────────────────────────────────────────

    private static readonly ConditionalWeakTable<Panel, ThinkingView> s_streamThinking = new();

    /// The "Thinking… Ns" row of a live reply, above where its text will go,
    /// counted from <paramref name="startedAt"/> (the thinking event's time).
    private ThinkingView StreamThinking(TextBlock status, DateTimeOffset? startedAt = null)
    {
        if (status.Parent is not Panel stack) return ThinkingView.Live(startedAt);
        if (s_streamThinking.TryGetValue(stack, out var existing))
        {
            if (startedAt is { } at) existing.StartedSince(at);
            return existing;
        }
        var view = ThinkingView.Live(startedAt);
        stack.Children.Insert(stack.Children.IndexOf(status), view.View);
        // The row says it; the status line would repeat it.
        status.Visibility = Visibility.Collapsed;
        s_streamThinking.AddOrUpdate(stack, view);
        TurnStatusThinkingRow(true);
        return view;
    }

    /// The reply's text started (at <paramref name="at"/> when known): "Thought for Ns".
    private void FinishStreamThinking(TextBlock status, DateTimeOffset? at = null)
    {
        if (status.Parent is not Panel stack || !s_streamThinking.TryGetValue(stack, out var view) || !view.IsLive) return;
        view.Finish(at);
        TurnStatusThinkingRow(false);
    }

    /// Moves a streamed reply's thinking row into the chat at `at`, where it
    /// stays above the saved message. Returns how many elements it added.
    private int MoveStreamThinking(Panel streamStack, int at)
    {
        if (!s_streamThinking.TryGetValue(streamStack, out var view)) return 0;
        view.Finish();
        streamStack.Children.Remove(view.View);
        _chat.Children.Insert(at, view.View);
        s_streamThinking.Remove(streamStack);
        return 1;
    }

    /// Builds the saved reply with the streamed view inside it.
    private UIElement AdoptingStream(Panel streamStack, Func<UIElement> build)
    {
        _adoptStream = s_streamViews.TryGetValue(streamStack, out var view) ? view : null;
        try { return build(); }
        finally { _adoptStream = null; }
    }

    private MarkdownView AdoptStreamView(MarkdownView view, string content)
    {
        _adoptStream = null;
        // FrameworkElement.Parent is not reliable once the stream bubble left the tree.
        if (s_streamHosts.TryGetValue(view, out var host)) host.Children.Remove(view.Root);
        view.Update(content, streaming: false);
        return view;
    }
}
