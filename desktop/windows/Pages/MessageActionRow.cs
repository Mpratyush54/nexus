using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Windows.ApplicationModel.DataTransfer;

namespace Nexus.Pages;

/// <summary>
/// The 28 px row under a chat message: Copy, Retry (the latest reply), Edit
/// (your messages), Fork from here, and the time. It appears on hover or
/// keyboard focus (fade 120 ms in, 80 ms out) and its space is always
/// reserved, so nothing moves. The latest reply keeps it shown.
///
/// A long chat has thousands of these, so the buttons are built only when
/// first needed: until then the row is one light tab stop that builds them
/// and hands focus to the first one.
/// </summary>
internal sealed class MessageActionRow
{
    private const double Shown = 0.8;
    private readonly bool _fromUser;
    private readonly Func<string> _copyText;
    private readonly Action? _edit;
    private readonly Func<Task>? _retry;
    private readonly Func<Task>? _fork;
    private StackPanel? _buttons;
    private TextBlock? _time;
    private FontIcon? _copyIcon;
    private Button? _copy;
    private Button? _retryButton;
    private bool _retryVisible;
    private bool _pinned;
    private bool _hovered;
    private bool _focused;
    private DispatcherTimer? _copiedTimer;

    public MessageActionRow(bool fromUser, Func<string> copyText, Action? edit, Func<Task>? retry, Func<Task>? fork)
    {
        _fromUser = fromUser;
        _copyText = copyText;
        _edit = edit;
        _retry = retry;
        _fork = fork;
        View = new ContentControl
        {
            Height = 28, Opacity = 0, IsTabStop = true, UseSystemFocusVisuals = true,
            HorizontalAlignment = fromUser ? HorizontalAlignment.Right : HorizontalAlignment.Left,
            HorizontalContentAlignment = HorizontalAlignment.Stretch,
            Margin = fromUser ? new Thickness(0, 2, 2, 0) : new Thickness(-6, 2, 0, 0),
            MinWidth = 28, Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent),
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(View, fromUser ? "Message actions" : "Reply actions");
        View.GotFocus += (_, e) =>
        {
            _focused = true;
            if (ReferenceEquals(e.OriginalSource, View))
            {
                // Keyboard reached the placeholder: build the row and move
                // focus into it (the placeholder stops being a tab stop).
                var state = View.FocusState == FocusState.Unfocused ? FocusState.Programmatic : View.FocusState;
                Realize();
                View.IsTabStop = false;
                // The new buttons can take focus once they are in the tree.
                View.DispatcherQueue?.TryEnqueue(() => FirstButton()?.Focus(state));
            }
            Refresh();
        };
        View.LostFocus += (_, _) =>
        {
            // Focus moving between the row's own buttons keeps it shown.
            View.DispatcherQueue?.TryEnqueue(() =>
            {
                var focused = View.XamlRoot is { } root ? FocusManager.GetFocusedElement(root) as DependencyObject : null;
                _focused = focused is not null && IsInside(focused, View);
                Refresh();
            });
        };
    }

    public ContentControl View { get; }

    /// Supplies the time on first reveal ("2:41 PM"); empty means none.
    public Func<string>? Time { get; set; }

    /// Retry is offered on the latest reply only.
    public bool RetryVisible
    {
        get => _retryVisible;
        set
        {
            _retryVisible = value;
            if (_retryButton is not null) _retryButton.Visibility = value ? Visibility.Visible : Visibility.Collapsed;
        }
    }

    /// Shown without hover (the latest reply).
    public bool Pinned
    {
        get => _pinned;
        set
        {
            if (_pinned == value) return;
            _pinned = value;
            if (value) Realize();
            Refresh();
        }
    }

    /// Hover over `target` (the message and this row) reveals the row.
    public void HoverOn(UIElement target)
    {
        target.PointerEntered += (_, _) => { _hovered = true; Realize(); Refresh(); };
        target.PointerExited += (_, _) => { _hovered = false; Refresh(); };
        target.PointerCanceled += (_, _) => { _hovered = false; Refresh(); };
    }

