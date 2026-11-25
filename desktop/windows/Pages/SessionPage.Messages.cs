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

    private sealed class MessageInfo(string role, string text, MessageActionRow actions)
    {
        public string Role { get; } = role;
        public string Text { get; } = text;
        public MessageActionRow Actions { get; } = actions;
        public bool FromUser => Role.Equals("user", StringComparison.OrdinalIgnoreCase);
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
        var view = !fromUser && _adoptStream is { } streamed ? AdoptStreamView(streamed, content) : new MarkdownView(content);
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
            : new Border
            {
                Child = view.Root, Padding = SessionChatRender.AssistantPad, HorizontalAlignment = HorizontalAlignment.Stretch,
                Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent),
            };
        Border? root = null;
        var actions = new MessageActionRow(fromUser, () => content,
            edit: fromUser ? () => EditMessage(content) : null,
            retry: fromUser ? null : () => RetryReplyAsync(root!),
            fork: () => ForkFromMessageAsync(root!));
        var stack = new StackPanel { HorizontalAlignment = fromUser ? HorizontalAlignment.Right : HorizontalAlignment.Stretch };
        stack.Children.Add(bubble);
        if (strip is not null)
        {
            strip.HorizontalAlignment = bubble.HorizontalAlignment;
            strip.Margin = new Thickness(0, 6, 0, 0);
            stack.Children.Add(strip);
        }
        stack.Children.Add(actions.View);
        root = new Border
        {
            Child = stack, CornerRadius = new CornerRadius(14),
            HorizontalAlignment = fromUser ? HorizontalAlignment.Right : HorizontalAlignment.Stretch,
            // Transparent, not null: the whole message (gaps too) takes hover.
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0),
        };
        actions.HoverOn(root);
        actions.Time = () => MessageTime(root);
        s_messages.AddOrUpdate(root, new MessageInfo(role, content, actions));
        root.Loaded += (_, _) => QueueLatestReplyActions();
        return root;
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
        var at = stack.Children.IndexOf(status);
        // The role label and the "Thinking…" line give way to the reply.
        if (at > 0 && stack.Children[at - 1] is TextBlock label && !ReferenceEquals(label, status))
        {
            stack.Children.RemoveAt(at - 1);
            at--;
        }
        stack.Children.RemoveAt(at);
        stack.Children.Insert(at, host);
        s_streamViews.AddOrUpdate(stack, view);
        s_streamByStatus.AddOrUpdate(status, view);
        s_streamHosts.AddOrUpdate(view, host);
        return view;
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
