using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Documents;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Media.Animation;
using Nexus.Core;
using Windows.ApplicationModel.DataTransfer;

namespace Nexus.Pages;

/// <summary>
/// A fenced code block in a chat: a card with the language at top left, a
/// Copy button at top right (a check for 1.5 s once copied), lines that
/// scroll sideways instead of wrapping, and light syntax colour. Long blocks
/// in saved chats open folded at 480 px with "Show all N lines". While a
/// reply streams the same card grows in place (<see cref="SetCode"/>).
/// </summary>
internal sealed class CodeBlockView
{
    private const double FoldHeight = 480;
    private const int FoldLines = 26;
    // While a fence is still arriving, colour is redone on each update only
    // up to this size; bigger blocks get colour once they close.
    private const int LiveHighlightChars = 24_000;

    private readonly Border _card;
    private readonly TextBlock _code;
    private readonly TextBlock _language;
    // The code sits in a plain Border; a ScrollViewer replaces it only when a
    // line is wider than the card (most blocks never need one).
    private FrameworkElement _host;
    private ScrollViewer? _scroll;
    private int _longestLine;
    private const string CopyGlyph = "", CheckGlyph = "";
    private readonly TextBlock _copy;
    private readonly Hyperlink _copyLink;
    private readonly Run _copyIcon;
    private readonly Run _copyText;
    private readonly StackPanel _body;
    private HyperlinkButton? _fold;
    private string _text = "";
    private string _lang = "";
    private bool _highlighted;
    private bool _folded;
    private DispatcherTimer? _copiedTimer;

    public CodeBlockView(string code, string language, bool closed = true, bool foldLong = true)
    {
        _language = new TextBlock
        {
            FontSize = 11.5, Foreground = NexusPalette.Muted, VerticalAlignment = VerticalAlignment.Center,
            FontFamily = WorkLogVisuals.MonoFont, TextTrimming = TextTrimming.CharacterEllipsis
        };
        // A hyperlink, not a Button: a long chat has hundreds of code blocks
        // and this stays keyboard-reachable (Tab, Enter) at a fraction of the cost.
        _copyIcon = new Run { Text = CopyGlyph, FontFamily = WorkLogVisuals.IconFont, FontSize = 12 };
        _copyText = new Run { Text = "  Copy" };
        var link = new Hyperlink { UnderlineStyle = UnderlineStyle.None, Foreground = NexusPalette.Muted };
        link.Inlines.Add(_copyIcon);
        link.Inlines.Add(_copyText);
        link.Click += (_, _) => Copy();
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(link, "Copy code");
        _copyLink = link;
        _copy = new TextBlock { FontSize = 11.5, VerticalAlignment = VerticalAlignment.Center, Margin = new Thickness(0, 0, 8, 0) };
        _copy.Inlines.Add(link);
        ToolTipService.SetToolTip(_copy, "Copy code");

        var header = new Grid { Padding = new Thickness(12, 4, 4, 4), MinHeight = 32, Background = CodePalette.HeaderFill };
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        header.Children.Add(_language);
        Grid.SetColumn(_copy, 1);
        header.Children.Add(_copy);

        _code = new TextBlock
        {
            FontFamily = WorkLogVisuals.MonoFont, FontSize = 12.5, LineHeight = 19,
            TextWrapping = TextWrapping.NoWrap, IsTextSelectionEnabled = true,
            Foreground = CodePalette.Ink, Padding = new Thickness(14, 10, 14, 12)
        };
        _host = new Border { Child = _code };
        _body = new StackPanel();
        _body.Children.Add(header);
        _body.Children.Add(_host);
        _card = new Border
        {
            Child = _body, CornerRadius = new CornerRadius(8), BorderThickness = new Thickness(1),
            BorderBrush = CodePalette.Border, Background = CodePalette.Fill,
            HorizontalAlignment = HorizontalAlignment.Stretch,
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_card, "Code block");
        _card.SizeChanged += (_, e) => { if (e.NewSize.Width != e.PreviousSize.Width) ScrollIfWide(); };
        SetCode(code, language, closed);
        if (foldLong && LineCount(code) > FoldLines) SetFolded(true);
    }

    public FrameworkElement View => _card;

    public string Code => _text;

    public string Language => _lang;