    private void Refresh()
    {
        var show = _pinned || _hovered || _focused;
        if (_time is not null)
        {
            if ((_hovered || _focused) && Time is { } time && _time.Text.Length == 0) _time.Text = time();
            _time.Visibility = _time.Text.Length > 0 && (_hovered || _focused) ? Visibility.Visible : Visibility.Collapsed;
        }
        var target = show ? Shown : 0;
        if (Math.Abs(View.Opacity - target) < 0.01) return;
        MessageActionsAnimations.FadeTo(View, target, show ? 120 : 80);
    }

    private Button? FirstButton() => _buttons?.Children.OfType<Button>().FirstOrDefault(b => b.Visibility == Visibility.Visible);

    private void Realize()
    {
        if (_buttons is not null) return;
        _buttons = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 2, VerticalAlignment = VerticalAlignment.Center };
        _time = new TextBlock
        {
            FontSize = 11.5, Foreground = NexusPalette.Muted, VerticalAlignment = VerticalAlignment.Center,
            Margin = new Thickness(6, 0, 6, 0), Visibility = Visibility.Collapsed
        };
        _copyIcon = Icon("");
        _copy = Action(_copyIcon, _fromUser ? "Copy message" : "Copy reply", "Copy");
        _copy.Click += (_, _) => Copy();
        var fork = Action(new TextBlock { Text = "⑂", FontSize = 17, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, Margin = new Thickness(0, -3, 0, 0) },
            "Fork from here", "Fork from here · a new chat with the conversation up to this message");
        fork.Click += async (_, _) => { if (_fork is not null) await _fork(); };
        if (_fromUser)
        {
            var edit = Action(Icon(""), "Edit message", "Edit · put this message back in the composer");
            edit.Click += (_, _) => _edit?.Invoke();
            _buttons.Children.Add(_time);
            _buttons.Children.Add(edit);
            _buttons.Children.Add(fork);
            _buttons.Children.Add(_copy);
        }
        else
        {
            _retryButton = Action(Icon(""), "Retry", "Retry · ask again with the same message");
            _retryButton.Visibility = _retryVisible ? Visibility.Visible : Visibility.Collapsed;
            _retryButton.Click += async (_, _) => { if (_retry is not null) await _retry(); };
            _buttons.Children.Add(_copy);
            _buttons.Children.Add(_retryButton);
            _buttons.Children.Add(fork);
            _buttons.Children.Add(_time);
        }
        View.Content = _buttons;
        View.IsTabStop = false;
    }

    private void Copy()
    {
        try
        {
            var data = new DataPackage();
            data.SetText(_copyText());
            Clipboard.SetContent(data);
        }
        catch (Exception)
        {
            return; // the clipboard can be held by another app for a moment
        }
        if (_copyIcon is null || _copy is null) return;
        MessageActionsAnimations.CrossFade(_copyIcon, () => { _copyIcon.Glyph = ""; _copyIcon.Foreground = NexusPalette.DiffAdded; });
        ToolTipService.SetToolTip(_copy, "Copied");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_copy, "Copied");
        _copiedTimer ??= new DispatcherTimer { Interval = TimeSpan.FromMilliseconds(1500) };
        _copiedTimer.Stop();
        _copiedTimer.Tick -= CopiedTick;
        _copiedTimer.Tick += CopiedTick;
        _copiedTimer.Start();
    }

    private void CopiedTick(object? sender, object e)
    {
        _copiedTimer?.Stop();
        if (_copyIcon is null || _copy is null) return;
        MessageActionsAnimations.CrossFade(_copyIcon, () => { _copyIcon.Glyph = ""; _copyIcon.ClearValue(IconElement.ForegroundProperty); });
        ToolTipService.SetToolTip(_copy, "Copy");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_copy, _fromUser ? "Copy message" : "Copy reply");
    }

    private static FontIcon Icon(string glyph) => new() { Glyph = glyph, FontSize = 13, FontFamily = WorkLogVisuals.IconFont };

    private static Button Action(UIElement icon, string name, string tip)
    {
        var button = new Button
        {
            Content = icon, Width = 28, Height = 28, MinWidth = 0, MinHeight = 0, Padding = new Thickness(0),
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0),
            CornerRadius = new CornerRadius(6), Foreground = NexusPalette.Muted,
        };
        ToolTipService.SetToolTip(button, tip);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(button, name);
        return button;
    }

    private static bool IsInside(DependencyObject node, DependencyObject container)
    {
        for (var at = node; at is not null; at = VisualTreeHelper.GetParent(at))
            if (ReferenceEquals(at, container)) return true;
        return false;
    }
}
