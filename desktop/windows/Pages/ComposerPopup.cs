using Microsoft.UI.Text;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Controls.Primitives;
using Microsoft.UI.Xaml.Documents;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Media.Animation;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// The small list that opens above the composer for "/" (commands) and "@"
/// (files, folders, chats, memories). It never takes focus: the message box
/// keeps the caret and forwards ↑/↓, Enter/Tab and Esc here, so typing is
/// never blocked. Rows highlight the letters that matched; an empty list
/// says so ("No files match"). Opens with a 140 ms fade and lift, closes
/// with a 90 ms fade (instant with reduced motion).
/// </summary>
internal sealed class ComposerPopup
{
    /// <summary>One row. <paramref name="Group"/> rows are listed under a small heading.</summary>
    internal sealed record Row(string Id, string Glyph, string Title, int[] TitleHits, string Detail, int[] DetailHits, string Group, object? Payload = null, string Badge = "");

    private readonly FrameworkElement _anchor;
    private readonly Popup _popup = new() { IsLightDismissEnabled = false };
    private readonly Border _card;
    private readonly StackPanel _list = new() { Spacing = 1 };
    private readonly ScrollViewer _scroll;
    private readonly TextBlock _title = new() { FontSize = 11, FontWeight = FontWeights.SemiBold, Opacity = 0.6, CharacterSpacing = 40 };
    private readonly StackPanel _empty = new() { Orientation = Orientation.Horizontal, Spacing = 8, Margin = new Thickness(10, 10, 10, 12), Visibility = Visibility.Collapsed };
    private readonly TextBlock _emptyText = new() { FontSize = 12.5, Opacity = 0.7, TextWrapping = TextWrapping.Wrap, MaxWidth = 380 };
    private readonly ProgressRing _busy = new() { Width = 14, Height = 14, MinWidth = 14, MinHeight = 14, IsActive = false, Visibility = Visibility.Collapsed };
    private readonly TranslateTransform _lift = new();
    private readonly List<(Row Row, Grid Element)> _rows = new();
    private Storyboard? _motion;
    private readonly TextBlock _note = new() { FontSize = 12, Opacity = 0.6, Margin = new Thickness(10, 0, 10, 4), TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };
    private int _selected = -1;
    // Results that arrive later (chats, memories) keep a row the person moved to, never one picked for them.
    private bool _userSelected;
    private bool _closing;

    /// <summary>A row was picked with the mouse (keys go through <see cref="Pick"/>).</summary>
    public event Action<Row>? Picked;

    public ComposerPopup(FrameworkElement anchor)
    {
        _anchor = anchor;
        _scroll = new ScrollViewer
        {
            Content = _list, MaxHeight = 296, VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
            HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled, Padding = new Thickness(0, 0, 4, 0)
        };
        var hint = new TextBlock
        {
            Text = "↑↓ move · Enter or Tab pick · Esc close", FontSize = 11, Opacity = 0.5, Margin = new Thickness(10, 4, 10, 0)
        };
        _empty.Children.Add(_busy);
        _empty.Children.Add(_emptyText);
        var stack = new StackPanel { Spacing = 2 };
        stack.Children.Add(new Border { Child = _title, Padding = new Thickness(10, 2, 10, 4) });
        stack.Children.Add(_note);
        stack.Children.Add(_scroll);
        stack.Children.Add(_empty);
        stack.Children.Add(new Border { Height = 1, Background = NexusPalette.Hairline, Margin = new Thickness(0, 4, 0, 0) });
        stack.Children.Add(hint);
        _card = new Border
        {
            Child = stack, Padding = new Thickness(4, 8, 4, 8), CornerRadius = new CornerRadius(12), BorderThickness = new Thickness(1),
            RenderTransform = _lift
        };
        AutomationProperties.SetName(_card, "Suggestions");
        _popup.Child = _card;
        _card.SizeChanged += (_, _) => Place();
        _anchor.SizeChanged += (_, _) => { if (IsOpen) Place(); };
    }

    public bool IsOpen => _popup.IsOpen && !_closing;
    public bool HasRows => _rows.Count > 0;
    /// <summary>Results are still on their way (Enter waits rather than sending a half-typed mention).</summary>
    public bool IsLoading { get; private set; }
    public Row? Selected => _selected >= 0 && _selected < _rows.Count ? _rows[_selected].Row : null;
    public int Count => _rows.Count;

    /// <summary>The query changed: the first row is selected again.</summary>
    public void NewQuery() => _userSelected = false;

