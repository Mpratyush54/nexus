using System.Text.Json;
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

    internal void Render(JsonElement? response)
    {
        _content.Children.Clear();
        _content.Children.Add(new TextBlock { Text = "⌁  Your agent activity", FontSize = 18, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, Foreground = NexusPalette.Amber });
        if (response is not { } data || !data.TryGetProperty("agent_activity", out var activity) || NxJson.Text(activity, "timestamp_basis") != "agent_source_time")
        {
            _content.Children.Add(new TextBlock { Text = "Activity by original agent date is unavailable. Refresh after the server is updated.", TextWrapping = TextWrapping.Wrap });
            return;
        }
        var days = new List<(DateOnly, int)>();
        if (activity.TryGetProperty("days", out var values) && values.ValueKind == JsonValueKind.Array)
            foreach (var value in values.EnumerateArray())
                if (DateOnly.TryParse(NxJson.Text(value, "date"), out var date) && int.TryParse(NxJson.Text(value, "count"), out var count)) days.Add((date, count));
        var cells = ActivityCalendar.Build(DateOnly.FromDateTime(DateTime.Today), days);
        var summary = new Grid();
        summary.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        summary.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        summary.Children.Add(new TextBlock { Text = $"{NxJson.Text(activity, "total")} messages · {NxJson.Text(activity, "active_days")} active days in the last year", TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true });
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
        _content.Children.Add(new ScrollViewer { Content = calendar, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled, Padding = new Thickness(0, 0, 0, 6) });
        var offset = TimeZoneInfo.Local.GetUtcOffset(DateTime.Now);
        _content.Children.Add(new TextBlock { Text = $"Original agent timestamps · UTC{(offset < TimeSpan.Zero ? "−" : "+")}{offset.Duration():hh\\:mm}. Uploading history does not add activity.", FontSize = 12, Opacity = 0.7, TextWrapping = TextWrapping.Wrap });
        var undated = NxJson.Text(activity, "undated_messages");
        if (undated.Length > 0 && undated != "0") _content.Children.Add(new TextBlock { Text = $"{undated} historical messages have no original date and are not placed on the calendar.", Foreground = NexusPalette.Amber, FontSize = 12, TextWrapping = TextWrapping.Wrap });
        if (NxJson.Text(activity, "partial") == "true") _content.Children.Add(new TextBlock { Text = "Some archived history could not be read. This calendar may be incomplete.", Foreground = NexusPalette.Amber, FontSize = 12, TextWrapping = TextWrapping.Wrap });
    }

    private static Brush Heat(int level)
    {
        if (level == 0) return SessionChatRender.SubtleFill;
        var color = ((SolidColorBrush)NexusPalette.Amber).Color;
        color.A = new byte[] { 0, 65, 115, 180, 255 }[level];
        return new SolidColorBrush(color);
    }
}
