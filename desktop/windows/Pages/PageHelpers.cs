using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

public static class UiThread
{
    public static Task Resume(DispatcherQueue? queue)
    {
        if (queue is null || queue.HasThreadAccess)
        {
            return Task.CompletedTask;
        }
        var done = new TaskCompletionSource();
        if (!queue.TryEnqueue(() => done.TrySetResult()))
        {
            done.TrySetResult();
        }
        return done.Task;
    }
}

public class NexusPage : Page
{
    protected NxClient Core => App.Core;
    private bool _entered;

    public NexusPage()
    {
        HorizontalAlignment = HorizontalAlignment.Stretch;
        VerticalAlignment = VerticalAlignment.Stretch;
        Loaded += (_, _) =>
        {
            if (!_entered) Enter(null);
        };
    }

    public void Enter(object? parameter)
    {
        _entered = true;
        OnEnter(parameter);
    }

    protected virtual void OnEnter(object? parameter) { }

    protected override void OnNavigatedTo(Microsoft.UI.Xaml.Navigation.NavigationEventArgs e)
    {
        base.OnNavigatedTo(e);
        Enter(e.Parameter);
    }

    protected async Task<NxResponse> Call(string method, object? args = null)
    {
        var response = await Core.CallAsync(method, args);
        await UiThread.Resume(DispatcherQueue);
        return response;
    }

    protected void CloudOnly(params Control[] controls)
    {
        void Apply()
        {
            var offline = ShellState.Current.Offline;
            foreach (var control in controls)
            {
                control.IsEnabled = !offline;
                ToolTipService.SetToolTip(control, offline ? "Available when online" : null);
            }
        }
        Apply();
        // Subscribed only while on screen: a static event holding the page
        // kept every dropped page alive.
        void Handler(object? _, EventArgs __) => DispatcherQueue?.TryEnqueue(Apply);
        Loaded += (_, _) => { ShellState.Changed += Handler; Apply(); };
        Unloaded += (_, _) => ShellState.Changed -= Handler;
    }

    protected static void ShowError(StackPanel panel, NxResponse response)
    {
        panel.Children.Clear();
        panel.Children.Add(new TextBlock
        {
            Text = NxErrors.Classify(response.Error, Connectivity.Offline) == CallTrouble.Slow
                ? NxErrors.SlowMessage
                : response.Error ?? "Request failed",
            TextWrapping = TextWrapping.Wrap
        });
    }

    protected static TextBlock Line(string text, bool title = false)
    {
        return new TextBlock
        {
            Text = NxJson.Scrub(text),
            TextWrapping = TextWrapping.Wrap,
            IsTextSelectionEnabled = true,
            FontSize = title ? 15 : 13.5,
            FontWeight = title ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal
        };
    }

    protected static TextBlock Muted(string text)
    {
        return new TextBlock
        {
            Text = NxJson.Scrub(text),
            TextWrapping = TextWrapping.Wrap,
            IsTextSelectionEnabled = true,
            FontSize = 12,
            Opacity = 0.65
        };
    }

    protected static TextBlock Monospace(string text)
    {
        return new TextBlock
        {
            Text = NxJson.Scrub(text),
            FontFamily = new FontFamily("Consolas"),
            TextWrapping = TextWrapping.Wrap,
            IsTextSelectionEnabled = true,
            FontSize = 13
        };
    }

    protected static TextBlock SectionHeader(string text)
    {
        return new TextBlock
        {
            Text = NxJson.Scrub(text),
            TextWrapping = TextWrapping.Wrap,
            FontSize = 13,
            FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
            Opacity = 0.7,
            Margin = new Thickness(0, 10, 0, 2)
        };
    }