    /// Updates the card in place (a streaming fence growing).
    public void SetCode(string code, string language, bool closed)
    {
        code ??= "";
        language ??= "";
        var changed = !string.Equals(code, _text, StringComparison.Ordinal) || !string.Equals(language, _lang, StringComparison.Ordinal);
        var wantColour = CodeSyntax.Supports(language) && (closed || code.Length <= LiveHighlightChars);
        if (!changed && _highlighted == wantColour) return;
        _text = code;
        _lang = language;
        _language.Text = language.Length > 0 ? language : "text";
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_card, (language.Length > 0 ? language + " code" : "Code") + ", " + LineCount(code) + " lines");
        _longestLine = LongestLine(code);
        Render(wantColour);
        ScrollIfWide();
        if (_fold is not null) UpdateFoldLabel();
    }

    private void Render(bool colour)
    {
        _highlighted = colour;
        _code.Inlines.Clear();
        var text = _text.Length > 0 ? _text : " ";
        if (!colour)
        {
            _code.Text = text;
            return;
        }
        var at = 0;
        foreach (var span in CodeSyntax.Tokenize(text, _lang))
        {
            if (span.Start > at) _code.Inlines.Add(new Run { Text = text[at..span.Start] });
            var run = new Run { Text = text.Substring(span.Start, span.Length) };
            if (CodePalette.For(span.Kind) is { } brush) run.Foreground = brush;
            if (span.Kind == CodeToken.Comment) run.FontStyle = Windows.UI.Text.FontStyle.Italic;
            _code.Inlines.Add(run);
            at = span.Start + span.Length;
        }
        if (at < text.Length) _code.Inlines.Add(new Run { Text = text[at..] });
    }

    private static int LongestLine(string text)
    {
        int longest = 0, start = 0;
        for (var i = 0; i <= text.Length; i++)
        {
            if (i < text.Length && text[i] != '\n') continue;
            var length = 0;
            for (var k = start; k < i; k++) length += text[k] == '\t' ? 4 : 1;
            longest = Math.Max(longest, length);
            start = i + 1;
        }
        return longest;
    }

    // Sideways scrolling once the longest line (mono, about 0.62 em per
    // character) is wider than the card.
    private void ScrollIfWide()
    {
        if (_scroll is not null || _card.ActualWidth <= 0) return;
        var needed = _longestLine * _code.FontSize * 0.62 + _code.Padding.Left + _code.Padding.Right;
        if (needed <= _card.ActualWidth - 2) return;
        var at = _body.Children.IndexOf(_host);
        if (_host is Border border) border.Child = null;
        _scroll = new ScrollViewer
        {
            Content = _code, MaxHeight = _host.MaxHeight,
            HorizontalScrollMode = ScrollMode.Auto, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto,
            VerticalScrollMode = ScrollMode.Disabled, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled,
            // The chat keeps the mouse wheel; Shift+wheel and the bar scroll sideways.
            IsVerticalScrollChainingEnabled = true,
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_scroll, "Code");
        _host = _scroll;
        if (at >= 0) _body.Children[at] = _scroll;
    }

    private void SetFolded(bool folded)
    {
        _folded = folded;
        _host.MaxHeight = folded ? FoldHeight : double.PositiveInfinity;
        if (_fold is null)
        {
            _fold = new HyperlinkButton { FontSize = 12, Padding = new Thickness(12, 4, 12, 6) };
            _fold.Click += (_, _) => SetFolded(!_folded);
            _body.Children.Add(_fold);
        }
        UpdateFoldLabel();
    }

    private void UpdateFoldLabel()
    {
        if (_fold is null) return;
        var lines = LineCount(_text);
        _fold.Content = _folded ? $"Show all {lines} lines" : "Show less";
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_fold, _fold.Content as string ?? "");
    }

    private static int LineCount(string text) => string.IsNullOrEmpty(text) ? 0 : text.Count(c => c == '\n') + 1;

    private void Copy()
    {
        try
        {
            var data = new DataPackage();
            data.SetText(_text);
            Clipboard.SetContent(data);
        }
        catch (Exception)
        {
            return; // clipboard busy: the button simply does not confirm
        }
        ShowCopied(true);
        _copiedTimer ??= new DispatcherTimer { Interval = TimeSpan.FromMilliseconds(1500) };
        _copiedTimer.Stop();
        _copiedTimer.Tick -= CopiedTick;
        _copiedTimer.Tick += CopiedTick;
        _copiedTimer.Start();
    }

    private void CopiedTick(object? sender, object e)
    {
        _copiedTimer?.Stop();
        ShowCopied(false);
    }

    private void ShowCopied(bool copied)
    {
        MessageActionsAnimations.CrossFade(_copy, () =>
        {
            _copyIcon.Text = copied ? CheckGlyph : CopyGlyph;
            _copyText.Text = copied ? "  Copied" : "  Copy";
            _copyLink.Foreground = copied ? NexusPalette.DiffAdded : NexusPalette.Muted;
        });
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_copyLink, copied ? "Copied" : "Copy code");
    }
}

