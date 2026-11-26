using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Media.Animation;
using Microsoft.UI.Xaml.Shapes;

namespace Nexus.Pages;

// "Jump to latest": a round ↓ that floats above the composer while you read
// further up. It counts replies that arrived below and shows a dot while one
// is streaming. Nothing scrolls you down while you are reading; the
// transcript follows new content only when it is pinned to the bottom.
public sealed partial class SessionPage
{
    private const double JumpShowDistance = 200;
    private const int JumpFadeMs = 150;
    private readonly Button _jumpLatest = new();
    private readonly Border _jumpBadge = new() { Visibility = Visibility.Collapsed };
    private readonly TextBlock _jumpCount = new() { FontSize = 10.5, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, Foreground = new SolidColorBrush(Microsoft.UI.Colors.White), HorizontalAlignment = HorizontalAlignment.Center };
    private readonly Ellipse _jumpDot = new() { Width = 8, Height = 8, Visibility = Visibility.Collapsed };
    private readonly HashSet<object> _newBelow = new();
    private bool _jumpShown;
    private Storyboard? _jumpFade;

    /// <summary>BuildConversationViewport hook: the button over the transcript column.</summary>
    private UIElement BuildJumpToLatest()
    {
        _jumpLatest.Content = new FontIcon { Glyph = "", FontSize = 13 };
        _jumpLatest.Width = _jumpLatest.Height = _jumpLatest.MinWidth = _jumpLatest.MinHeight = 32;
        _jumpLatest.Padding = new Thickness(0);
        _jumpLatest.CornerRadius = new CornerRadius(16);
        _jumpLatest.BorderThickness = new Thickness(1);
        _jumpLatest.BorderBrush = NexusPalette.Hairline;
        // Solid and lifted, so it reads over the text it floats on.
        _jumpLatest.Background = NexusPalette.UserFill;
#if !HAS_UNO
        _jumpLatest.Shadow = new ThemeShadow();
        _jumpLatest.Translation = new System.Numerics.Vector3(0, 0, 18);
#endif
        _jumpLatest.Click += async (_, _) => await JumpToLatestAsync();
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_jumpLatest, "Jump to latest");
        ToolTipService.SetToolTip(_jumpLatest, "Jump to latest (" + Shortcuts.Label("↓") + ")");
        _jumpBadge.Child = _jumpCount;
        _jumpBadge.Background = NexusPalette.Accent;
        _jumpBadge.CornerRadius = new CornerRadius(8);
        _jumpBadge.MinWidth = 16;
        _jumpBadge.Height = 16;
        _jumpBadge.Padding = new Thickness(4, 0, 4, 0);
        _jumpBadge.HorizontalAlignment = HorizontalAlignment.Right;
        _jumpBadge.VerticalAlignment = VerticalAlignment.Top;
        _jumpBadge.Margin = new Thickness(0, -5, -7, 0);
        _jumpBadge.IsHitTestVisible = false;
        _jumpDot.Fill = NexusPalette.Accent;
        _jumpDot.HorizontalAlignment = HorizontalAlignment.Right;
        _jumpDot.VerticalAlignment = VerticalAlignment.Top;
        _jumpDot.Margin = new Thickness(0, 0, 0, 0);
        _jumpDot.IsHitTestVisible = false;
        var host = new Grid
        {
            HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Bottom,
            Margin = new Thickness(0, 0, 0, 12), Visibility = Visibility.Collapsed, Opacity = 0,
            RenderTransform = new ScaleTransform { ScaleX = 0.85, ScaleY = 0.85 }, RenderTransformOrigin = new Windows.Foundation.Point(0.5, 0.5),
            Children = { _jumpLatest, _jumpBadge, _jumpDot }
        };
        // After the stick-to-bottom tracking and the viewer's extent have caught up.
        _chatScroll.ViewChanged += (_, _) => QueueJumpUpdate();
        _chat.SizeChanged += (_, _) => QueueJumpUpdate();
        _chatScroll.SizeChanged += (_, _) => QueueJumpUpdate();
        // The header's "↓ Back to latest" (older pages) is this button now.
        _latest.RegisterPropertyChangedCallback(VisibilityProperty, (_, _) =>
        {
            if (_latest.Visibility == Visibility.Visible) _latest.Visibility = Visibility.Collapsed;
            UpdateJumpToLatest();
        });
        return host;
    }

    private FrameworkElement? JumpHost => _jumpLatest.Parent as FrameworkElement;
    private bool _jumpUpdateQueued;

    private void QueueJumpUpdate()
    {
        if (_jumpUpdateQueued || DispatcherQueue is null) return;
        _jumpUpdateQueued = true;
        DispatcherQueue.TryEnqueue(Microsoft.UI.Dispatching.DispatcherQueuePriority.Low, () =>
        {
            _jumpUpdateQueued = false;
            UpdateJumpToLatest();
        });
    }

    private void UpdateJumpToLatest()
    {
        if (JumpHost is not { } host) return;
        var distance = _chatScroll.ScrollableHeight - _chatScroll.VerticalOffset;
        var away = _viewingHistoricalPage || (!_stickToBottom && distance > JumpShowDistance && !_scrollDetached);
        if (_stickToBottom && distance <= StickThreshold) _newBelow.Clear();
        var count = _newBelow.Count;
        _jumpCount.Text = count > 9 ? "9+" : count.ToString();
        _jumpBadge.Visibility = count > 0 ? Visibility.Visible : Visibility.Collapsed;
        // New content streaming below and nothing counted yet: a dot.
        _jumpDot.Visibility = count == 0 && _streamHasText && LiveTurnActive ? Visibility.Visible : Visibility.Collapsed;
        ToolTipService.SetToolTip(_jumpLatest, (count == 1 ? "1 new reply · " : count > 1 ? count + " new replies · " : "") + "Jump to latest (" + Shortcuts.Label("↓") + ")");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_jumpLatest, count > 0 ? "Jump to latest, " + count + " new" : "Jump to latest");
        if (away == _jumpShown) return;
        _jumpShown = away;
        FadeJump(host, away);
    }

    private void FadeJump(FrameworkElement host, bool show)
    {
        _jumpFade?.Stop();
        host.Visibility = Visibility.Visible;
        host.IsHitTestVisible = show;
        var scale = (ScaleTransform)host.RenderTransform;
        if (!Motion.Enabled)
        {
            host.Opacity = show ? 1 : 0;
            scale.ScaleX = scale.ScaleY = 1;
            if (!show) host.Visibility = Visibility.Collapsed;
            return;
        }
        var from = host.Opacity;
        var board = new Storyboard();
        void Add(DependencyObject target, string property, double a, double b)
        {
            var animation = new DoubleAnimation { From = a, To = b, Duration = TimeSpan.FromMilliseconds(JumpFadeMs), FillBehavior = FillBehavior.Stop, EasingFunction = new CubicEase { EasingMode = EasingMode.EaseOut } };
            Storyboard.SetTarget(animation, target);
            Storyboard.SetTargetProperty(animation, property);
            board.Children.Add(animation);
        }
        Add(host, "Opacity", from, show ? 1 : 0);
        Add(scale, "ScaleX", show ? 0.85 : 1, show ? 1 : 0.85);
        Add(scale, "ScaleY", show ? 0.85 : 1, show ? 1 : 0.85);
        host.Opacity = show ? 1 : 0;
        scale.ScaleX = scale.ScaleY = show ? 1 : 0.85;
        board.Completed += (_, _) =>
        {
            if (!ReferenceEquals(_jumpFade, board)) return;
            _jumpFade = null;
            if (!_jumpShown) host.Visibility = Visibility.Collapsed;
        };
        _jumpFade = board;
        board.Begin();
    }

    /// <summary>Back to the newest message (the button, Ctrl/Cmd+↓, End), then follow it again.</summary>
    private async Task JumpToLatestAsync()
    {
        _newBelow.Clear();
        if (_viewingHistoricalPage)
        {
            await ReturnToLatestAsync();
            UpdateJumpToLatest();
            return;
        }
        SetStickToBottom(true, "jump to latest");
        MarkProgrammaticScroll(Motion.Enabled ? 700 : 350);
        _chatScroll.ChangeView(null, _chatScroll.ScrollableHeight, null, disableAnimation: !Motion.Enabled);
        UpdateJumpToLatest();
    }

    /// <summary>A reply arrived (or started) out of view: count it once.</summary>
    private void NoteNewBelow(object turn)
    {
        if (_stickToBottom) return;
        _newBelow.Add(turn);
        UpdateJumpToLatest();
    }

    /// <summary>Follows new content only while the transcript is pinned to the bottom.</summary>
    private void KeepLatestInView()
    {
        if (_stickToBottom) ScrollChatToEnd();
        else UpdateJumpToLatest();
    }
}
