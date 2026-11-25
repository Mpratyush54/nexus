using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// An agent's thinking: one quiet collapsed row, "Thought for 12s" (or
/// "Thought process" when the duration is unknown), that opens to the text.
/// <see cref="Live"/> makes the streaming form: "Thinking… 4s" ticking until
/// <see cref="Finish"/>.
/// </summary>
internal sealed class ThinkingView
{
    private readonly TextBlock _label;
    private readonly FontIcon _chevron;
    private readonly Border _detail;
    private readonly TextBlock _body;
    private readonly Button _button;
    private readonly System.Text.StringBuilder _text = new();
    private DispatcherTimer? _tick;
    private DateTimeOffset _started;

    public ThinkingView(string content, TimeSpan? duration = null)
    {
        _text.Append(NxJson.Scrub(content ?? ""));
        var icon = new FontIcon
        {
            Glyph = "", FontFamily = WorkLogVisuals.IconFont, FontSize = 12, Foreground = NexusPalette.Muted,
            VerticalAlignment = VerticalAlignment.Center, HorizontalAlignment = HorizontalAlignment.Center
        };
        _label = new TextBlock { FontSize = 12.5, Foreground = NexusPalette.Muted, VerticalAlignment = VerticalAlignment.Center };
        _chevron = WorkLogVisuals.Chevron();
        _chevron.Opacity = 0.7;
        var line = new Grid { ColumnSpacing = 8 };
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(16) });
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(10) });
        line.Children.Add(icon);
        Grid.SetColumn(_label, 1);
        line.Children.Add(_label);
        Grid.SetColumn(_chevron, 2);
        line.Children.Add(_chevron);
        _button = WorkLogVisuals.RowButton(line);
        _button.HorizontalAlignment = HorizontalAlignment.Left;
        _body = new TextBlock
        {
            TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true, FontSize = 12.5, LineHeight = 19,
            Foreground = NexusPalette.Muted
        };
        _detail = new Border
        {
            Child = _body, Visibility = Visibility.Collapsed, Margin = new Thickness(9, 2, 6, 8),
            Padding = new Thickness(14, 2, 0, 2), BorderThickness = new Thickness(2, 0, 0, 0), BorderBrush = NexusPalette.Hairline
        };
        _button.Click += (_, _) => Toggle();
        var stack = new StackPanel { Margin = new Thickness(0, 2, 0, 2) };
        stack.Children.Add(_button);
        stack.Children.Add(_detail);
        View = stack;
        SetLabel(duration is { } d ? "Thought for " + Seconds(d) : "Thought process");
    }

    public FrameworkElement View { get; }

    public bool IsExpanded => _detail.Visibility == Visibility.Visible;

    /// Streaming thinking: "Thinking… Ns" ticks once a second.
    public static ThinkingView Live()
    {
        var view = new ThinkingView("");
        view._started = DateTimeOffset.Now;
        view.SetLabel("Thinking…");
        view._tick = new DispatcherTimer { Interval = TimeSpan.FromSeconds(1) };
        view._tick.Tick += (_, _) => view.SetLabel("Thinking… " + Seconds(DateTimeOffset.Now - view._started));
        view._tick.Start();
        return view;
    }

    public void Append(string delta)
    {
        if (string.IsNullOrEmpty(delta)) return;
        _text.Append(delta);
        if (IsExpanded) _body.Text = _text.ToString();
    }

    /// The thinking ended (the reply's text started): "Thought for Ns".
    public void Finish(TimeSpan? duration = null)
    {
        _tick?.Stop();
        _tick = null;
        var took = duration ?? (_started == default ? (TimeSpan?)null : DateTimeOffset.Now - _started);
        SetLabel(took is { } d ? "Thought for " + Seconds(d) : "Thought process");
    }

    private void Toggle()
    {
        var open = !IsExpanded;
        if (open) _body.Text = _text.Length > 0 ? _text.ToString() : "(no thinking text was recorded)";
        _detail.Visibility = open ? Visibility.Visible : Visibility.Collapsed;
        _chevron.Glyph = open ? WorkLogVisuals.ChevronDown : WorkLogVisuals.ChevronRight;
        SetLabel(_label.Text);
        if (open) Motion.FadeSlideIn(_detail, dy: -4, durationMs: 180);
    }

    private void SetLabel(string text)
    {
        _label.Text = text;
        ToolTipService.SetToolTip(_button, IsExpanded ? "Hide thinking" : "Show thinking");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_button, text + (IsExpanded ? ", expanded" : ", collapsed"));
    }

    internal static string Seconds(TimeSpan span) =>
        span.TotalSeconds < 60 ? Math.Max(1, (int)Math.Round(span.TotalSeconds)) + "s" : (int)span.TotalMinutes + "m " + span.Seconds + "s";
}

