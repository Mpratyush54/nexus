using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Media.Animation;
using Microsoft.UI.Xaml.Shapes;
using Nexus.Core;
using System.Text.Json;

namespace Nexus.Pages;

// One status line inside the streaming reply, Claude Code style:
// "● Running npm test… · 12s · Esc to stop". It ticks every second and
// follows the turn's events; the composer no longer narrates the turn.
public sealed partial class SessionPage
{
    private sealed class TurnStatusLine
    {
        public required TurnStatus Model { get; init; }
        public required StackPanel Root { get; init; }
        public required TextBlock Verb { get; init; }
        public required TextBlock Elapsed { get; init; }
        public required TextBlock Hint { get; init; }
        public required TextBlock Notice { get; init; }
        public required Ellipse Dot { get; init; }
        public DispatcherQueueTimer? Timer { get; set; }
        public Storyboard? Pulse { get; set; }
        public string LastSpoken { get; set; } = "";
    }

    private TurnStatusLine? _turnStatus;

    /// <summary>BeginTurnView hook: the line goes at the end of the reply and starts ticking.</summary>
    private void AttachTurnStatus(StackPanel streamStack, string agent)
    {
        DetachTurnStatus();
        var accent = NexusPalette.Accent;
        var dot = new Ellipse { Width = 7, Height = 7, Fill = accent, VerticalAlignment = VerticalAlignment.Center, Margin = new Thickness(1, 1, 3, 0) };
        var verb = new TextBlock { FontSize = 12.5, VerticalAlignment = VerticalAlignment.Center, Foreground = ShimmerBrush() };
        var elapsed = new TextBlock { FontSize = 12.5, Foreground = NexusPalette.Muted, VerticalAlignment = VerticalAlignment.Center};
        var hint = new TextBlock { FontSize = 12, Foreground = NexusPalette.Muted, Opacity = 0.75, VerticalAlignment = VerticalAlignment.Center };
        var line = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6, Children = { dot, verb, elapsed, hint } };
        var notice = new TextBlock { FontSize = 11.5, Foreground = NexusPalette.Muted, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed, MaxWidth = 640, Margin = new Thickness(17, 0, 0, 0) };
        var root = new StackPanel { Spacing = 2, Margin = new Thickness(0, 4, 0, 0), Children = { line, notice } };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetLiveSetting(root, Microsoft.UI.Xaml.Automation.Peers.AutomationLiveSetting.Polite);
        var status = new TurnStatusLine
        {
            Model = new TurnStatus(ChatTitles.AgentName(agent)), Root = root, Verb = verb, Elapsed = elapsed, Hint = hint, Notice = notice, Dot = dot
        };
        _turnStatus = status;
        streamStack.Children.Add(root);
        if (Motion.Enabled)
        {
            var pulse = new DoubleAnimation { From = 1, To = 0.3, Duration = TimeSpan.FromMilliseconds(900), AutoReverse = true, RepeatBehavior = RepeatBehavior.Forever, EasingFunction = new SineEase { EasingMode = EasingMode.EaseInOut } };
            Storyboard.SetTarget(pulse, dot);
            Storyboard.SetTargetProperty(pulse, "Opacity");
            var board = new Storyboard();
            board.Children.Add(pulse);
            if (verb.Foreground is LinearGradientBrush shimmer && shimmer.GradientStops.Count == 3)
            {
                // A soft highlight sweeps across the verb, left to right.
                for (var i = 0; i < 3; i++)
                {
                    var sweep = new DoubleAnimation
                    {
                        From = -0.6 + i * 0.3, To = 1.4 + i * 0.3, Duration = TimeSpan.FromMilliseconds(1800),
                        RepeatBehavior = RepeatBehavior.Forever, EnableDependentAnimation = true
                    };
                    Storyboard.SetTarget(sweep, shimmer.GradientStops[i]);
                    Storyboard.SetTargetProperty(sweep, "Offset");
                    board.Children.Add(sweep);
                }
            }
            status.Pulse = board;
            board.Begin();
        }
        var timer = DispatcherQueue.CreateTimer();
        timer.Interval = TimeSpan.FromSeconds(1);
        timer.IsRepeating = true;
        timer.Tick += (_, _) => RenderTurnStatus();
        timer.Start();
        status.Timer = timer;
        RenderTurnStatus();
    }

    private static Brush ShimmerBrush()
    {
        var muted = (NexusPalette.Muted as SolidColorBrush)?.Color ?? Windows.UI.Color.FromArgb(255, 160, 160, 160);
#if HAS_UNO
        return new SolidColorBrush(muted);
#else
        if (!Motion.Enabled) return new SolidColorBrush(muted);
        var bright = ThemeService.EffectiveIsDark ? Windows.UI.Color.FromArgb(255, 245, 245, 245) : Windows.UI.Color.FromArgb(255, 20, 20, 20);
        return new LinearGradientBrush
        {
            StartPoint = new Windows.Foundation.Point(0, 0.5), EndPoint = new Windows.Foundation.Point(1, 0.5),
            GradientStops =
            {
                new GradientStop { Color = muted, Offset = -0.6 },
                new GradientStop { Color = bright, Offset = -0.3 },
                new GradientStop { Color = muted, Offset = 0.0 }
            }
        };
#endif
    }

    private void RenderTurnStatus()
    {
        if (_turnStatus is not { } s) return;
        var model = s.Model;
        s.Verb.Text = model.Verb + "…";
        s.Elapsed.Text = "· " + TurnStatus.FormatElapsed(model.Elapsed);
        s.Hint.Text = model.ShowsStopHint ? "· Esc to stop" : "";
        s.Dot.Fill = model.Current == TurnStatus.Phase.Approval ? NexusPalette.Amber : NexusPalette.Accent;
        s.Notice.Text = model.Notice;
        s.Notice.Visibility = model.Notice.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
        // Screen readers hear the verb change, not every second.
        var spoken = model.Verb + (model.Notice.Length > 0 ? ". " + model.Notice : "");
        if (spoken != s.LastSpoken)
        {
            s.LastSpoken = spoken;
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(s.Root, spoken);
        }
    }

    /// <summary>EndTurnStreaming hook: the line goes once the reply stops streaming.</summary>
    private void DetachTurnStatus()
    {
        if (_turnStatus is not { } s) return;
        _turnStatus = null;
        s.Timer?.Stop();
        s.Pulse?.Stop();
        if (s.Root.Parent is Panel parent) parent.Children.Remove(s.Root);
    }

    /// <summary>ApplyLiveEvent hook: one live event moves the line along.</summary>
    private void TurnStatusEvent(string kind, JsonElement root)
    {
        if (_turnStatus is not { } s) return;
        var text = NxJson.Text(root, "text");
        if (kind == "error" && text.Length > 0) { kind = "status"; text = AppDiagnostics.Friendly("live", text); }
        var tool = kind == "tool_start" ? NxJson.Text(root, "tool_name") : "";
        var input = kind == "tool_start" ? NxJson.Text(root, "tool_input") : "";
        if (s.Model.Apply(kind, text, tool, input)) RenderTurnStatus();
    }

    /// <summary>The one-shot (continue.here) path: progress without live events.</summary>
    private void TurnStatusProgress(string kind, string text)
    {
        if (_turnStatus is not { } s) return;
        var changed = kind switch
        {
            "activity" => s.Model.Apply("tool_start", "", ActivityTool(text), ActivityInput(text)),
            "status" => s.Model.Apply("status", text),
            "text" => s.Model.Apply("text_delta"),
            _ => false
        };
        if (changed) RenderTurnStatus();
    }

    private static string ActivityTool(string json)
    {
        try { using var doc = JsonDocument.Parse(json); return NxJson.Text(doc.RootElement, "tool"); }
        catch (JsonException) { return ""; }
    }

    private static string ActivityInput(string json)
    {
        try { using var doc = JsonDocument.Parse(json); return NxJson.Text(doc.RootElement, "input"); }
        catch (JsonException) { return ""; }
    }

    private void TurnStatusQueued()
    {
        if (_turnStatus is not { } s) return;
        s.Model.Queued();
        RenderTurnStatus();
    }

    private void TurnStatusNotice(string text)
    {
        if (_turnStatus is not { } s) return;
        if (s.Model.Apply("status", text)) RenderTurnStatus();
    }

    /// <summary>A reopened chat's turn started before the page showed it.</summary>
    private void TurnStatusStartedAt(string iso)
    {
        if (_turnStatus is not { } s || !DateTimeOffset.TryParse(iso, out var when) || when.Year < 2000) return;
        s.Model.StartedSince(when);
        RenderTurnStatus();
    }

    private void TurnStatusStopping(string? verb = null)
    {
        if (_turnStatus is not { } s) return;
        s.Model.Stopping();
        RenderTurnStatus();
        if (verb is { Length: > 0 }) s.Verb.Text = verb + "…";
    }
}