    /// <summary>
    /// Shows (or refreshes) the list. Keeps a row the person selected when it
    /// is still there. <paramref name="note"/> is a quiet line above the rows
    /// ("No files match “x”" while chats still do).
    /// </summary>
    public void Show(string title, IReadOnlyList<Row> rows, string emptyText, bool loading, double width, string note = "")
    {
        var keep = _userSelected ? Selected?.Id : null;
        _note.Text = note;
        _note.Visibility = note.Length > 0 && rows.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
        _title.Text = title.ToUpperInvariant();
        _list.Children.Clear();
        _rows.Clear();
        var group = "";
        foreach (var row in rows)
        {
            // A heading above each group, except a first group that repeats the title ("Commands").
            var first = _list.Children.Count == 0;
            if (row.Group.Length > 0 && row.Group != group && !(first && string.Equals(row.Group, title, StringComparison.OrdinalIgnoreCase)))
            {
                _list.Children.Add(new TextBlock
                {
                    Text = row.Group, FontSize = 11, Opacity = 0.5, FontWeight = FontWeights.SemiBold,
                    Margin = new Thickness(10, first ? 0 : 8, 10, 2)
                });
            }
            group = row.Group;
            var element = RowElement(row, _rows.Count);
            _rows.Add((row, element));
            _list.Children.Add(element);
        }
        _scroll.Visibility = _rows.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
        _empty.Visibility = _rows.Count == 0 ? Visibility.Visible : Visibility.Collapsed;
        _emptyText.Text = loading && _rows.Count == 0 ? "Looking…" : emptyText;
        IsLoading = loading;
        _busy.IsActive = loading && _rows.Count == 0;
        _busy.Visibility = _busy.IsActive ? Visibility.Visible : Visibility.Collapsed;
        _card.Width = width;
        _card.Background = ThemeService.Brush("FlyoutPresenterBackground", Windows.UI.Color.FromArgb(255, 0x16, 0x17, 0x16));
        _card.BorderBrush = NexusPalette.Hairline;
        _card.RequestedTheme = ThemeService.EffectiveIsDark ? ElementTheme.Dark : ElementTheme.Light;
        var again = keep is null ? -1 : _rows.FindIndex(r => r.Row.Id == keep);
        Select(again >= 0 ? again : 0);
        Open();
    }

    private void Open()
    {
        if (_popup.IsOpen && !_closing) { Place(); return; }
        _closing = false;
        _motion?.Stop();
        _popup.XamlRoot = _anchor.XamlRoot;
        if (_popup.XamlRoot is null) return;
        _popup.IsOpen = true;
        Place();
        if (!Motion.Enabled) { _card.Opacity = 1; _lift.Y = 0; return; }
        Animate(0, 1, 6, 0, 140, null);
    }

    /// <summary>Closes with a short fade.</summary>
    public void Close()
    {
        if (!_popup.IsOpen || _closing) return;
        _selected = -1;
        _userSelected = false;
        if (!Motion.Enabled) { _popup.IsOpen = false; return; }
        _closing = true;
        Animate(1, 0, 0, 4, 90, () =>
        {
            if (!_closing) return;
            _closing = false;
            _popup.IsOpen = false;
        });
    }

    private void Animate(double fromOpacity, double toOpacity, double fromY, double toY, int ms, Action? done)
    {
        _motion?.Stop();
        var board = new Storyboard();
        var ease = new CubicEase { EasingMode = EasingMode.EaseOut };
        var fade = new DoubleAnimation { From = fromOpacity, To = toOpacity, Duration = TimeSpan.FromMilliseconds(ms), EasingFunction = ease };
        Storyboard.SetTarget(fade, _card);
        Storyboard.SetTargetProperty(fade, "Opacity");
        var lift = new DoubleAnimation { From = fromY, To = toY, Duration = TimeSpan.FromMilliseconds(ms), EasingFunction = ease };
        Storyboard.SetTarget(lift, _lift);
        Storyboard.SetTargetProperty(lift, "Y");
        board.Children.Add(fade);
        board.Children.Add(lift);
        board.Completed += (_, _) => { if (ReferenceEquals(_motion, board)) { _motion = null; done?.Invoke(); } };
        _motion = board;
        board.Begin();
    }

    // Above the anchor (the composer), left-aligned with it.
    private void Place()
    {
        if (!_popup.IsOpen || _anchor.XamlRoot is null) return;
        try
        {
            var at = _anchor.TransformToVisual(null).TransformPoint(new Windows.Foundation.Point(0, 0));
            var height = _card.ActualHeight > 0 ? _card.ActualHeight : _card.DesiredSize.Height;
            _popup.HorizontalOffset = at.X;
            // Above the composer; below it when there is no room above (Home's composer sits near the top).
            var above = at.Y - height - 8;
            _popup.VerticalOffset = above >= 8 ? above : at.Y + _anchor.ActualHeight + 8;
        }
        catch (ArgumentException) { }
    }

