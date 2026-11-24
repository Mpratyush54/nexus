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
        ShellState.Changed += (_, _) =>
        {
            var queue = DispatcherQueue;
            queue?.TryEnqueue(Apply);
        };
    }

    protected static void ShowError(StackPanel panel, NxResponse response)
    {
        panel.Children.Clear();
        panel.Children.Add(new TextBlock
        {
            Text = response.Error ?? "Request failed",
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
        return Application.Current?.Resources.TryGetValue(key, out var value) == true && value is Brush brush
            ? brush
            : new SolidColorBrush(fallback);
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
        header.Children.Add(Line(title, title: true));
        header.Children.Add(Muted(subtitle));
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
