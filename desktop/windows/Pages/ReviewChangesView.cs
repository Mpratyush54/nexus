using Microsoft.UI.Text;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Documents;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using System.Text.Json;
using Windows.System;

namespace Nexus.Pages;

/// <summary>
/// "Review changes (3 files, +24 −7)" under a turn that changed files. The
/// card opens in place into the review: a file list, then each file's
/// unified hunks with syntax tint. Keep or Undo works per hunk (on hover or
/// focus), per file and for everything; Ctrl/Cmd+Enter keeps all. Undo
/// writes the earlier text back into the project folder, from the snapshot
/// the core took before the turn (internal/checkpoint). Every action returns
/// the review as it now stands, and the view is rebuilt from that.
/// </summary>
internal sealed class ReviewChangesView
{
    private const int MaxLinesPerFile = 400;
    private readonly string _checkpointId;
    private readonly Func<string, object, Task<NxResponse>> _call;
    private readonly Border _root;
    private readonly TextBlock _title = new() { FontSize = 13.5, FontWeight = FontWeights.SemiBold, VerticalAlignment = VerticalAlignment.Center, TextTrimming = TextTrimming.CharacterEllipsis };
    private readonly TextBlock _subtitle = new() { FontSize = 12, VerticalAlignment = VerticalAlignment.Center, Foreground = NexusPalette.Muted };
    private readonly FontIcon _icon = new() { Glyph = "", FontSize = 15, FontFamily = WorkLogVisuals.IconFont, VerticalAlignment = VerticalAlignment.Center };
    private readonly Button _open = new() { MinWidth = 0, Padding = new Thickness(12, 5, 12, 5), CornerRadius = new CornerRadius(8) };
    private readonly Button _keepAll = new() { Content = "Keep all", MinWidth = 0, Padding = new Thickness(12, 5, 12, 5), CornerRadius = new CornerRadius(8) };
    private readonly Button _undoAll = new() { Content = "Undo all", MinWidth = 0, Padding = new Thickness(12, 5, 12, 5), CornerRadius = new CornerRadius(8) };
    private readonly StackPanel _body = new() { Spacing = 10, Visibility = Visibility.Collapsed, Margin = new Thickness(0, 12, 0, 0) };
    private readonly TextBlock _status = new() { FontSize = 12, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed, Foreground = NexusPalette.Amber };
    private ChangeReview? _review;
    private bool _open_;
    private bool _busy;

    public ReviewChangesView(string checkpointId, int files, Func<string, object, Task<NxResponse>> call)
    {
        _checkpointId = checkpointId;
        _call = call;
        _title.Text = ChangeReview.Placeholder(files);
        _subtitle.Text = "Keep or undo what the agent changed";
        _open.Content = "Review";
        _open.Style = Application.Current.Resources["AccentButtonStyle"] as Style;
        AutomationProperties.SetName(_open, "Review changes");
        ToolTipService.SetToolTip(_keepAll, "Keep all · " + Shortcuts.Label("Enter"));
        ToolTipService.SetToolTip(_undoAll, "Undo all · put every file back as it was before this turn");
        _open.Click += (_, _) => Toggle();
        _keepAll.Click += async (_, _) => await DecideAsync(true, "", -1, "");
        _undoAll.Click += async (_, _) => await DecideAsync(false, "", -1, "");

        var head = new Grid { ColumnSpacing = 12 };
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var badge = new Border
        {
            Width = 32, Height = 32, CornerRadius = new CornerRadius(9), Background = NexusPalette.AccentFill,
            Child = _icon, VerticalAlignment = VerticalAlignment.Center,
        };
        _icon.Foreground = NexusPalette.Accent;
        _icon.HorizontalAlignment = HorizontalAlignment.Center;
        head.Children.Add(badge);
        var words = new StackPanel { Spacing = 1, VerticalAlignment = VerticalAlignment.Center };
        words.Children.Add(_title);
        words.Children.Add(_subtitle);
        Grid.SetColumn(words, 1);
        head.Children.Add(words);
        var buttons = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6, VerticalAlignment = VerticalAlignment.Center };
        buttons.Children.Add(_undoAll);
        buttons.Children.Add(_keepAll);
        buttons.Children.Add(_open);
        Grid.SetColumn(buttons, 2);
        head.Children.Add(buttons);

