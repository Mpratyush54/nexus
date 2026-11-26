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

    /// <summary>No projects yet: a friendly start instead of an error.</summary>
    /// <param name="chooseFolders">The "Choose folders" action, or null when the page already offers it.</param>
    internal void RenderNoProjects(Action? chooseFolders)
    {
        _content.Children.Clear();
        AutomationProperties.SetName(_content, "Agent activity");
        _content.Children.Add(StateView.Empty("", "Pick a folder to start",
            "Your agents' activity will show here once Nexus saves chats from a folder you choose.",
            chooseFolders is null ? null : "Choose folders", chooseFolders, primary: true));
    }

    /// <summary>Paints the calendar. scope names what it covers ("all your projects", a project name).</summary>
    /// <param name="retry">Shown as a quiet "Try again" when the calendar's call was slow.</param>
    internal void Render(ActivitySummary? activity, string scope, string? error = null, Action? retry = null)
    {
        _content.Children.Clear();
        AutomationProperties.SetName(_content, "Agent activity");
        if (activity is null)
        {
            _content.Children.Add(StateView.Error("Activity isn't available right now", error is { Length: > 0 } ? error : "This server doesn't report activity yet.", retry, "Activity is taking a moment…"));
            return;
        }
        var days = activity.Days;
        var cells = ActivityCalendar.Build(DateOnly.FromDateTime(DateTime.Today), days);
        var summary = new Grid();
        summary.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        summary.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var offset = TimeZoneInfo.Local.GetUtcOffset(DateTime.Now);
        var line = new TextBlock { Text = $"{Nexus.Core.FriendlyTime.Count(activity.Total, "agent message")} on {Nexus.Core.FriendlyTime.Count(activity.ActiveDays, "day")} in the past year, {scope}", TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true, FontSize = NexusTokens.FontBody, Opacity = 0.8 };
        // How the days are counted is detail, not headline.
        ToolTipService.SetToolTip(line, $"Days use each agent's own message times (UTC{(offset < TimeSpan.Zero ? "−" : "+")}{offset.Duration():hh\\:mm}). Uploading old history doesn't add activity.");
        summary.Children.Add(line);
        var legend = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 4, VerticalAlignment = VerticalAlignment.Center };
        legend.Children.Add(new TextBlock { Text = "Less", FontSize = 10, Opacity = 0.7 });
        for (var level = 0; level < 5; level++) legend.Children.Add(new Border { Width = 10, Height = 10, CornerRadius = new CornerRadius(2), Background = Heat(level) });
        AutomationProperties.SetAccessibilityView(legend, Microsoft.UI.Xaml.Automation.Peers.AccessibilityView.Raw);
        legend.Children.Add(new TextBlock { Text = "More", FontSize = 10, Opacity = 0.7 });
        Grid.SetColumn(legend, 1); summary.Children.Add(legend); _content.Children.Add(summary);
        var scroller = new ScrollViewer { Content = BuildCalendar(cells, activity, scope), HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled, Padding = new Thickness(0, 0, 0, 6) };
        // Open on the most recent weeks (the right end), not last year.
        scroller.SizeChanged += (_, _) => scroller.ChangeView(scroller.ScrollableWidth, null, null, true);
        _content.Children.Add(scroller);
        if (activity.Undated > 0) _content.Children.Add(new TextBlock { Text = $"{activity.Undated:N0} older messages have no date, so they aren't on the calendar.", Opacity = 0.65, FontSize = 12, TextWrapping = TextWrapping.Wrap });
        if (activity.FailedProjects > 0 && retry is not null) _content.Children.Add(new TextBlock { Text = $"Activity for {activity.FailedProjects} project{(activity.FailedProjects == 1 ? " is" : "s are")} taking a moment. Nexus adds {(activity.FailedProjects == 1 ? "it" : "them")} when the server answers.", Opacity = 0.7, FontSize = 12, TextWrapping = TextWrapping.Wrap });
        else if (activity.FailedProjects > 0) _content.Children.Add(new TextBlock { Text = $"Activity for {activity.FailedProjects} project{(activity.FailedProjects == 1 ? "" : "s")} could not be loaded. This calendar may be incomplete.", Foreground = NexusPalette.Amber, FontSize = 12, TextWrapping = TextWrapping.Wrap });
        else if (activity.Pending) _content.Children.Add(new TextBlock { Text = "Still reading older archived chats. The calendar fills in by itself.", Opacity = 0.7, FontSize = 12, TextWrapping = TextWrapping.Wrap });
        else if (activity.Partial) _content.Children.Add(new TextBlock { Text = "Some archived history could not be read. This calendar may be incomplete.", Foreground = NexusPalette.Amber, FontSize = 12, TextWrapping = TextWrapping.Wrap });
    }

    private const double Cell = 12, Gap = 3, Step = Cell + Gap, LabelWidth = 28, HeaderHeight = 20;

    // One element for the whole year: the cells are drawn as a few paths
    // (one per shade) instead of ~370 Borders, so screen readers meet a
    // single "Activity calendar" with a spoken summary, not 365 stops, and
    // the page builds far fewer elements. A day's count shows as a tooltip
    // under the pointer.
    private static FrameworkElement BuildCalendar(IReadOnlyList<ActivityDay> cells, ActivitySummary activity, string scope)
    {
        var weeks = cells.Count / 7;
        var canvas = new Canvas { Width = LabelWidth + weeks * Step, Height = HeaderHeight + 7 * Step, Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent) };
        foreach (var (day, label) in new[] { (1, "Mon"), (3, "Wed"), (5, "Fri") })
        {
            var text = new TextBlock { Text = label, FontSize = 9, Opacity = 0.65 };
            Canvas.SetTop(text, HeaderHeight + day * Step - 1);
            canvas.Children.Add(text);
        }
        var groups = new Dictionary<(int Level, bool InRange), GeometryGroup>();
        var lastMonth = -1;
        var today = DateOnly.FromDateTime(DateTime.Today);
        for (var i = 0; i < cells.Count; i++)
        {
            var cell = cells[i]; var week = i / 7;
            if (i % 7 == 0 && cell.Date.Month != lastMonth)
            {
                var label = new TextBlock { Text = cell.Date.ToString("MMM"), FontSize = 10, Opacity = 0.7 };
                Canvas.SetLeft(label, LabelWidth + week * Step);
                canvas.Children.Add(label); lastMonth = cell.Date.Month;
            }
            var key = (ActivityCalendar.Level(cell.Count), cell.InRange);
            if (!groups.TryGetValue(key, out var group)) groups[key] = group = new GeometryGroup();
            group.Children.Add(new RectangleGeometry { Rect = new Windows.Foundation.Rect(LabelWidth + week * Step, HeaderHeight + (i % 7) * Step, Cell, Cell) });
            if (cell.Date == today)
            {
                var mark = new Microsoft.UI.Xaml.Shapes.Rectangle { Width = Cell, Height = Cell, RadiusX = 2, RadiusY = 2, Stroke = NexusPalette.Accent, StrokeThickness = 1 };
                Canvas.SetLeft(mark, LabelWidth + week * Step); Canvas.SetTop(mark, HeaderHeight + (i % 7) * Step);
                canvas.Children.Add(mark);
            }
        }
        foreach (var ((level, inRange), group) in groups)
            canvas.Children.Insert(0, new Microsoft.UI.Xaml.Shapes.Path { Data = group, Fill = Heat(level), Opacity = inRange ? 1 : 0.15 });
        foreach (var child in canvas.Children) AutomationProperties.SetAccessibilityView(child, Microsoft.UI.Xaml.Automation.Peers.AccessibilityView.Raw);

        var tip = new ToolTip();
        ToolTipService.SetToolTip(canvas, tip);
        canvas.PointerMoved += (_, e) =>
        {
            var at = e.GetCurrentPoint(canvas).Position;
            var week = (int)((at.X - LabelWidth) / Step); var day = (int)((at.Y - HeaderHeight) / Step);
            var index = week * 7 + day;
            tip.Content = at.X >= LabelWidth && at.Y >= HeaderHeight && day is >= 0 and < 7 && index >= 0 && index < cells.Count
                ? DayText(cells[index]) : null;
            tip.Visibility = tip.Content is null ? Visibility.Collapsed : Visibility.Visible;
        };
        AutomationProperties.SetName(canvas, "Activity calendar");
        AutomationProperties.SetFullDescription(canvas, Describe(cells, activity, scope));
        AutomationProperties.SetHelpText(canvas, Describe(cells, activity, scope));
        return canvas;
    }

    private static string DayText(ActivityDay cell) =>
        $"{cell.Date:dddd, MMM d, yyyy}: {(cell.Count == 0 ? "no" : cell.Count.ToString("N0"))} agent message{(cell.Count == 1 ? "" : "s")}";

    private static string Describe(IReadOnlyList<ActivityDay> cells, ActivitySummary activity, string scope)
    {
        var active = cells.Where(c => c.InRange && c.Count > 0).ToList();
        if (active.Count == 0) return $"No agent messages in the past year, {scope}.";
        var busiest = active.MaxBy(c => c.Count)!;
        var latest = active.MaxBy(c => c.Date)!;
        var lastMonth = active.Where(c => c.Date > DateOnly.FromDateTime(DateTime.Today.AddDays(-30))).Sum(c => c.Count);
        return $"{activity.Total:N0} agent messages on {activity.ActiveDays} day{(activity.ActiveDays == 1 ? "" : "s")} in the past year, {scope}. " +
            $"{lastMonth:N0} in the last 30 days. Busiest day: {busiest.Date:MMMM d} with {busiest.Count:N0}. Most recent: {latest.Date:MMMM d}.";
    }

    private static Brush Heat(int level)
    {
        if (level == 0) return NexusTokens.Brush("NexusSkeletonBrush", Windows.UI.Color.FromArgb(20, 255, 255, 255));
        var color = ((SolidColorBrush)NexusPalette.Accent).Color;
        color.A = new byte[] { 0, 65, 115, 180, 255 }[level];
        return new SolidColorBrush(color);
    }
}
