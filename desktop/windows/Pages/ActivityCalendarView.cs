using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

internal sealed class ActivityCalendarView : UserControl
{
    private readonly StackPanel _content = new() { Spacing = 14 };
    internal ActivityCalendarView() => Content = _content;

    // The page owns the "Activity" heading; this view is the numbers and grid.
    internal void ShowLoading(string scope)
    {
        _content.Children.Clear();
        _content.Children.Add(Skeleton.Block(280, 12, 6));
        var grid = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 3 };
        for (var week = 0; week < 26; week++)
        {
            var column = new StackPanel { Spacing = 3 };
            for (var day = 0; day < 7; day++) column.Children.Add(new Border { Width = 12, Height = 12, CornerRadius = new CornerRadius(2), Background = SessionChatRender.SubtleFill });
            grid.Children.Add(column);
        }
        _content.Children.Add(grid);
        AutomationProperties.SetName(_content, "Loading activity for " + scope);
    }

    /// <summary>Paints the calendar. scope names what it covers ("all your projects", a project name).</summary>
    internal void Render(ActivitySummary? activity, string scope, string? error = null)
    {
        _content.Children.Clear();
        AutomationProperties.SetName(_content, "Agent activity");
        if (activity is null)
        {
            _content.Children.Add(StateView.Error("Activity isn't available right now", error is { Length: > 0 } ? error : "This server doesn't report activity yet.", null));
            return;
        }
        var days = activity.Days;
        var cells = ActivityCalendar.Build(DateOnly.FromDateTime(DateTime.Today), days);
        var summary = new Grid();
        summary.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        summary.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var offset = TimeZoneInfo.Local.GetUtcOffset(DateTime.Now);
        var line = new TextBlock { Text = $"{activity.Total:N0} agent messages on {activity.ActiveDays} days in the past year, {scope}", TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true, FontSize = NexusTokens.FontBody, Opacity = 0.8 };
        // How the days are counted is detail, not headline.
        ToolTipService.SetToolTip(line, $"Days use each agent's own message times (UTC{(offset < TimeSpan.Zero ? "−" : "+")}{offset.Duration():hh\\:mm}). Uploading old history doesn't add activity.");
        summary.Children.Add(line);
        var legend = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 4, VerticalAlignment = VerticalAlignment.Center };
        legend.Children.Add(new TextBlock { Text = "Less", FontSize = 10, Opacity = 0.7 });
        for (var level = 0; level < 5; level++) legend.Children.Add(new Border { Width = 10, Height = 10, CornerRadius = new CornerRadius(2), Background = Heat(level) });
        legend.Children.Add(new TextBlock { Text = "More", FontSize = 10, Opacity = 0.7 });
        Grid.SetColumn(legend, 1); summary.Children.Add(legend); _content.Children.Add(summary);
        var calendar = new Grid { RowSpacing = 3, ColumnSpacing = 3, HorizontalAlignment = HorizontalAlignment.Left };
        calendar.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(28) });
        for (var week = 0; week < cells.Count / 7; week++) calendar.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(12) });
        calendar.RowDefinitions.Add(new RowDefinition { Height = new GridLength(20) });
        for (var day = 0; day < 7; day++) calendar.RowDefinitions.Add(new RowDefinition { Height = new GridLength(12) });
        foreach (var (day, label) in new[] { (1, "Mon"), (3, "Wed"), (5, "Fri") })
        {
            var text = new TextBlock { Text = label, FontSize = 9, Opacity = 0.65 };
            Grid.SetRow(text, day + 1); calendar.Children.Add(text);
        }
        var lastMonth = -1;
        for (var i = 0; i < cells.Count; i++)
        {
            var cell = cells[i]; var week = i / 7;
            if (i % 7 == 0 && cell.Date.Month != lastMonth)
            {
                var label = new TextBlock { Text = cell.Date.ToString("MMM"), FontSize = 10, Opacity = 0.7 };
                Grid.SetColumn(label, week + 1); Grid.SetColumnSpan(label, Math.Min(3, cells.Count / 7 - week));
                calendar.Children.Add(label); lastMonth = cell.Date.Month;
            }
            var mark = new Border { Width = 12, Height = 12, CornerRadius = new CornerRadius(2), Background = Heat(ActivityCalendar.Level(cell.Count)), Opacity = cell.InRange ? 1 : 0.15 };
            var hint = $"{cell.Date:dddd, MMM d, yyyy}: {cell.Count} agent message{(cell.Count == 1 ? "" : "s")}";
            ToolTipService.SetToolTip(mark, hint); AutomationProperties.SetName(mark, hint);
            if (cell.Date == DateOnly.FromDateTime(DateTime.Today)) { mark.BorderThickness = new Thickness(1); mark.BorderBrush = NexusPalette.Accent; }
            Grid.SetColumn(mark, week + 1); Grid.SetRow(mark, i % 7 + 1); calendar.Children.Add(mark);
        }
        var scroller = new ScrollViewer { Content = calendar, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled, Padding = new Thickness(0, 0, 0, 6) };
        // Open on the most recent weeks (the right end), not last year.
        scroller.SizeChanged += (_, _) => scroller.ChangeView(scroller.ScrollableWidth, null, null, true);
        _content.Children.Add(scroller);
        if (activity.Undated > 0) _content.Children.Add(new TextBlock { Text = $"{activity.Undated:N0} older messages have no date, so they aren't on the calendar.", Opacity = 0.65, FontSize = 12, TextWrapping = TextWrapping.Wrap });
        if (activity.FailedProjects > 0) _content.Children.Add(new TextBlock { Text = $"Activity for {activity.FailedProjects} project{(activity.FailedProjects == 1 ? "" : "s")} could not be loaded. This calendar may be incomplete.", Foreground = NexusPalette.Amber, FontSize = 12, TextWrapping = TextWrapping.Wrap });
        else if (activity.Partial) _content.Children.Add(new TextBlock { Text = "Some archived history could not be read. This calendar may be incomplete.", Foreground = NexusPalette.Amber, FontSize = 12, TextWrapping = TextWrapping.Wrap });
    }

    private static Brush Heat(int level)
    {
        if (level == 0) return NexusTokens.Brush("NexusSkeletonBrush", Windows.UI.Color.FromArgb(20, 255, 255, 255));
        var color = ((SolidColorBrush)NexusPalette.Accent).Color;
        color.A = new byte[] { 0, 65, 115, 180, 255 }[level];
        return new SolidColorBrush(color);
    }
}
