using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Shapes;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// The leading status slot on a sidebar chat row: a spinner while the agent
/// works, a pulsing amber dot when it needs approval, an accent dot for a
/// reply that arrived while you were away, red for a failure, a hollow dot
/// while queued. Collapsed (no gap) when the chat is idle. Follows
/// <see cref="LiveSessions"/> by itself while it is on screen.
/// </summary>
internal sealed partial class ChatStatusDot : Grid
{
    private readonly string _sessionId;
    private LiveSessions.Indicator _shown = (LiveSessions.Indicator)(-1);

    public ChatStatusDot(string sessionId)
    {
        _sessionId = sessionId;
        Width = 10;
        Height = 10;
        VerticalAlignment = VerticalAlignment.Center;
        Loaded += (_, _) => { LiveSessions.Current.Changed += OnChanged; Show(LiveSessions.Current.IndicatorFor(_sessionId)); };
        Unloaded += (_, _) => LiveSessions.Current.Changed -= OnChanged;
        Show(LiveSessions.Current.IndicatorFor(_sessionId));
    }

    private void OnChanged(LiveSessions.ChatStatus? previous, LiveSessions.ChatStatus next)
    {
        if (!string.Equals(next.SessionId, _sessionId, StringComparison.OrdinalIgnoreCase)) return;
        DispatcherQueue?.TryEnqueue(() => Show(LiveSessions.Current.IndicatorFor(_sessionId)));
    }

    private void Show(LiveSessions.Indicator indicator)
    {
        if (indicator == _shown) return;
        _shown = indicator;
        Children.Clear();
        Visibility = indicator == LiveSessions.Indicator.None ? Visibility.Collapsed : Visibility.Visible;
        var label = indicator switch
        {
            LiveSessions.Indicator.Running => "Agent working",
            LiveSessions.Indicator.Queued => "Waiting to start",
            LiveSessions.Indicator.Approval => "Needs your approval",
            LiveSessions.Indicator.Unread => "New reply",
            LiveSessions.Indicator.Error => "Stopped with a problem",
            LiveSessions.Indicator.Interrupted => "Stopped when Nexus closed",
            _ => ""
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(this, label);
        ToolTipService.SetToolTip(this, label.Length > 0 ? label : null);
        switch (indicator)
        {
            case LiveSessions.Indicator.Running:
                Children.Add(new ProgressRing { Width = 10, Height = 10, MinWidth = 10, MinHeight = 10, IsActive = true, IsIndeterminate = true, Foreground = NexusPalette.Accent });
                break;
            case LiveSessions.Indicator.Approval:
                var amber = Dot(NexusPalette.Amber, 8);
                Children.Add(amber);
                Motion.Twinkle(amber, 1200, 0, 0.35f);
                break;
            case LiveSessions.Indicator.Unread:
                // Teal, so a new reply never reads as the amber "needs you".
                Children.Add(Dot(NexusPalette.Teal, 8));
                break;
            case LiveSessions.Indicator.Error:
                Children.Add(Dot(new SolidColorBrush(Windows.UI.Color.FromArgb(255, 0xE5, 0x48, 0x4D)), 8));
                break;
            case LiveSessions.Indicator.Queued:
                Children.Add(new Ellipse { Width = 8, Height = 8, Stroke = NexusPalette.Muted, StrokeThickness = 1.5 });
                break;
            case LiveSessions.Indicator.Interrupted:
                Children.Add(new Ellipse { Width = 8, Height = 8, Stroke = NexusPalette.Amber, StrokeThickness = 1.5 });
                break;
        }
    }

    private static Ellipse Dot(Brush fill, double size) => new() { Width = size, Height = size, Fill = fill, HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center };
}
