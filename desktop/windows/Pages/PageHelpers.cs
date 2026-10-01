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
        Loaded += (_, _) =>
        {
            if (!_entered)
            {
                Enter(null);
            }
        };
    }

    /// <summary>Show this page without Frame.Navigate (avoids WinRT AV without full Appx/PRI tooling).</summary>
    public void Enter(object? parameter)
    {
        _entered = true;
        OnEnter(parameter);
    }

    protected virtual void OnEnter(object? parameter)
    {
    }

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
            FontSize = title ? 13 : 12,
            FontWeight = title ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal
        };
    }

    protected static TextBlock PageHeading(string text) => new()
    {
        Text = text,
        FontSize = 22,
        FontWeight = Microsoft.UI.Text.FontWeights.SemiBold
    };

    protected static TextBlock Muted(string text, double size = 12)
    {
        return new TextBlock
        {
            Text = NxJson.Scrub(text),
            TextWrapping = TextWrapping.Wrap,
            FontSize = size,
            Opacity = 0.72
        };
    }

    /// <summary>Content column capped so pages don’t stretch into empty space on wide windows.</summary>
    protected static FrameworkElement PageBody(UIElement content, double maxWidth = 720)
    {
        return new Border
        {
            MaxWidth = maxWidth,
            HorizontalAlignment = HorizontalAlignment.Left,
            Child = content
        };
    }

    protected static Border Card(params UIElement[] children)
    {
        var stack = new StackPanel { Spacing = 4 };
        foreach (var child in children)
        {
            stack.Children.Add(child);
        }
        return new Border
        {
            Padding = new Thickness(12, 10, 12, 10),
            Margin = new Thickness(0, 0, 0, 4),
            CornerRadius = new CornerRadius(6),
            BorderThickness = new Thickness(1),
            BorderBrush = new SolidColorBrush(Microsoft.UI.Colors.Gray) { Opacity = 0.55 },
            Background = new SolidColorBrush(Microsoft.UI.ColorHelper.FromArgb(28, 255, 255, 255)),
            Child = stack
        };
    }

    /// <summary>A deliberate state surface instead of an unexplained blank page.</summary>
    protected static Border EmptyState(string title, string detail, FrameworkElement? action = null)
    {
        var stack = new StackPanel { Spacing = 8 };
        stack.Children.Add(new TextBlock
        {
            Text = title,
            FontSize = 16,
            FontWeight = Microsoft.UI.Text.FontWeights.SemiBold
        });
        stack.Children.Add(Muted(detail, 13));
        if (action is not null)
        {
            action.Margin = new Thickness(0, 4, 0, 0);
            stack.Children.Add(action);
        }
        return new Border
        {
            Padding = new Thickness(20),
            CornerRadius = new CornerRadius(10),
            BorderThickness = new Thickness(1),
            BorderBrush = new SolidColorBrush(Microsoft.UI.Colors.Gray) { Opacity = 0.45 },
            Background = new SolidColorBrush(Microsoft.UI.ColorHelper.FromArgb(32, 255, 255, 255)),
            Child = stack
        };
    }

    /// <summary>Compact list row: primary + optional secondary on the left, trailing control on the right.</summary>
    protected static Border DenseRow(string primary, string? secondary = null, FrameworkElement? trailing = null)
    {
        var labels = new StackPanel { Spacing = 2, VerticalAlignment = VerticalAlignment.Center };
        labels.Children.Add(new TextBlock
        {
            Text = NxJson.Scrub(primary),
            FontSize = 13,
            FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
            TextTrimming = TextTrimming.CharacterEllipsis
        });
        if (!string.IsNullOrWhiteSpace(secondary))
        {
            labels.Children.Add(Muted(secondary!));
        }

        var grid = new Grid { ColumnSpacing = 12 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        Grid.SetColumn(labels, 0);
        grid.Children.Add(labels);
        if (trailing is not null)
        {
            trailing.VerticalAlignment = VerticalAlignment.Center;
            Grid.SetColumn(trailing, 1);
            grid.Children.Add(trailing);
        }

        return new Border
        {
            Padding = new Thickness(12, 8, 12, 8),
            Margin = new Thickness(0, 0, 0, 2),
            CornerRadius = new CornerRadius(6),
            BorderThickness = new Thickness(1),
            BorderBrush = new SolidColorBrush(Microsoft.UI.Colors.Gray) { Opacity = 0.45 },
            Child = grid
        };
    }

    /// <summary>Settings-style row: title/description left, control right.</summary>
    protected static Border SettingRow(string title, string? description, FrameworkElement control)
    {
        var labels = new StackPanel { Spacing = 2, VerticalAlignment = VerticalAlignment.Center };
        labels.Children.Add(new TextBlock
        {
            Text = title,
            FontSize = 13,
            FontWeight = Microsoft.UI.Text.FontWeights.SemiBold
        });
        if (!string.IsNullOrWhiteSpace(description))
        {
            labels.Children.Add(Muted(description!));
        }

        var grid = new Grid { ColumnSpacing = 16 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        Grid.SetColumn(labels, 0);
        Grid.SetColumn(control, 1);
        control.VerticalAlignment = VerticalAlignment.Center;
        grid.Children.Add(labels);
        grid.Children.Add(control);

        return new Border
        {
            Padding = new Thickness(14, 10, 14, 10),
            BorderThickness = new Thickness(0, 0, 0, 1),
            BorderBrush = new SolidColorBrush(Microsoft.UI.Colors.Gray) { Opacity = 0.35 },
            Child = grid
        };
    }

    protected static Button Action(string label, Action click)
    {
        var button = new Button { Content = label, Padding = new Thickness(12, 4, 12, 4) };
        button.Click += (_, _) => click();
        return button;
    }

    protected static StackPanel Row(params UIElement[] children)
    {
        var row = new StackPanel
        {
            Orientation = Orientation.Horizontal,
            Spacing = 8,
            VerticalAlignment = VerticalAlignment.Center
        };
        foreach (var child in children)
        {
            row.Children.Add(child);
        }
        return row;
    }
}
