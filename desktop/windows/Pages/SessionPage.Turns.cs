using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using System.Text.Json;

namespace Nexus.Pages;

// One turn on screen: the person's bubble, the streaming reply, its
// activity, and what the finished turn turns into. Sending a message and
// reopening a chat whose agent is still working (SessionPage.Live.cs) both
// build the same view, so a replayed turn looks exactly like a live one.
public sealed partial class SessionPage
{
    private sealed class TurnView
    {
        public required string Agent { get; init; }
        public required string Prompt { get; init; }
        public required string RequestId { get; init; }
        public required string LiveTime { get; init; }
        public required UIElement UserBubble { get; init; }
        public required Border StreamBubble { get; init; }
        public required StackPanel StreamStack { get; init; }
        public required TextBlock StreamText { get; init; }
        public required LiveActivityView Activity { get; init; }
        public string UserKey { get; set; } = "";
        public bool HadStreamText { get; set; }
    }

    /// <summary>The reply was left on screen because the chat was closed; the agent keeps working.</summary>
    private static readonly NxResponse Detached = new(false, "detached", null);

    private TurnView? _turnView;

    private TurnView BeginTurnView(string agent, string prompt, string[] images, string requestID)
    {
        _streamRequestID = requestID;
        _streamAgent = agent;
        _streamHasText = false;
        var userKey = "live:" + requestID;
        var liveTime = DateTimeOffset.Now.ToString("O");
        _conversation.Add(new ConversationEntry(userKey, "user", prompt, liveTime, -1, _conversation.NextOrder));
        // The bubble shows the images right away, from the same markers the
        // saved message carries.
        var shownPrompt = HarnessMessage.Parse(ComposerAttachments.WithImageTags(prompt, images));
        var userBubble = IndexedChatMessage("user", shownPrompt.Text, userKey, null, shownPrompt.Attachments.Count > 0 ? shownPrompt : null);
        var streamText = new TextBlock
        {
            // Empty until the first words: the status line below says what is happening.
            Text = "", TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true,
            FontSize = 14, MaxWidth = 820, Opacity = 0.9, Visibility = Visibility.Collapsed
        };
        var streamStack = new StackPanel { Spacing = 7 };
        streamStack.Children.Add(SessionChatRender.RoleLabel(ChatTitles.AgentName(agent)));
        streamStack.Children.Add(streamText);
        var activityView = new LiveActivityView();
        _streamActivity = activityView;
        streamStack.Children.Add(activityView.View);
        var fileChangesHost = new Border { Visibility = Visibility.Collapsed };
        _streamFileChanges = fileChangesHost;
        streamStack.Children.Add(fileChangesHost);
        AttachTurnStatus(streamStack, agent);
        var streamBubble = new Border
        {
            Child = streamStack, Padding = SessionChatRender.AssistantPad,
            HorizontalAlignment = HorizontalAlignment.Stretch,
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent)
        };
        _streamText = streamText;
        AddChatElement(userBubble);
        AddChatElement(streamBubble);
        RefreshConversationNavigation();
        // Sending follows the message down; a turn that started on its own
        // (a queued follow-up) doesn't pull someone reading further up.
        if (_attaching) KeepLatestInView(); else ScrollChatToEnd();
        var view = new TurnView
        {
            Agent = agent, Prompt = prompt, RequestId = requestID, LiveTime = liveTime, UserBubble = userBubble,
            StreamBubble = streamBubble, StreamStack = streamStack, StreamText = streamText, Activity = activityView, UserKey = userKey
        };
        _turnView = view;
        return view;
    }

    /// <summary>The turn stopped streaming into this page (finished, failed or detached).</summary>
    private void EndTurnStreaming(TurnView view)
    {
        view.HadStreamText = _streamHasText;
        _streamRequestID = "";
        _streamAgent = "";
        _streamText = null;
        _streamActivity = null;
        _streamFileChanges = null;
        _streamHasText = false;
        _sendInFlight = false;
        DetachTurnStatus();
        if (ReferenceEquals(_turnView, view)) _turnView = null;
        ClearApprovalCards();
        SetSending(false);
        UpdateActions();
    }

    /// <summary>Turns the streamed reply into the saved message, or shows why it stopped.</summary>
    private void FinishTurnView(TurnView view, NxResponse response, string sessionID, string typed)
    {
        var agent = view.Agent;
        var prompt = view.Prompt;
        var userBubble = view.UserBubble;
        var streamBubble = view.StreamBubble;
        var streamStack = view.StreamStack;
        var activityView = view.Activity;
        if (ReferenceEquals(response, Detached))
        {
            // The chat was closed mid-turn. The agent keeps going in the core;
            // reopening the chat replays the turn from there.
            RemoveTurnView(view);
            return;
        }
        if (_id != sessionID) return;
        if (!response.Ok || response.Result is not { } result)
        {
            // The message stays, with Retry and Edit (SessionPage.Composer.cs).
            ShowTurnFailed(view, response, agent, userBubble, streamBubble, streamStack, view.StreamText, view.HadStreamText || activityView.Count > 0, view.UserKey);
            return;
        }
        var answer = NxJson.Text(result, "text");
        if (answer.Length == 0)
        {
            // A replayed turn: the core saved it, so the transcript shows it.
            if (typed.Length == 0)
            {
                RemoveTurnView(view);
                _ = RevalidateAsync();
                return;
            }
            ShowTurnFailed(view, new NxResponse(false, "The agent returned no readable reply.", null), agent, userBubble, streamBubble, streamStack, view.StreamText, view.HadStreamText || activityView.Count > 0, view.UserKey);
            return;
        }
        var liveUserKey = view.UserKey;
        var streamIndex = _chat.Children.IndexOf(streamBubble);
        var userIndex = NxJson.Text(result, "user_idx");
        if (long.TryParse(userIndex, out var userOrder) && userOrder > 0)
        {
            _conversation.Remove(liveUserKey); _messageAnchors.Remove(liveUserKey);
            liveUserKey = TurnKey(userIndex, "user");
            if (userBubble is FrameworkElement userAnchor) _messageAnchors[liveUserKey] = userAnchor;
            _conversation.Add(new ConversationEntry(liveUserKey, "user", prompt, view.LiveTime, -1, userOrder));
        }
        var answerIndex = NxJson.Text(result, "answer_idx");
        var answerKey = answerIndex.Length > 0 && answerIndex != "0" ? "turn:" + answerIndex : "answer:" + view.RequestId;
        var answerOrder = long.TryParse(answerIndex, out var ai) && ai > 0 ? ai : _conversation.NextOrder;
        _conversation.Add(new ConversationEntry(answerKey, "assistant", answer, view.LiveTime, -1, answerOrder));
        if (streamIndex >= 0)
        {
            _chat.Children.RemoveAt(streamIndex);
            streamIndex += MoveStreamThinking(streamStack, streamIndex);
            _chat.Children.Insert(streamIndex, AdoptingStream(view.StreamStack, () => IndexedChatMessage("assistant", answer, answerKey, agent)));
            if (activityView.Count > 0)
            {
                streamStack.Children.Remove(activityView.View);
                activityView.View.IsExpanded = false;
                _chat.Children.Insert(++streamIndex, activityView.View);
            }
            var changes = StringArray(result, "file_changes");
            if (changes.Count > 0) _chat.Children.Insert(streamIndex + 1, FileChangesExpander(changes, result));
        }
        var returnedNativeID = NxJson.Text(result, "native_id");
        if (returnedNativeID.Length > 0)
        {
            _localNativeID = returnedNativeID;
            _localNativeAgent = agent;
        }
        _continuationContext += "\n\nuser: " + prompt + "\n\nassistant: " + answer;
        if (_continuationContext.Length > 11000) _continuationContext = _continuationContext[^11000..];
        // Success is silent; a reply that couldn't save or sync says so on itself.
        OnTurnSaved(view, answerKey, result);
        KeepLatestInView();
        RefreshConversationNavigation();
        _ = RefreshActivityAsync(sessionID);
        if (_header.Text == "New chat" && _projectID.Length > 0 && _nativeID.StartsWith("nexus-", StringComparison.Ordinal))
        {
            var firstLine = prompt.Split('\n', 2)[0].Trim();
            var title = firstLine.Length > 72 ? firstLine[..69] + "…" : firstLine;
            if (title.Length > 0)
            {
                _header.Text = title;
                _ = NameNewChatAsync(sessionID, title);
            }
        }
        MainWindow.Current?.RefreshChats();
    }

    private void RemoveTurnView(TurnView view)
    {
        _chat.Children.Remove(view.UserBubble);
        _chat.Children.Remove(view.StreamBubble);
        _conversation.Remove(view.UserKey);
        _messageAnchors.Remove(view.UserKey);
        RefreshConversationNavigation();
        // Make the next visit re-read the transcript: the turn may finish
        // and be saved while the chat is closed.
        _loadedAt = DateTime.MinValue;
    }
}
