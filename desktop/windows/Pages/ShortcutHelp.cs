using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// Ctrl/Cmd+/: every keyboard shortcut, grouped, generated from
/// <see cref="ShortcutRegistry"/> so a new shortcut appears here by itself.
/// macOS shows ⌘ ⌥ ⇧ keycaps.
/// </summary>
internal static class ShortcutHelp
{
    private static bool s_open;

    public static async void Show(MainWindow window)
    {
        if (s_open || window.Content?.XamlRoot is null) return;
        s_open = true;
        try
        {
            var mac = Shortcuts.UseCommand;
            var all = ShortcutRegistry.All(ShellPrefs.QuickAskKeys, mac);
            var body = new StackPanel { Spacing = 14, MinWidth = 460 };
            foreach (var (group, rows) in ShortcutRegistry.Sheet(all))
            {
                var section = new StackPanel { Spacing = 2 };
                section.Children.Add(new TextBlock
                {
                    Text = group, FontSize = NexusTokens.FontCaption, Opacity = 0.6,
                    FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, Margin = new Thickness(0, 0, 0, 4)
                });
                foreach (var (title, keys, rowNote) in rows)
                {
                    var note = keys.Any(k => k.Id == ShortcutRegistry.QuickAsk) && !ShellPrefs.Current.QuickAskEnabled ? "Off · turn it on in Settings › General" : rowNote;
                    section.Children.Add(Row(title, keys, note, mac));
                }
                body.Children.Add(section);
            }
            var dialog = new ContentDialog
            {
                XamlRoot = window.Content.XamlRoot,
                Title = "Keyboard shortcuts",
                Content = new ScrollViewer { Content = body, MaxHeight = 520, Padding = new Thickness(0, 0, 12, 0) },
                CloseButtonText = "Close",
                DefaultButton = ContentDialogButton.Close
            };
            AutomationProperties.SetName(dialog, "Keyboard shortcuts");
            await dialog.ShowThemedAsync();
        }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Warn("shell", "shortcut sheet failed", ex);
        }
        finally
        {
            s_open = false;
        }
    }

    private static FrameworkElement Row(string title, IReadOnlyList<ShortcutRegistry.Shortcut> keys, string note, bool mac)
    {
        var grid = new Grid { ColumnSpacing = 16, Padding = new Thickness(0, 4, 0, 4) };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var text = new StackPanel { VerticalAlignment = VerticalAlignment.Center };
        text.Children.Add(new TextBlock { Text = title, FontSize = NexusTokens.FontBody, TextWrapping = TextWrapping.Wrap });
        if (note.Length > 0) text.Children.Add(new TextBlock { Text = note, FontSize = NexusTokens.FontMicro, Opacity = 0.55, TextWrapping = TextWrapping.Wrap });
        grid.Children.Add(text);
        var caps = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 4, VerticalAlignment = VerticalAlignment.Center };
        for (var i = 0; i < keys.Count; i++)
        {
            if (i > 0) caps.Children.Add(new TextBlock { Text = "or", FontSize = NexusTokens.FontMicro, Opacity = 0.5, VerticalAlignment = VerticalAlignment.Center, Margin = new Thickness(2, 0, 2, 0) });
            foreach (var cap in ShortcutRegistry.Keycaps(keys[i], mac)) caps.Children.Add(Keycap(cap));
        }
        Grid.SetColumn(caps, 1);
        grid.Children.Add(caps);
        AutomationProperties.SetName(grid, title + ": " + string.Join(" or ", keys.Select(k => ShortcutRegistry.Label(k, mac))));
        return grid;
    }

    private static Border Keycap(string text) => new()
    {
        Child = new TextBlock { Text = text, FontSize = NexusTokens.FontCaption, FontFamily = new Microsoft.UI.Xaml.Media.FontFamily("Segoe UI, Segoe UI Symbol, SF Pro Text") },
        MinWidth = 24, Padding = new Thickness(7, 2, 7, 3), CornerRadius = new CornerRadius(5),
        BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1, 1, 1, 2),
        Background = NexusPalette.UserFill
    };
}