        var stack = new StackPanel();
        stack.Children.Add(head);
        stack.Children.Add(_status);
        stack.Children.Add(_body);
        _root = new Border
        {
            Child = stack, Padding = new Thickness(12, 10, 12, 10), CornerRadius = new CornerRadius(12),
            BorderThickness = new Thickness(1), BorderBrush = SessionChatRender.SubtleBorder, Background = SessionChatRender.CodeFill,
            Margin = new Thickness(2, 4, 8, 10), HorizontalAlignment = HorizontalAlignment.Stretch, Tag = ReviewTag,
        };
        AutomationProperties.SetName(_root, "Changes from this turn");
        // Ctrl/Cmd+Enter anywhere in the review keeps everything. Buttons
        // handle Enter themselves, so handled events are listened to too.
        _root.AddHandler(UIElement.KeyDownEvent, new KeyEventHandler(OnKeyDown), true);
        SetButtonsVisible(false);
    }

    public const string ReviewTag = "nexus-review-changes";

    public FrameworkElement View => _root;

    public string CheckpointId => _checkpointId;

    /// Loads the review (counts, files) without opening it.
    public async Task LoadAsync()
    {
        var response = await _call("live.review", new { checkpoint_id = _checkpointId });
        if (response.Ok && response.Result is { } result) Show(ChangeReview.Parse(result));
        else if (response.Error is { Length: > 0 })
        {
            _subtitle.Text = "The earlier versions are no longer kept";
            _open.IsEnabled = false;
        }
    }

    /// Opens the review (used by the turn's card and keyboard).
    public void Open()
    {
        if (!_open_) Toggle();
    }

    private void Toggle()
    {
        _open_ = !_open_;
        _open.Content = _open_ ? "Close" : "Review";
        AutomationProperties.SetName(_open, _open_ ? "Close review" : "Review changes");
        if (_open_)
        {
            Render();
            _body.Visibility = Visibility.Visible;
            Motion.FadeIn(_body, rise: 6);
            if (_review is null) _ = LoadAsync();
        }
        else
        {
            _body.Visibility = Visibility.Collapsed;
        }
    }

    private void SetButtonsVisible(bool pending)
    {
        _keepAll.Visibility = _undoAll.Visibility = pending ? Visibility.Visible : Visibility.Collapsed;
    }

    private void Show(ChangeReview review)
    {
        _review = review;
        _title.Text = review.Title;
        if (review.Settled)
        {
            _subtitle.Text = review.Summary + " · every change kept or undone";
            _icon.Glyph = "";
            _icon.Foreground = NexusPalette.DiffAdded;
            _open.Visibility = Visibility.Collapsed;
            SetButtonsVisible(false);
            if (_open_) { _open_ = false; _body.Visibility = Visibility.Collapsed; }
            AutomationProperties.SetName(_root, "Changes reviewed");
            return;
        }
        _subtitle.Text = "Keep or undo what the agent changed";
        _open.Visibility = Visibility.Visible;
        SetButtonsVisible(true);
        if (_open_) Render();
    }

    private void Render()
    {
        _body.Children.Clear();
        if (_review is not { } review)
        {
            _body.Children.Add(new ProgressRing { IsActive = true, Width = 20, Height = 20, HorizontalAlignment = HorizontalAlignment.Left });
            return;
        }
        // File list first: one row per file with its own Keep / Undo.
        var list = new StackPanel { Spacing = 2 };
        var sections = new List<(ReviewFile File, FrameworkElement Section)>();
        foreach (var file in review.Files)
        {
            var section = FileSection(file);
            sections.Add((file, section));
            list.Children.Add(FileRow(file, section));
        }
        _body.Children.Add(new Border
        {
            Child = list, CornerRadius = new CornerRadius(8), Padding = new Thickness(4),
            BorderThickness = new Thickness(1), BorderBrush = SessionChatRender.SubtleBorder,
        });
        foreach (var (_, section) in sections) _body.Children.Add(section);
        _body.Children.Add(new TextBlock
        {
            Text = "Undo puts the earlier text back in your project folder. " + Shortcuts.Label("Enter") + " keeps all.",
            FontSize = 11.5, Foreground = NexusPalette.Muted, TextWrapping = TextWrapping.Wrap,
        });
    }

    private FrameworkElement FileRow(ReviewFile file, FrameworkElement section)
    {
        var row = new Grid { ColumnSpacing = 10, Padding = new Thickness(8, 4, 4, 4), CornerRadius = new CornerRadius(6), Background = Transparent };
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        row.Children.Add(StatusBadge(file));
        var name = new HyperlinkButton { Padding = new Thickness(0), HorizontalAlignment = HorizontalAlignment.Left };
        var nameText = new TextBlock { TextTrimming = TextTrimming.CharacterEllipsis, FontSize = 12.5 };
        nameText.Inlines.Add(new Run { Text = file.FileName, FontWeight = FontWeights.SemiBold });
        if (file.Folder.Length > 0) nameText.Inlines.Add(new Run { Text = "  " + file.Folder, Foreground = NexusPalette.Muted });
        name.Content = nameText;
        ToolTipService.SetToolTip(name, file.Path + " · " + file.StatusLabel);
        AutomationProperties.SetName(name, file.StatusLabel + ": " + file.Path);
        name.Click += (_, _) => section.StartBringIntoView(new BringIntoViewOptions { AnimationDesired = Motion.Enabled, VerticalAlignmentRatio = 0.1 });
        Grid.SetColumn(name, 1);
        row.Children.Add(name);
        var stats = Stats(file.Added, file.Removed);
        stats.VerticalAlignment = VerticalAlignment.Center;
        Grid.SetColumn(stats, 2);
        row.Children.Add(stats);
        var actions = Actions(file, -1, "", small: true);
        Grid.SetColumn(actions, 3);
        row.Children.Add(actions);
        row.PointerEntered += (_, _) => row.Background = SessionChatRender.SubtleFill;
        row.PointerExited += (_, _) => row.Background = Transparent;
        return row;
    }

    private FrameworkElement FileSection(ReviewFile file)
    {
        var stack = new StackPanel { Spacing = 0 };
        var head = new Grid { ColumnSpacing = 10, Padding = new Thickness(10, 7, 6, 7), Background = CodePalette.HeaderFill };
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        head.Children.Add(StatusBadge(file));
        var path = SessionChatRender.Mono(file.Path, 12);
        path.VerticalAlignment = VerticalAlignment.Center;
        path.TextTrimming = TextTrimming.CharacterEllipsis;
        Grid.SetColumn(path, 1);
        head.Children.Add(path);
        var stats = Stats(file.Added, file.Removed);
        stats.VerticalAlignment = VerticalAlignment.Center;
        Grid.SetColumn(stats, 2);
        head.Children.Add(stats);
        var actions = Actions(file, -1, "", small: false);
        Grid.SetColumn(actions, 3);
        head.Children.Add(actions);
        stack.Children.Add(head);
        if (file.Notice.Length > 0 || file.WholeFileOnly)
        {
            stack.Children.Add(new TextBlock
            {
                Text = file.Notice.Length > 0 ? file.Notice : file.Status == "added" ? "Empty new file." : "No line changes to show.",
                FontSize = 12, Foreground = NexusPalette.Muted, TextWrapping = TextWrapping.Wrap, Margin = new Thickness(12, 8, 12, 10),
            });
        }
        var shown = 0;
        foreach (var hunk in file.Hunks)
        {
            if (shown >= MaxLinesPerFile)
            {
                stack.Children.Add(new TextBlock
                {
                    Text = "More changes in this file aren't shown here. Keep or Undo the file applies to all of them.",
                    FontSize = 12, Foreground = NexusPalette.Muted, Margin = new Thickness(12, 6, 12, 8), TextWrapping = TextWrapping.Wrap,
                });
                break;
            }
            stack.Children.Add(HunkView(file, hunk));
            shown += hunk.Lines.Count;
        }
        return new Border
        {
            Child = stack, CornerRadius = new CornerRadius(8), BorderThickness = new Thickness(1),
            BorderBrush = CodePalette.Border, Background = CodePalette.Fill,
        };
    }

    private FrameworkElement HunkView(ReviewFile file, ReviewHunk hunk)
    {
        var lines = new StackPanel();
        foreach (var line in hunk.Lines) lines.Children.Add(LineView(line, file.Language));
        var header = new Grid { Padding = new Thickness(10, 3, 6, 3), Background = CodePalette.HeaderFill, Opacity = 0.95 };
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var label = SessionChatRender.Mono(hunk.Header, 11.5);
        label.Foreground = NexusPalette.Muted;
        label.VerticalAlignment = VerticalAlignment.Center;
        header.Children.Add(label);
        var actions = Actions(file, hunk.Index, file.Version, small: true);
        // Shown on hover or keyboard focus, like the message action row; the
        // space stays reserved so nothing moves.
        actions.Opacity = 0;
        Grid.SetColumn(actions, 1);
        header.Children.Add(actions);
        var block = new StackPanel { Background = Transparent };
        block.Children.Add(header);
        block.Children.Add(new ScrollViewer
        {
            Content = lines, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled,
            HorizontalScrollMode = ScrollMode.Auto, VerticalScrollMode = ScrollMode.Disabled,
        });
        void Reveal(bool show) => MessageActionsAnimations.FadeTo(actions, show ? 1 : 0, show ? 120 : 80);
        block.PointerEntered += (_, _) => Reveal(true);
        block.PointerExited += (_, _) => Reveal(false);
        actions.GotFocus += (_, _) => Reveal(true);
        actions.LostFocus += (_, _) => Reveal(false);
        AutomationProperties.SetName(block, "Change " + (hunk.Index + 1) + " in " + file.FileName + ", " + hunk.Added + " added, " + hunk.Removed + " removed");
        return block;
    }

    private static FrameworkElement LineView(ReviewLine line, string language)
    {
        var text = new TextBlock
        {
            FontFamily = new FontFamily("Cascadia Mono,Consolas,Menlo,monospace"), FontSize = 12, TextWrapping = TextWrapping.NoWrap,
            IsTextSelectionEnabled = true,
        };
        var gutter = (line.Old > 0 ? line.Old.ToString() : "").PadLeft(5) + " " + (line.New > 0 ? line.New.ToString() : "").PadLeft(5) + "  ";
        text.Inlines.Add(new Run { Text = gutter, Foreground = NexusPalette.Muted });
        text.Inlines.Add(new Run
        {
            Text = line.Added ? "+ " : line.Removed ? "− " : "  ",
            Foreground = line.Added ? NexusPalette.DiffAdded : line.Removed ? NexusPalette.DiffRemoved : NexusPalette.Muted,
        });
        // Syntax tint on the code itself; the row's fill says added/removed.
        var code = line.Text.Length > 0 ? line.Text : " ";
        var at = 0;
        foreach (var span in language.Length > 0 && code.Length <= 2000 ? CodeSyntax.Tokenize(code, language) : new List<CodeSpan>())
        {
            if (span.Start < at || span.Start + span.Length > code.Length) continue;
            if (span.Start > at) text.Inlines.Add(new Run { Text = code[at..span.Start], Foreground = CodePalette.Ink });
            var run = new Run { Text = code.Substring(span.Start, span.Length), Foreground = CodePalette.For(span.Kind) ?? CodePalette.Ink };
            if (span.Kind == CodeToken.Comment) run.FontStyle = Windows.UI.Text.FontStyle.Italic;
            text.Inlines.Add(run);
            at = span.Start + span.Length;
        }
        if (at < code.Length) text.Inlines.Add(new Run { Text = code[at..], Foreground = CodePalette.Ink });
        var row = new Border { Child = text, Padding = new Thickness(4, 1, 12, 1) };
        if (line.Added) row.Background = LineFill(added: true);
        else if (line.Removed) row.Background = LineFill(added: false);
        return row;
    }

    private static Brush LineFill(bool added)
    {
        var dark = ThemeService.EffectiveIsDark;
        return new SolidColorBrush(added
            ? (dark ? Windows.UI.Color.FromArgb(46, 46, 160, 67) : Windows.UI.Color.FromArgb(40, 26, 127, 55))
            : (dark ? Windows.UI.Color.FromArgb(46, 248, 81, 73) : Windows.UI.Color.FromArgb(34, 207, 34, 46)));
    }

    private FrameworkElement Actions(ReviewFile file, int hunk, string version, bool small)
    {
        var panel = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 4, VerticalAlignment = VerticalAlignment.Center };
        var what = hunk >= 0 ? "this change" : "this file";
        var undo = ActionButton("Undo", small);
        ToolTipService.SetToolTip(undo, hunk >= 0 ? "Undo this change · put these lines back" : file.Status == "added" ? "Undo · remove this new file" : file.Status == "deleted" ? "Undo · bring this file back" : "Undo · put the whole file back");
        AutomationProperties.SetName(undo, "Undo " + what + " in " + file.FileName);
        undo.IsEnabled = file.CanUndo;
        undo.Click += async (_, _) => await DecideAsync(false, file.Path, hunk, version);
        var keep = ActionButton("Keep", small);
        ToolTipService.SetToolTip(keep, hunk >= 0 ? "Keep this change" : "Keep every change in this file");
        AutomationProperties.SetName(keep, "Keep " + what + " in " + file.FileName);
        keep.Click += async (_, _) => await DecideAsync(true, file.Path, hunk, version);
        panel.Children.Add(undo);
        panel.Children.Add(keep);
        return panel;
    }

    private static Button ActionButton(string text, bool small) => new()
    {
        Content = text, FontSize = small ? 11.5 : 12, MinWidth = 0, MinHeight = 0,
        Padding = small ? new Thickness(8, 2, 8, 3) : new Thickness(10, 3, 10, 4), CornerRadius = new CornerRadius(6),
    };

    private static FrameworkElement StatusBadge(ReviewFile file)
    {
        var color = file.Status switch { "added" => NexusPalette.DiffAdded, "deleted" => NexusPalette.DiffRemoved, _ => NexusPalette.Amber };
        var badge = new Border
        {
            Width = 18, Height = 18, CornerRadius = new CornerRadius(5), BorderThickness = new Thickness(1), BorderBrush = color,
            VerticalAlignment = VerticalAlignment.Center,
            Child = new TextBlock { Text = file.Letter, FontSize = 10.5, FontWeight = FontWeights.Bold, Foreground = color, HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center },
        };
        ToolTipService.SetToolTip(badge, file.StatusLabel);
        return badge;
    }

    private static TextBlock Stats(int added, int removed)
    {
        var t = new TextBlock { FontSize = 12 };
        if (added > 0) t.Inlines.Add(new Run { Text = "+" + added, Foreground = NexusPalette.DiffAdded });
        if (removed > 0) t.Inlines.Add(new Run { Text = (added > 0 ? "  " : "") + "−" + removed, Foreground = NexusPalette.DiffRemoved });
        return t;
    }

    private void OnKeyDown(object sender, KeyRoutedEventArgs e)
    {
        if (e.Key != VirtualKey.Enter || _review is not { Settled: false }) return;
        var primary = Shortcuts.UseCommand ? VirtualKey.LeftWindows : VirtualKey.Control;
        var down = Microsoft.UI.Input.InputKeyboardSource.GetKeyStateForCurrentThread(primary).HasFlag(Windows.UI.Core.CoreVirtualKeyStates.Down);
        if (!down && Shortcuts.UseCommand)
            down = Microsoft.UI.Input.InputKeyboardSource.GetKeyStateForCurrentThread(VirtualKey.RightWindows).HasFlag(Windows.UI.Core.CoreVirtualKeyStates.Down);
        if (!down) return;
        e.Handled = true;
        _ = DecideAsync(true, "", -1, "");
    }

    private async Task DecideAsync(bool keep, string path, int hunk, string version)
    {
        if (_busy) return;
        _busy = true;
        _status.Visibility = Visibility.Collapsed;
        try
        {
            var response = await _call(keep ? "live.review.keep" : "live.review.undo",
                new { checkpoint_id = _checkpointId, path, hunk, version });
            if (response.Result is { ValueKind: JsonValueKind.Object } result) Show(ChangeReview.Parse(result));
            if (!response.Ok)
            {
                _status.Text = response.Error ?? "Nexus couldn't change that file.";
                _status.Visibility = Visibility.Visible;
                // A stale review is refreshed so the next click works.
                if (response.Result is null) await LoadAsync();
            }
            else if (path.Length == 0 || _review is { Settled: true })
            {
                Toast.Show(keep ? "Kept the changes" : "Put the files back as they were before this turn", key: "review-" + _checkpointId);
            }
        }
        finally { _busy = false; }
    }

    private static Brush? s_transparent;
    private static Brush Transparent => s_transparent ??= new SolidColorBrush(Microsoft.UI.Colors.Transparent);
}
