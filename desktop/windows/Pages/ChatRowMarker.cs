using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// "Draft" (italic, accent) on a sidebar chat row when the chat has an unsent
/// message, and "Queued" (or "3 queued") when follow-ups wait behind its
/// reply. The open chat shows no "Draft": you are looking at it. Collapsed
/// when there is nothing to say. Follows <see cref="ComposerDraftStore"/> and
/// <see cref="LiveSessions"/> by itself while on screen.
/// </summary>
internal sealed partial class ChatRowMarker : Grid
{
    private readonly string _sessionId;
    private readonly TextBlock _text = new() { FontSize = 12, FontStyle = Windows.UI.Text.FontStyle.Italic, VerticalAlignment = VerticalAlignment.Center };
    private string _shown = "\0";

    public ChatRowMarker(string sessionId)
    {
        _sessionId = sessionId;
        VerticalAlignment = VerticalAlignment.Center;
        _text.Foreground = NexusPalette.Accent;
        Children.Add(_text);
        Loaded += (_, _) =>
        {
            ComposerDraftStore.Default.Changed += OnDraftChanged;
            LiveSessions.Current.Changed += OnLiveChanged;
            Refresh();
        };
        Unloaded += (_, _) =>
        {
            ComposerDraftStore.Default.Changed -= OnDraftChanged;
            LiveSessions.Current.Changed -= OnLiveChanged;
        };
        Refresh();
    }

    /// <summary>What the marker says for a chat: "Queued"/"2 queued" wins over "Draft"; "" for nothing.</summary>
    internal static string TextFor(bool draft, int queued) =>
        queued > 1 ? queued + " queued" : queued == 1 ? "Queued" : draft ? "Draft" : "";

    private void OnDraftChanged(string sessionId)
    {
        if (!string.Equals(sessionId, _sessionId, StringComparison.OrdinalIgnoreCase)) return;
        DispatcherQueue?.TryEnqueue(Refresh);
    }

    private void OnLiveChanged(LiveSessions.ChatStatus? previous, LiveSessions.ChatStatus next)
    {
        if (!string.Equals(next.SessionId, _sessionId, StringComparison.OrdinalIgnoreCase)) return;
        DispatcherQueue?.TryEnqueue(Refresh);
    }

    private void Refresh()
    {
        var queued = LiveSessions.Current.Get(_sessionId)?.Followups ?? 0;
        var text = TextFor(ComposerDraftStore.Default.ShowsDraftMarker(_sessionId), queued);
        if (text == _shown) return;
        var appearing = _shown is "\0" or "" && text.Length > 0;
        _shown = text;
        _text.Text = text;
        Visibility = text.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
        var tip = queued > 0
            ? (queued == 1 ? "1 message waits" : queued + " messages wait") + " to go when the current reply finishes"
            : text.Length > 0 ? "You have an unsent message in this chat" : null;
        ToolTipService.SetToolTip(this, tip);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(this, tip ?? "");
        if (appearing && IsLoaded) Motion.FadeSlideIn(this, dx: 4, dy: 0, durationMs: 160);
    }
}
