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
            FontSize = title ? 18 : 14,
            FontWeight = title ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal
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
}
