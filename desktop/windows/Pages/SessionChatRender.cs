using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using Windows.ApplicationModel.DataTransfer;

namespace Nexus.Pages;

/// <summary>Shared chat transcript visuals (Session tab only).</summary>
internal static class SessionChatRender
{
    internal const double TurnSpacing = 7;
    internal const double AuxRowHeight = 26;
    internal static readonly Thickness AuxIndent = new(10, 0, 0, 0);
    internal static readonly Thickness UserBubblePad = new(14, 9, 14, 10);
    internal static readonly Thickness AssistantPad = new(2, 4, 8, 4);

    // ── Markdown tables ──────────────────────────────────────────────────
    internal static bool IsTableRow(string line)
    {
        var t = line.Trim();
        return t.Length > 2 && t[0] == '|' && t.IndexOf('|', 1) > 0;
    }

    internal static bool IsTableRule(string line) =>
        System.Text.RegularExpressions.Regex.IsMatch(line.Trim(), @"^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?$");

    internal static List<string> TableCells(string row)
    {
        var t = row.Trim();
        if (t.StartsWith('|')) t = t[1..];
        if (t.EndsWith('|')) t = t[..^1];
        // An escaped \| belongs to the cell text.
        return System.Text.RegularExpressions.Regex.Split(t, @"(?<!\\)\|").Select(c => c.Trim().Replace("\\|", "|")).ToList();
    }