    protected static Border Chip(string text)
    {
        return new Border
        {
            // Plain muted metadata, not pills (reference: T3 Code headers).
            Padding = new Thickness(0, 0, 10, 0),
            VerticalAlignment = VerticalAlignment.Center,
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent),
            BorderThickness = new Thickness(0),
            Child = new TextBlock { Text = NxJson.Scrub(text), FontSize = 12, Opacity = 0.55 }
        };
    }

    protected static Border ListItem(params UIElement[] children)
    {
        var stack = new StackPanel { Spacing = 6 };
        foreach (var child in children)
        {
            stack.Children.Add(child);
        }
        return new Border
        {
            Padding = new Thickness(14, 10, 14, 10),
            Margin = new Thickness(0, 0, 0, 6),
            HorizontalAlignment = HorizontalAlignment.Stretch,
            CornerRadius = new CornerRadius(9),
            BorderThickness = new Thickness(1),
            Background = ResourceBrush("CardBackgroundFillColorDefaultBrush", Windows.UI.Color.FromArgb(26, 255, 255, 255)),
            BorderBrush = ResourceBrush("CardStrokeColorDefaultBrush", Windows.UI.Color.FromArgb(60, 255, 255, 255)),
            Child = stack
        };
    }

    protected static Border Card(params UIElement[] children)
    {
        var stack = new StackPanel { Spacing = 8 };
        foreach (var child in children)
        {
            stack.Children.Add(child);
        }
        return new Border
        {
            Padding = new Thickness(18, 16, 18, 16),
            Margin = new Thickness(0, 0, 0, 2),
            MinHeight = 96,
            HorizontalAlignment = HorizontalAlignment.Stretch,
            CornerRadius = new CornerRadius(12),
            BorderThickness = new Thickness(1),
            Background = ResourceBrush("CardBackgroundFillColorDefaultBrush", Windows.UI.Color.FromArgb(26, 255, 255, 255)),
            BorderBrush = ResourceBrush("CardStrokeColorDefaultBrush", Windows.UI.Color.FromArgb(60, 255, 255, 255)),
            Child = stack
        };
    }

    private static Brush ResourceBrush(string key, Windows.UI.Color fallback)
    {
        return ThemeService.Brush(key, fallback);
    }

    protected static Button Action(string label, Action click)
    {
        var button = new Button { Content = label, MinHeight = 34, Padding = new Thickness(12, 5, 12, 5) };
        button.Click += (_, _) => click();
        return button;
    }

    protected static StackPanel Row(params UIElement[] children)
    {
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        foreach (var child in children)
        {
            row.Children.Add(child);
        }
        return row;
    }

    protected static StackPanel PageHeader(string title, string subtitle)
    {
        var header = new StackPanel { Spacing = 4 };
        // "Memory & knowledge · central-memory": the page names the project it shows.
        var heading = Line(title, title: true);
        heading.FontSize = NexusTokens.FontPageTitle;
        heading.IsTextSelectionEnabled = false;
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetHeadingLevel(heading, Microsoft.UI.Xaml.Automation.Peers.AutomationHeadingLevel.Level1);
        var context = new Microsoft.UI.Xaml.Documents.Run { Text = "" };
        heading.Inlines.Clear();
        heading.Inlines.Add(new Microsoft.UI.Xaml.Documents.Run { Text = title });
        heading.Inlines.Add(new Microsoft.UI.Xaml.Documents.Run { Text = "" });
        context = (Microsoft.UI.Xaml.Documents.Run)heading.Inlines[1];
        // Project pages show one project. When the sidebar switcher says
        // "All projects" the title can't claim one: it stays "Memory" and a
        // chip says which project is shown and opens the switcher (as Home's
        // "start in" chip does). With one project chosen, the title names it.
        var chipText = new TextBlock { FontSize = NexusTokens.FontCaption, VerticalAlignment = VerticalAlignment.Center, TextTrimming = TextTrimming.CharacterEllipsis, MaxWidth = 260 };
        var chipContent = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        chipContent.Children.Add(new FontIcon { Glyph = "", FontSize = 12, Opacity = 0.7, VerticalAlignment = VerticalAlignment.Center });
        chipContent.Children.Add(chipText);
        chipContent.Children.Add(new FontIcon { Glyph = "", FontSize = 9, Opacity = 0.6, VerticalAlignment = VerticalAlignment.Center });
        var chip = new Button
        {
            Content = chipContent, Padding = new Thickness(8, 3, 8, 4), MinHeight = 26, CornerRadius = new CornerRadius(13),
            VerticalAlignment = VerticalAlignment.Center, Visibility = Visibility.Collapsed
        };
        ToolTipService.SetToolTip(chip, "The sidebar shows all projects; this page shows one. Choose which.");
        chip.Click += (_, _) => MainWindow.Current?.OpenProjectSwitcher(chip);
        void Update(object? _ = null, EventArgs? __ = null)
        {
            var project = ShellState.Current.ProjectName;
            var all = ShellState.Current.ChatFilter.Length == 0;
            context.Text = !all && project.Length > 0 ? " · " + project : "";
            chip.Visibility = all && project.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
            chipText.Text = "Showing " + project;
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(chip, "Showing project " + project + ". Choose another project");
        }
        Update();
        header.Loaded += (_, _) => { ShellState.Changed += Update; ShellState.ChatFilterChanged += Update; Update(); };
        header.Unloaded += (_, _) => { ShellState.Changed -= Update; ShellState.ChatFilterChanged -= Update; };
        var titleRow = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 12 };
        titleRow.Children.Add(heading);
        titleRow.Children.Add(chip);
        header.Children.Add(titleRow);
        var sub = Muted(subtitle);
        sub.FontSize = NexusTokens.FontBody - 0.5;
        sub.IsTextSelectionEnabled = false;
        header.Children.Add(sub);
        return header;
    }

    protected void WatchShellProject(Func<Task> reload)
    {
        async void Handler(object? _, EventArgs __) => await reload();
        Loaded += (_, _) => ShellState.Changed += Handler;
        Unloaded += (_, _) => ShellState.Changed -= Handler;
    }

    protected string ShellProjectId() => ShellState.Current.ProjectId;

    protected string ShellProjectLabel()
    {
        var state = ShellState.Current;
        return state.ProjectName.Length > 0 ? state.ProjectName : state.ProjectId;
    }
}