    public void Move(int delta)
    {
        if (_rows.Count == 0) return;
        var next = (_selected + delta) % _rows.Count;
        if (next < 0) next += _rows.Count;
        _userSelected = true;
        Select(next);
    }

    /// <summary>The selected row, for Enter/Tab.</summary>
    public Row? Pick() => Selected;

    private void Select(int index)
    {
        if (_rows.Count == 0) { _selected = -1; return; }
        index = Math.Clamp(index, 0, _rows.Count - 1);
        _selected = index;
        for (var i = 0; i < _rows.Count; i++)
            _rows[i].Element.Background = i == index ? SelectedFill : new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        var element = _rows[index].Element;
        AutomationProperties.SetName(_card, "Suggestions: " + _rows[index].Row.Title + " selected");
        // Keep the selection in view without stealing focus.
        element.DispatcherQueue?.TryEnqueue(() =>
        {
            try
            {
                if (element.ActualHeight <= 0) return;
                var top = element.TransformToVisual(_list).TransformPoint(new Windows.Foundation.Point(0, 0)).Y;
                if (index == 0) top = 0;
                if (top < _scroll.VerticalOffset) _scroll.ChangeView(null, top, null, true);
                else if (top + element.ActualHeight > _scroll.VerticalOffset + _scroll.ViewportHeight)
                    _scroll.ChangeView(null, top + element.ActualHeight - _scroll.ViewportHeight, null, true);
            }
            catch (ArgumentException) { }
        });
    }

    private static Brush SelectedFill => new SolidColorBrush(ThemeService.EffectiveIsDark
        ? Windows.UI.Color.FromArgb(0x24, 0xFF, 0xFF, 0xFF)
        : Windows.UI.Color.FromArgb(0x14, 0x00, 0x00, 0x00));

    private Grid RowElement(Row row, int index)
    {
        var grid = new Grid
        {
            Padding = new Thickness(10, 6, 10, 6), CornerRadius = new CornerRadius(8), ColumnSpacing = 10, MinHeight = 34,
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent)
        };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var icon = new FontIcon { Glyph = row.Glyph, FontSize = 14, Opacity = 0.75, VerticalAlignment = VerticalAlignment.Center };
        grid.Children.Add(icon);
        var words = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8, VerticalAlignment = VerticalAlignment.Center };
        var title = Highlighted(row.Title, row.TitleHits, 13, 0.95);
        title.TextTrimming = TextTrimming.CharacterEllipsis;
        words.Children.Add(title);
        if (row.Detail.Length > 0)
        {
            var detail = Highlighted(row.Detail, row.DetailHits, 12, 0.55);
            detail.TextTrimming = TextTrimming.CharacterEllipsis;
            detail.MaxWidth = 300;
            words.Children.Add(detail);
        }
        Grid.SetColumn(words, 1);
        grid.Children.Add(words);
        if (row.Badge.Length > 0)
        {
            var badge = new TextBlock { Text = row.Badge, FontSize = 11, Opacity = 0.5, VerticalAlignment = VerticalAlignment.Center };
            Grid.SetColumn(badge, 2);
            grid.Children.Add(badge);
        }
        AutomationProperties.SetName(grid, row.Title + (row.Detail.Length > 0 ? ", " + row.Detail : ""));
        grid.PointerEntered += (_, _) => { _userSelected = true; Select(index); };
        grid.Tapped += (_, e) =>
        {
            e.Handled = true;
            Select(index);
            if (Selected is { } picked) Picked?.Invoke(picked);
        };
        return grid;
    }

    /// <summary>A line with the matched letters in the accent colour, semi-bold.</summary>
    private static TextBlock Highlighted(string text, int[] hits, double size, double opacity)
    {
        var block = new TextBlock { FontSize = size, TextWrapping = TextWrapping.NoWrap, VerticalAlignment = VerticalAlignment.Center };
        var normal = (ThemeService.EffectiveIsDark ? 255 : 20);
        foreach (var (part, hit) in FuzzyMatch.Segments(text, hits))
        {
            var run = new Run { Text = part };
            if (hit)
            {
                run.Foreground = NexusPalette.Accent;
                run.FontWeight = FontWeights.SemiBold;
            }
            else
            {
                run.Foreground = new SolidColorBrush(Windows.UI.Color.FromArgb((byte)(opacity * 255), (byte)normal, (byte)normal, (byte)normal));
            }
            block.Inlines.Add(run);
        }
        return block;
    }
}
