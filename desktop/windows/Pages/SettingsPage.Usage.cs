using System.Globalization;
using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Shapes;
using Nexus.Core;

namespace Nexus.Pages;

// Settings > Usage: tokens and cost across every coding agent, for the
// signed-in user's own chats (usage.summary scope=mine). Today / 7 days /
// 30 days tiles, a 30-day daily chart, and by-agent / by-model lists, with
// a project filter. Self-contained: the Settings layout places
// BuildUsageSection() in its section slot.
public sealed partial class SettingsPage
{
    private const int UsageDays = 30;
    private readonly ComboBox _usageProject = new() { MinWidth = 220, PlaceholderText = "All projects" };
    private readonly StackPanel _usageBody = new() { Spacing = 16 };
    private readonly TextBlock _usageStatus = new() { TextWrapping = TextWrapping.Wrap, FontSize = 12, Opacity = 0.7 };
    private bool _usageProjectsLoaded;
    private int _usageRequest;

    // Fills the Usage slot in the Settings section list (SettingsPage.Sections.cs).
    partial void BuildUsageSectionHook(ref UIElement? section) => section = BuildUsageSection();

    public UIElement BuildUsageSection()
    {
        var refresh = new Button { Content = "Refresh" };
        refresh.Click += (_, _) => _ = LoadUsageAsync();
        _usageProject.SelectionChanged += (_, _) => { if (_usageProjectsLoaded) _ = LoadUsageAsync(); };
        var filters = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8, Children = { _usageProject, refresh } };
        var intro = new TextBlock
        {
            Text = "Tokens and cost across all your coding agents. Cost is what the agent reported, or an estimate from list prices (est.). Models with no known price show tokens only.",
            TextWrapping = TextWrapping.Wrap, FontSize = 12, Opacity = 0.7,
        };
        var root = new StackPanel { Spacing = 12, Children = { intro, filters, _usageStatus, _usageBody } };
        root.Loaded += (_, _) => _ = LoadUsageAsync();
        return root;
    }

    private async Task LoadUsageProjectsAsync()
    {
        if (_usageProjectsLoaded) return;
        var response = await App.Core.CallAsync(NxMethods.ProjectsList);
        await UiThread.Resume(DispatcherQueue);
        _usageProject.Items.Clear();
        _usageProject.Items.Add(new ComboBoxItem { Content = "All projects", Tag = "" });
        if (response.Ok)
            foreach (var project in NxJson.Items(response.Result))
            {
                var id = NxJson.Text(project, "id", "project_id");
                if (id.Length == 0) continue;
                var label = NxJson.Text(project, "display_name", "folder_name", "name");
                _usageProject.Items.Add(new ComboBoxItem { Content = label.Length > 0 ? label : "Project", Tag = id });
            }
        _usageProject.SelectedIndex = 0;
        _usageProjectsLoaded = true;
    }

    private async Task<List<UsageTotal>?> UsageQueryAsync(string groupBy, DateTime fromLocalDate, string project, int offset)
    {
        var response = await App.Core.CallAsync(NxMethods.UsageSummary, new
        {
            group_by = groupBy, scope = "mine", project_id = project,
            from = fromLocalDate.ToString("yyyy-MM-dd", CultureInfo.InvariantCulture), utc_offset_minutes = offset,
        });
        if (!response.Ok) return null;
        var items = new List<UsageTotal>();
        foreach (var item in NxJson.Items(response.Result)) items.Add(UsageTotal.Parse(item));
        return items;
    }

    private async Task LoadUsageAsync()
    {
        var request = ++_usageRequest;
        await LoadUsageProjectsAsync();
        var project = (_usageProject.SelectedItem as ComboBoxItem)?.Tag as string ?? "";
        var offset = (int)TimeZoneInfo.Local.GetUtcOffset(DateTime.Now).TotalMinutes;
        var today = DateTime.Now.Date;
        var from = today.AddDays(-(UsageDays - 1));
        _usageStatus.Text = "Loading usage…";
        var days = UsageQueryAsync("day", from, project, offset);
        var agents = UsageQueryAsync("agent", from, project, offset);
        var models = UsageQueryAsync("model", from, project, offset);
        await Task.WhenAll(days, agents, models);
        await UiThread.Resume(DispatcherQueue);
        if (request != _usageRequest) return;
        _usageBody.Children.Clear();
        if (days.Result is null || agents.Result is null || models.Result is null)
        {
            _usageStatus.Text = "Usage isn't available. Sign in, and check that your Nexus server is up to date.";
            return;
        }
        var byDay = days.Result.ToDictionary(d => d.Key, StringComparer.Ordinal);
        UsageTotal Window(int n)
        {
            var sum = new UsageTotal();
            for (var i = 0; i < n; i++)
                if (byDay.TryGetValue(today.AddDays(-i).ToString("yyyy-MM-dd", CultureInfo.InvariantCulture), out var d)) sum.Add(d);
            return sum;
        }
        var month = Window(UsageDays);
        _usageStatus.Text = month.AllTokens == 0 ? "No usage recorded in the last 30 days." : "";
        _usageBody.Children.Add(new StackPanel
        {
            Orientation = Orientation.Horizontal, Spacing = 12,
            Children = { UsageTile("Today", Window(1)), UsageTile("Last 7 days", Window(7)), UsageTile("Last 30 days", month) },
        });
        if (month.AllTokens == 0) return;
        _usageBody.Children.Add(UsageDailyChart(byDay, today));
        _usageBody.Children.Add(UsageList("By agent (30 days)", agents.Result, t => AgentCatalogName(t.Key)));
        _usageBody.Children.Add(UsageList("By model (30 days)", models.Result, t => t.Key.Length > 0 ? t.Key : "Unknown model"));
    }

    private static string AgentCatalogName(string harness) => harness.Length == 0 ? "Unknown agent"
        : char.ToUpperInvariant(harness[0]) + harness[1..];

    private static Border UsageTile(string label, UsageTotal t)
    {
        var cost = UsageFormat.Cost(t);
        var panel = new StackPanel
        {
            Spacing = 2,
            Children =
            {
                new TextBlock { Text = label, FontSize = 12, Foreground = NexusPalette.Muted },
                new TextBlock { Text = UsageFormat.Tokens(t.AllTokens) + " tokens", FontSize = 20, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold },
                new TextBlock { Text = UsageFormat.Tokens(t.InTokens) + " in · " + UsageFormat.Tokens(t.Output) + " out", FontSize = 12, Foreground = NexusPalette.Muted },
                new TextBlock { Text = cost.Length > 0 ? cost : "No priced usage", FontSize = 12, Foreground = NexusPalette.Muted },
            },
        };
        if (t.UnpricedTokens > 0) ToolTipService.SetToolTip(panel, UsageFormat.Tokens(t.UnpricedTokens) + " tokens are from models with no known price and aren't in the cost.");
        return new Border
        {
            Child = panel, Padding = new Thickness(14, 10, 14, 10), MinWidth = 150,
            CornerRadius = new CornerRadius(8), BorderThickness = new Thickness(1), BorderBrush = NexusPalette.Hairline, Background = NexusPalette.Surface,
        };
    }

    // One series, one hue: tokens per day for 30 days. The y-axis label and
    // its top tick name the scale; first, middle and last dates label the
    // x-axis; each bar has a hover tooltip with the exact day and values.
    private static UIElement UsageDailyChart(Dictionary<string, UsageTotal> byDay, DateTime today)
    {
        const double plotHeight = 120;
        var days = Enumerable.Range(0, UsageDays).Select(i => today.AddDays(i - (UsageDays - 1))).ToList();
        long Value(DateTime d) => byDay.TryGetValue(d.ToString("yyyy-MM-dd", CultureInfo.InvariantCulture), out var t) ? t.AllTokens : 0;
        var max = Math.Max(1, days.Max(Value));

        var bars = new Grid { Height = plotHeight, ColumnSpacing = 2 };
        for (var i = 0; i < days.Count; i++)
        {
            bars.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            var day = days[i];
            var value = Value(day);
            var bar = new Rectangle
            {
                Fill = NexusPalette.Accent, RadiusX = 2, RadiusY = 2, VerticalAlignment = VerticalAlignment.Bottom,
                Height = value == 0 ? 0 : Math.Max(2, plotHeight * value / max),
            };
            // The hit target is the whole column, not just the bar.
            var cell = new Grid { Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), Children = { bar } };
            byDay.TryGetValue(day.ToString("yyyy-MM-dd", CultureInfo.InvariantCulture), out var total);
            var tip = day.ToString("ddd d MMM", CultureInfo.CurrentCulture) + "\n" +
                (total is null ? "No usage" : UsageFormat.Line(total));
            ToolTipService.SetToolTip(cell, tip);
            Grid.SetColumn(cell, i);
            bars.Children.Add(cell);
        }
        var baseline = new Border { Height = 1, Background = NexusPalette.Hairline };

        var xAxis = new Grid();
        xAxis.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        xAxis.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        xAxis.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        TextBlock Axis(string text, HorizontalAlignment align, int col)
        {
            var t = new TextBlock { Text = text, FontSize = 11, Foreground = NexusPalette.Muted, HorizontalAlignment = align };
            Grid.SetColumn(t, col);
            return t;
        }
        xAxis.Children.Add(Axis(days[0].ToString("d MMM", CultureInfo.CurrentCulture), HorizontalAlignment.Left, 0));
        xAxis.Children.Add(Axis(days[UsageDays / 2].ToString("d MMM", CultureInfo.CurrentCulture), HorizontalAlignment.Center, 1));
        xAxis.Children.Add(Axis("Today", HorizontalAlignment.Right, 2));

        var yAxis = new Grid { Height = plotHeight, Width = 44 };
        yAxis.Children.Add(new TextBlock { Text = UsageFormat.Tokens(max), FontSize = 11, Foreground = NexusPalette.Muted, VerticalAlignment = VerticalAlignment.Top, HorizontalAlignment = HorizontalAlignment.Right });
        yAxis.Children.Add(new TextBlock { Text = "0", FontSize = 11, Foreground = NexusPalette.Muted, VerticalAlignment = VerticalAlignment.Bottom, HorizontalAlignment = HorizontalAlignment.Right });

        var plot = new Grid { ColumnSpacing = 8 };
        plot.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        plot.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        plot.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        plot.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        plot.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        Grid.SetColumn(yAxis, 0);
        var column = new StackPanel { Children = { bars, baseline } };
        Grid.SetColumn(column, 1);
        Grid.SetColumn(xAxis, 1);
        Grid.SetRow(xAxis, 1);
        plot.Children.Add(yAxis);
        plot.Children.Add(column);
        plot.Children.Add(xAxis);

        var chart = new StackPanel
        {
            Spacing = 6, MaxWidth = 720, HorizontalAlignment = HorizontalAlignment.Left,
            Children =
            {
                new TextBlock { Text = "Tokens per day (last 30 days)", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold },
                plot,
            },
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(chart, "Daily token usage, last 30 days, peak " + UsageFormat.Tokens(max) + " tokens");
        return chart;
    }

    private static UIElement UsageList(string title, List<UsageTotal> items, Func<UsageTotal, string> name)
    {
        var list = new StackPanel { Spacing = 4, MaxWidth = 720, HorizontalAlignment = HorizontalAlignment.Left };
        list.Children.Add(new TextBlock { Text = title, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
        foreach (var t in items.OrderByDescending(t => t.CostUsd).ThenByDescending(t => t.AllTokens))
        {
            var row = new Grid { ColumnSpacing = 12 };
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(150) });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(110) });
            var label = new TextBlock { Text = name(t), TextTrimming = TextTrimming.CharacterEllipsis };
            var tokens = new TextBlock { Text = UsageFormat.Tokens(t.InTokens) + " in · " + UsageFormat.Tokens(t.Output) + " out", Foreground = NexusPalette.Muted, HorizontalAlignment = HorizontalAlignment.Right };
            var cost = new TextBlock { Text = UsageFormat.Cost(t) is { Length: > 0 } c ? c : "tokens only", Foreground = NexusPalette.Muted, HorizontalAlignment = HorizontalAlignment.Right };
            Grid.SetColumn(tokens, 1);
            Grid.SetColumn(cost, 2);
            row.Children.Add(label);
            row.Children.Add(tokens);
            row.Children.Add(cost);
            ToolTipService.SetToolTip(row, UsageFormat.Detail(t) + (t.Turns > 0 ? "\n" + t.Turns.ToString(CultureInfo.InvariantCulture) + " recorded calls" : ""));
            list.Children.Add(row);
        }
        return list;
    }
}
