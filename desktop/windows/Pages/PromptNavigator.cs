using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Controls.Primitives;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

// A narrow conversation outline. Hit testing uses the whole rail, so even
// hundreds of tiny markers can be selected without overlapping buttons.
internal sealed class PromptNavigator : UserControl
{
    // Short chats need no outline: two lone markers beside the messages read
    // as rendering glitches rather than navigation.
    internal const int MinPrompts = 3;
    private readonly Canvas _canvas = new() { Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent) };
    private readonly Border _track = new() { Width = 2, CornerRadius = new CornerRadius(1), IsHitTestVisible = false, Opacity = 0.07 };
    private readonly ToolTip _preview;
    private readonly TextBlock _previewTitle = new() { FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, FontSize = 12 };
    private readonly TextBlock _previewText = new() { TextWrapping = TextWrapping.Wrap, MaxLines = 7, TextTrimming = TextTrimming.CharacterEllipsis, FontSize = 13, LineHeight = 20 };
    private IReadOnlyList<ConversationEntry> _prompts = [];
    private readonly List<Border> _markers = new();
    private string _activeKey = "";
    private int _hover = -1;
    internal event Action<ConversationEntry>? JumpRequested;

    internal PromptNavigator()
    {
        Width = 28;
        IsTabStop = true;
        VerticalAlignment = VerticalAlignment.Stretch;
        Content = _canvas;
        AutomationProperties.SetName(this, "Conversation prompts. Use arrow keys to preview your inputs and Enter to jump.");
        var content = new StackPanel { Spacing = 8, Width = 310, Children = { _previewTitle, _previewText, new TextBlock { Text = "Click to jump to this message", Opacity = 0.6, FontSize = 11 } } };
        _preview = new ToolTip { Content = content, Placement = PlacementMode.Left, PlacementTarget = this };
        ToolTipService.SetToolTip(this, _preview);
        _canvas.SizeChanged += (_, _) => Render();
        _canvas.PointerMoved += (_, e) =>
        {
            if (_prompts.Count == 0 || _canvas.ActualHeight <= 0) return;
            Select(Math.Clamp((int)(e.GetCurrentPoint(_canvas).Position.Y / _canvas.ActualHeight * _prompts.Count), 0, _prompts.Count - 1));
        };
        _canvas.PointerExited += (_, _) => { _hover = -1; _preview.IsOpen = false; StyleMarkers(); };
        _canvas.PointerPressed += (_, e) =>
        {
            if (_hover < 0 || !e.GetCurrentPoint(_canvas).Properties.IsLeftButtonPressed) return;
            _preview.IsOpen = false;
            JumpRequested?.Invoke(_prompts[_hover]);
            e.Handled = true;
        };
        KeyDown += OnKeyDown;
        LostFocus += (_, _) => _preview.IsOpen = false;
        Unloaded += (_, _) => _preview.IsOpen = false;
    }

    internal void SetPrompts(IReadOnlyList<ConversationEntry> prompts)
    {
        _prompts = prompts;
        _hover = -1;
        _preview.IsOpen = false;
        Visibility = prompts.Count >= MinPrompts ? Visibility.Visible : Visibility.Collapsed;
        Render();
    }

    internal void SetActive(string key)
    {
        if (_activeKey == key) return;
        _activeKey = key;
        StyleMarkers();
    }

    private void Select(int index)
    {
        if (_hover == index && _preview.IsOpen) return;
        _hover = index;
        var item = _prompts[index];
        var time = DateTimeOffset.TryParse(item.Timestamp, out var at) ? " · " + at.ToLocalTime().ToString("MMM d, h:mm tt") : "";
        _previewTitle.Text = "Your input " + (index + 1) + " of " + _prompts.Count + time;
        _previewText.Text = ConversationIndex.Preview(item.Text, length: 600);
        _preview.IsOpen = true;
        StyleMarkers();
    }

    private void Render()
    {
        _canvas.Children.Clear(); _markers.Clear();
        var ink = Application.Current.Resources.TryGetValue("TextFillColorPrimaryBrush", out var resource) && resource is Brush brush ? brush : new SolidColorBrush(Microsoft.UI.Colors.White);
        // A faint rail ties the markers together so they read as an outline.
        _track.Background = ink;
        _track.Height = Math.Max(0, _canvas.ActualHeight - 4);
        Canvas.SetLeft(_track, 23);
        Canvas.SetTop(_track, 2);
        _canvas.Children.Add(_track);
        for (var i = 0; i < _prompts.Count; i++)
        {
            var marker = new Border { Height = 2, Width = 8, CornerRadius = new CornerRadius(1), Background = ink, IsHitTestVisible = false };
            Canvas.SetTop(marker, (_canvas.ActualHeight - 4) * (i + 0.5) / _prompts.Count);
            Canvas.SetLeft(marker, 10);
            _canvas.Children.Add(marker); _markers.Add(marker);
        }
        StyleMarkers();
    }

    private void StyleMarkers()
    {
        for (var i = 0; i < _markers.Count; i++)
        {
            var selected = _prompts[i].Key == _activeKey;
            _markers[i].Opacity = i == _hover ? 1 : selected ? 0.8 : 0.3;
            _markers[i].Width = i == _hover ? 20 : selected ? 14 : 8;
            Canvas.SetLeft(_markers[i], 24 - _markers[i].Width);
        }
    }

    private void OnKeyDown(object sender, KeyRoutedEventArgs e)
    {
        if (_prompts.Count == 0) return;
        switch (e.Key)
        {
            case Windows.System.VirtualKey.Down: Select(Math.Min(_prompts.Count - 1, _hover + 1)); break;
            case Windows.System.VirtualKey.Up: Select(Math.Max(0, _hover - 1)); break;
            case Windows.System.VirtualKey.Home: Select(0); break;
            case Windows.System.VirtualKey.End: Select(_prompts.Count - 1); break;
            case Windows.System.VirtualKey.Enter:
            case Windows.System.VirtualKey.Space:
                if (_hover >= 0) { _preview.IsOpen = false; JumpRequested?.Invoke(_prompts[_hover]); }
                break;
            case Windows.System.VirtualKey.Escape: _preview.IsOpen = false; break;
            default: return;
        }
        e.Handled = true;
    }
}
