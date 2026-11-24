using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Documents;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using Windows.ApplicationModel.DataTransfer;

namespace Nexus.Pages;

internal static class FileDiffView
{
    internal static UIElement Create(FileDiffPreview change)
    {
        var body = new StackPanel { Spacing = 8, HorizontalAlignment = HorizontalAlignment.Stretch };
        var rows = change.Lines;
        var added = rows.Count(line => line.Kind == "added");
        var removed = rows.Count(line => line.Kind == "removed");
        var header = new Grid { ColumnSpacing = 12 };
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var path = SessionChatRender.Mono(change.Path, 12);
        header.Children.Add(path);
        var stats = new TextBlock { FontSize = 12, VerticalAlignment = VerticalAlignment.Center };
        stats.Inlines.Add(new Run { Text = $"+{added}", Foreground = NexusPalette.Teal });
        stats.Inlines.Add(new Run { Text = $"  −{removed}", Foreground = new SolidColorBrush(Microsoft.UI.ColorHelper.FromArgb(255, 247, 141, 141)) });
        Grid.SetColumn(stats, 1); header.Children.Add(stats);
        var copy = new Button { Content = "Copy diff", FontSize = 11, Padding = new Thickness(8, 3, 8, 3), IsEnabled = change.Diff.Length > 0 };
        copy.Click += (_, _) => { var data = new DataPackage(); data.SetText(change.Diff); Clipboard.SetContent(data); };
        Grid.SetColumn(copy, 2); header.Children.Add(copy);
        body.Children.Add(header);
        if (change.Notice.Length > 0 || change.Diff.Length == 0)
            body.Children.Add(new TextBlock { Text = change.Notice.Length > 0 ? change.Notice : "No text diff is available for this file.", TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true, Foreground = NexusPalette.Amber, FontSize = 12 });
        if (change.Diff.Length > 0)
        {
            var text = SessionChatRender.Mono("", 12);
            text.TextWrapping = TextWrapping.NoWrap;
            text.LineHeight = 20;
            foreach (var row in rows)
            {
                var run = new Run { Text = $"{row.OldLine?.ToString() ?? "",5} {row.NewLine?.ToString() ?? "",5}  {row.Text}\n" };
                if (row.Kind == "added") run.Foreground = NexusPalette.Teal;
                else if (row.Kind == "removed") run.Foreground = new SolidColorBrush(Microsoft.UI.ColorHelper.FromArgb(255, 247, 141, 141));
                else if (row.Kind == "header") run.Foreground = NexusPalette.Accent;
                text.Inlines.Add(run);
            }
            body.Children.Add(new ScrollViewer { Content = text, MaxHeight = 460, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Auto });
        }
        return new Border { Child = body, Background = SessionChatRender.CodeFill, BorderBrush = SessionChatRender.SubtleBorder, BorderThickness = new Thickness(1), CornerRadius = new CornerRadius(8), Padding = new Thickness(12), HorizontalAlignment = HorizontalAlignment.Stretch };
    }
}