    /// A table as a grid: bold header, wrapping cells, hairline row rules.
    internal static FrameworkElement Table(List<string> rows, Action<Microsoft.UI.Xaml.Documents.InlineCollection, string> format)
    {
        var cells = rows.Select(TableCells).ToList();
        var columns = cells.Max(r => r.Count);
        var grid = new Grid { ColumnSpacing = 18, MaxWidth = 900, HorizontalAlignment = HorizontalAlignment.Left };
        for (var c = 0; c < columns; c++)
            grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Auto), MaxWidth = 420 });
        for (var r = 0; r < cells.Count; r++)
        {
            grid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
            var rule = new Border { BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(0, 0, 0, 1), Margin = new Thickness(0, 0, -18, 0) };
            Grid.SetRow(rule, r);
            Grid.SetColumnSpan(rule, columns);
            grid.Children.Add(rule);
            for (var c = 0; c < columns; c++)
            {
                var block = new RichTextBlock { IsTextSelectionEnabled = true, TextWrapping = TextWrapping.Wrap, FontSize = 13.5, LineHeight = 20, Margin = new Thickness(0, 6, 0, 6) };
                var p = new Microsoft.UI.Xaml.Documents.Paragraph();
                if (r == 0) p.FontWeight = Microsoft.UI.Text.FontWeights.SemiBold;
                var text = c < cells[r].Count ? cells[r][c] : "";
                format(p.Inlines, text.Length > 0 ? text : " ");
                block.Blocks.Add(p);
                Grid.SetRow(block, r);
                Grid.SetColumn(block, c);
                grid.Children.Add(block);
            }
        }
        return grid;
    }

    internal static Brush SubtleFill =>
        ResourceBrush("ControlFillColorSecondaryBrush", Windows.UI.Color.FromArgb(28, 255, 255, 255));

    internal static Brush SubtleBorder =>
        ResourceBrush("ControlStrokeColorDefaultBrush", Windows.UI.Color.FromArgb(40, 255, 255, 255));

    internal static Brush UserBubbleFill =>
        NexusPalette.UserFill;

    internal static Brush CodeFill =>
        ResourceBrush("CardBackgroundFillColorSecondaryBrush", Windows.UI.Color.FromArgb(48, 0, 0, 0));

    internal static Brush AuxAccent =>
        new SolidColorBrush(Windows.UI.Color.FromArgb(90, 255, 255, 255));

    internal static TextBlock KindLabel(string text) => new()
    {
        Text = text,
        FontSize = 10,
        FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
        Opacity = 0.48,
        VerticalAlignment = VerticalAlignment.Center,
        MinWidth = 42
    };

    internal static TextBlock RoleLabel(string text) => new()
    {
        Text = text,
        FontSize = 11,
        FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
        Opacity = 0.52,
        Margin = new Thickness(0, 0, 0, 2)
    };

    internal static TextBlock Hint(string text) => new()
    {
        Text = text,
        FontSize = 11,
        Opacity = 0.52,
        TextTrimming = TextTrimming.CharacterEllipsis,
        VerticalAlignment = VerticalAlignment.Center,
        MaxWidth = 360
    };

    internal static TextBlock Mono(string text, double size = 12) => new()
    {
        Text = text,
        FontFamily = new FontFamily("Consolas"),
        FontSize = size,
        TextWrapping = TextWrapping.Wrap,
        IsTextSelectionEnabled = true,
        Opacity = 0.92
    };

    internal static TextBlock MonoLine(string text, double size = 11.5) => new()
    {
        Text = text,
        FontFamily = new FontFamily("Consolas"),
        FontSize = size,
        TextTrimming = TextTrimming.CharacterEllipsis,
        VerticalAlignment = VerticalAlignment.Center,
        IsTextSelectionEnabled = true,
        Opacity = 0.78
    };

    internal static UIElement CodeBlock(string text)
    {
        var copy = new Button { Content = "Copy", FontSize = 11, Padding = new Thickness(8, 3, 8, 3), HorizontalAlignment = HorizontalAlignment.Right };
        copy.Click += (_, _) =>
        {
            var data = new DataPackage(); data.SetText(text); Clipboard.SetContent(data);
        };
        var code = Mono(text, 13);
        code.TextWrapping = TextWrapping.NoWrap;
        code.LineHeight = 20;
        var content = new StackPanel { Spacing = 6 };
        content.Children.Add(copy);
        content.Children.Add(new ScrollViewer
        {
            Content = code, MaxHeight = 360,
            HorizontalScrollBarVisibility = ScrollBarVisibility.Auto,
            VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
            Padding = new Thickness(0, 0, 0, 6)
        });
        return new Border
        {
            Child = content, Padding = new Thickness(14, 10, 14, 10), CornerRadius = new CornerRadius(8),
            Background = CodeFill, BorderBrush = SubtleBorder, BorderThickness = new Thickness(1),
            HorizontalAlignment = HorizontalAlignment.Stretch
        };
    }

    internal static Border PathChip(string path, string glyph = "·")
    {
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        row.Children.Add(new TextBlock
        {
            Text = glyph,
            FontSize = 11,
            Opacity = 0.55,
            VerticalAlignment = VerticalAlignment.Center,
            FontFamily = new FontFamily("Consolas")
        });
        row.Children.Add(new TextBlock
        {
            Text = path,
            FontFamily = new FontFamily("Consolas"),
            FontSize = 11.5,
            TextTrimming = TextTrimming.CharacterEllipsis,
            MaxWidth = 520,
            VerticalAlignment = VerticalAlignment.Center,
            IsTextSelectionEnabled = true
        });
        return new Border
        {
            Padding = new Thickness(8, 4, 10, 5),
            CornerRadius = new CornerRadius(6),
            Background = SubtleFill,
            BorderBrush = SubtleBorder,
            BorderThickness = new Thickness(1),
            HorizontalAlignment = HorizontalAlignment.Left,
            Child = row
        };
    }

    internal static UIElement CompactAuxHeader(string kind, string primary, bool monospace = false)
    {
        var row = new Grid
        {
            ColumnSpacing = 8,
            VerticalAlignment = VerticalAlignment.Center,
            MinHeight = AuxRowHeight,
            MaxHeight = AuxRowHeight
        };
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        row.Children.Add(new TextBlock
        {
            Text = kind,
            // Work-log style (reference: T3 Code): the action in quiet grey.
            FontSize = 12,
            FontWeight = Microsoft.UI.Text.FontWeights.Medium,
            Foreground = NexusPalette.Teal,
            Opacity = 0.9,
            VerticalAlignment = VerticalAlignment.Center,
            MinWidth = 28
        });
        var label = monospace
            ? MonoLine(primary)
            : new TextBlock
            {
                Text = NxJson.Scrub(primary),
                FontSize = 12,
                TextTrimming = TextTrimming.CharacterEllipsis,
                VerticalAlignment = VerticalAlignment.Center,
                Opacity = 0.82,
                IsTextSelectionEnabled = true
            };
        Grid.SetColumn(label, 1);
        row.Children.Add(label);
        return row;
    }

    internal static UIElement CompactExpander(UIElement header, UIElement detail)
    {
        return new Expander
        {
            Header = header,
            Content = new Border
            {
                Margin = new Thickness(12, 2, 0, 2),
                Padding = new Thickness(8, 6, 8, 6),
                Background = CodeFill,
                CornerRadius = new CornerRadius(4),
                Child = new ScrollViewer
                {
                    Content = detail,
                    MaxHeight = 440,
                    VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
                    HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled
                }
            },
            IsExpanded = false,
            HorizontalAlignment = HorizontalAlignment.Stretch,
            HorizontalContentAlignment = HorizontalAlignment.Stretch,
            Padding = new Thickness(0),
            MinHeight = AuxRowHeight,
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent)
        };
    }

    internal static UIElement WrapAux(UIElement line) => new Border
    {
        Margin = AuxIndent,
        MinHeight = AuxRowHeight,
        Child = line
    };

    internal static StackPanel AuxHeader(string kind, string title, string hint)
    {
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        row.Children.Add(KindLabel(kind));
        row.Children.Add(new TextBlock
        {
            Text = title,
            FontSize = 12.5,
            FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
            TextTrimming = TextTrimming.CharacterEllipsis,
            MaxWidth = 260,
            VerticalAlignment = VerticalAlignment.Center
        });
        if (hint.Length > 0) row.Children.Add(Hint(hint));
        return row;
    }

    /// A message the harness delivered for an agent (a sub-agent's report, a
    /// background task's notification): one quiet row in work-log style,
    /// collapsed, never a bubble from the person.
    internal static UIElement EnvelopeCard(HarnessEnvelope envelope) =>
        CollapsedCard(envelope.Kind == EnvelopeKind.TaskNotification ? "\uEA8F" : WorkLogText.Glyph(StepKind.Agent),
            envelope.Label, envelope.Title, envelope.Status is "completed" or "" ? "" : envelope.Status, envelope.IsError, envelope.Body);

    /// A compaction summary ("This session is being continued from a
    /// previous conversation…"): the harness wrote it, so it is one collapsed
    /// row, opened on demand, not a message from the person.
    internal static UIElement SummaryCard(string text)
    {
        var body = text.Replace("\r\n", "\n").Trim();
        // The lead paragraph only says what this is; the row's label says it too.
        var split = body.IndexOf("\n\n", StringComparison.Ordinal);
        if (split > 0 && TranscriptDedupe.IsCompactionSummary(body[..split])) body = body[(split + 2)..].Trim();
        return CollapsedCard("\uE8BD", "Conversation summary", "", "", false, body);
    }

    /// A one-line harness note in the transcript ("Request interrupted by user").
    internal static UIElement SystemNote(string text)
    {
        var note = new TextBlock
        {
            Text = text, FontSize = 12, Foreground = NexusPalette.Muted, Opacity = 0.9,
            TextTrimming = TextTrimming.CharacterEllipsis, MaxLines = 1, IsTextSelectionEnabled = true,
            HorizontalAlignment = HorizontalAlignment.Right
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(note, text);
        return new Border { Child = note, HorizontalAlignment = HorizontalAlignment.Right, Margin = new Thickness(0, 0, 4, 2) };
    }

    // One quiet row in work-log style that expands to show `body`.
    private static UIElement CollapsedCard(string glyph, string label, string title, string status, bool error, string body)
    {
        var icon = new FontIcon
        {
            Glyph = glyph,
            FontFamily = WorkLogVisuals.IconFont, FontSize = 12,
            Foreground = error ? NexusPalette.Danger : NexusPalette.Muted,
            VerticalAlignment = VerticalAlignment.Center, HorizontalAlignment = HorizontalAlignment.Center
        };
        var text = new TextBlock
        {
            FontSize = 12.5, TextTrimming = TextTrimming.CharacterEllipsis, TextWrapping = TextWrapping.NoWrap,
            MaxLines = 1, VerticalAlignment = VerticalAlignment.Center
        };
        text.Inlines.Add(new Microsoft.UI.Xaml.Documents.Run { Text = label });
        if (title.Length > 0)
            text.Inlines.Add(new Microsoft.UI.Xaml.Documents.Run { Text = " " + title, Foreground = NexusPalette.Muted });
        var statusText = new TextBlock
        {
            Text = status,
            FontSize = 11.5, VerticalAlignment = VerticalAlignment.Center,
            Foreground = error ? NexusPalette.Danger : NexusPalette.Muted
        };
        var chevron = WorkLogVisuals.Chevron();
        chevron.Opacity = body.Length > 0 ? 0.7 : 0;
        var line = new Grid { ColumnSpacing = 8 };
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(16) });
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(10) });
        line.Children.Add(icon);
        Grid.SetColumn(text, 1); line.Children.Add(text);
        Grid.SetColumn(statusText, 2); line.Children.Add(statusText);
        Grid.SetColumn(chevron, 3); line.Children.Add(chevron);
        var button = WorkLogVisuals.RowButton(line);
        var name = (label + ": " + title).Trim(' ', ':');
        ToolTipService.SetToolTip(button, name);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(button, name);
        var detail = new Border { Visibility = Visibility.Collapsed, Margin = new Thickness(30, 2, 6, 8) };
        button.Click += (_, _) =>
        {
            if (body.Length == 0) return;
            detail.Child ??= WorkLogVisuals.OutputBlock(body, 12, mono: false, error: error);
            detail.Visibility = detail.Visibility == Visibility.Visible ? Visibility.Collapsed : Visibility.Visible;
            chevron.Glyph = detail.Visibility == Visibility.Visible ? WorkLogVisuals.ChevronDown : WorkLogVisuals.ChevronRight;
        };
        var stack = new StackPanel { HorizontalAlignment = HorizontalAlignment.Stretch, Margin = new Thickness(0, 2, 0, 2) };
        stack.Children.Add(button);
        stack.Children.Add(detail);
        return stack;
    }

    internal static Border CodePanel(UIElement content) => new()
    {
        Background = CodeFill,
        CornerRadius = new CornerRadius(6),
        Padding = new Thickness(10, 8, 10, 8),
        BorderBrush = SubtleBorder,
        BorderThickness = new Thickness(1),
        Child = content
    };

    private static string Truncate(string value, int max)
    {
        if (value.Length <= max)
        {
            return value;
        }
        return value[..max] + "…";
    }

    private static Brush ResourceBrush(string key, Windows.UI.Color fallback)
    {
        return Application.Current?.Resources.TryGetValue(key, out var value) == true && value is Brush brush
            ? brush
            : new SolidColorBrush(fallback);
    }
}