/// Code colours, tuned for each theme (GitHub-like, quiet on both).
internal static class CodePalette
{
    private static bool Dark => ThemeService.EffectiveIsDark;

    // One brush per colour, shared by every code block (a long chat has
    // thousands of coloured runs). Brushes are UI-thread objects.
    private static readonly Dictionary<ulong, SolidColorBrush> s_brushes = new();

    private static SolidColorBrush Rgb(uint dark, uint light, byte alpha = 255)
    {
        var v = Dark ? dark : light;
        var key = ((ulong)alpha << 24) | v;
        if (!s_brushes.TryGetValue(key, out var brush))
            s_brushes[key] = brush = new SolidColorBrush(Windows.UI.Color.FromArgb(alpha, (byte)(v >> 16), (byte)(v >> 8), (byte)v));
        return brush;
    }

    public static Brush Fill => Dark ? Rgb(0xFFFFFF, 0, 10) : Rgb(0, 0xF6F8FA);
    public static Brush HeaderFill => Dark ? Rgb(0xFFFFFF, 0, 8) : Rgb(0, 0xEEF1F4);
    public static Brush Border => Dark ? Rgb(0xFFFFFF, 0, 22) : Rgb(0, 0xD8DEE4);
    public static Brush Ink => Rgb(0xE6EDF3, 0x1F2328);
    /// Inline `code` in prose.
    public static Brush InlineInk => Rgb(0xF0B48A, 0xA8431B);

    public static Brush? For(CodeToken kind) => kind switch
    {
        CodeToken.Keyword => Rgb(0xFF7B72, 0xCF222E),
        CodeToken.String => Rgb(0xA5D6FF, 0x0A3069),
        CodeToken.Comment => Rgb(0x8B949E, 0x6E7781),
        CodeToken.Number => Rgb(0x79C0FF, 0x0550AE),
        CodeToken.Type => Rgb(0xFFA657, 0x953800),
        CodeToken.Property => Rgb(0x7EE787, 0x116329),
        CodeToken.Meta => Rgb(0xD2A8FF, 0x8250DF),
        CodeToken.Added => Rgb(0x56D364, 0x1A7F37),
        CodeToken.Removed => Rgb(0xF85149, 0xCF222E),
        _ => null,
    };
}

/// Small shared fades for the message action row and code copy buttons.
internal static class MessageActionsAnimations
{
    /// Fades the element out, applies the change, fades back (120 ms in total).
    public static void CrossFade(UIElement? element, Action change)
    {
        if (element is null || !Motion.Enabled) { change(); return; }
        var outSb = Fade(element, 1, 0, 60);
        outSb.Completed += (_, _) =>
        {
            change();
            Fade(element, 0, 1, 60).Begin();
        };
        outSb.Begin();
    }

    /// Opacity to `to` over `ms` (instant when animations are off).
    public static void FadeTo(UIElement element, double to, int ms)
    {
        if (!Motion.Enabled) { element.Opacity = to; return; }
        Fade(element, element.Opacity, to, ms).Begin();
    }

    private static Storyboard Fade(UIElement element, double from, double to, int ms)
    {
        var animation = new DoubleAnimation
        {
            From = from, To = to, Duration = new Duration(TimeSpan.FromMilliseconds(ms)),
            EasingFunction = new CubicEase { EasingMode = EasingMode.EaseOut }, EnableDependentAnimation = false
        };
        Storyboard.SetTarget(animation, element);
        Storyboard.SetTargetProperty(animation, "Opacity");
        var storyboard = new Storyboard();
        storyboard.Children.Add(animation);
        return storyboard;
    }
}
