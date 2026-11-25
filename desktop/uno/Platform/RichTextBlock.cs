using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Documents;
using Microsoft.UI.Xaml.Media;
using Windows.ApplicationModel.DataTransfer;

namespace Nexus.Pages;

/// <summary>
/// Uno does not implement RichTextBlock (Uno0001): on macOS every chat
/// message, memory, brief, work-log note and Markdown preview rendered as
/// nothing. The shared pages build documents as RichTextBlock + Paragraphs;
/// in the Uno head this type (same namespace as the pages, so it wins over
/// Microsoft.UI.Xaml.Controls.RichTextBlock) keeps that API and shows each
/// paragraph as a selectable TextBlock with the paragraph's own inlines,
/// size, weight, font and colour. Tables (InlineUIContainer is not
/// implemented either) go in with <see cref="AddElement"/>.
/// </summary>
internal sealed partial class RichTextBlock : StackPanel
{
    private readonly Dictionary<Block, UIElement> _elements = new();
    private bool _built;
    private bool _selectable;
    private TextWrapping _wrapping = TextWrapping.Wrap;
    private double _fontSize = 14;
    private double _lineHeight;
    private Brush? _foreground;

    public RichTextBlock() => Spacing = 0;

    /// <summary>The paragraphs, in order. Read when the block is first measured.</summary>
    public List<Block> Blocks { get; } = new();

    public bool IsTextSelectionEnabled { get => _selectable; set { _selectable = value; Apply(); } }
    public TextWrapping TextWrapping { get => _wrapping; set { _wrapping = value; Apply(); } }
    public double FontSize { get => _fontSize; set { _fontSize = value; Apply(); } }
    public double LineHeight { get => _lineHeight; set { _lineHeight = value; Apply(); } }
    public Brush? Foreground { get => _foreground; set { _foreground = value; Apply(); } }

    /// <summary>A whole element (a Markdown table) as the next block.</summary>
    public void AddElement(UIElement element)
    {
        var placeholder = new Paragraph();
        _elements[placeholder] = element;
        Blocks.Add(placeholder);
    }

    /// <summary>Uno has no text positions here; links just skip the hover underline.</summary>
    public TextPointer? GetPositionFromPoint(Windows.Foundation.Point point) => null;

    public string SelectedText =>
        string.Join("\n", Lines().Select(t => t.SelectedText).Where(s => !string.IsNullOrEmpty(s)));

    public void SelectAll()
    {
        Build();
        foreach (var line in Lines()) line.SelectAll();
    }

    public void CopySelectionToClipboard()
    {
        var text = SelectedText;
        if (text.Length == 0) return;
        var data = new DataPackage();
        data.SetText(text);
        Clipboard.SetContent(data);
    }

    private IEnumerable<TextBlock> Lines() => Children.OfType<TextBlock>();

    protected override Windows.Foundation.Size MeasureOverride(Windows.Foundation.Size availableSize)
    {
        Build();
        return base.MeasureOverride(availableSize);
    }

    private void Build()
    {
        if (_built) return;
        _built = true;
        foreach (var block in Blocks)
        {
            if (_elements.TryGetValue(block, out var element))
            {
                if (element is FrameworkElement fe) fe.Margin = new Thickness(0, 4, 0, 10);
                Children.Add(element);
                continue;
            }
            if (block is not Paragraph paragraph) continue;
            var line = new TextBlock { Margin = new Thickness(0, 0, 0, 5) };
            var inlines = paragraph.Inlines.ToList();
            paragraph.Inlines.Clear();
            foreach (var inline in inlines) line.Inlines.Add(inline);
            if (IsSet(paragraph, TextElement.FontSizeProperty)) line.FontSize = paragraph.FontSize;
            if (IsSet(paragraph, TextElement.FontWeightProperty)) line.FontWeight = paragraph.FontWeight;
            if (IsSet(paragraph, TextElement.FontFamilyProperty)) line.FontFamily = paragraph.FontFamily;
            if (IsSet(paragraph, TextElement.ForegroundProperty)) line.Foreground = paragraph.Foreground;
            line.Tag = paragraph;
            Children.Add(line);
        }
        Apply();
    }

    private void Apply()
    {
        if (!_built) return;
        foreach (var line in Lines())
        {
            var paragraph = line.Tag as Paragraph;
            line.IsTextSelectionEnabled = _selectable;
            line.TextWrapping = _wrapping;
            if (paragraph is null || !IsSet(paragraph, TextElement.FontSizeProperty)) line.FontSize = _fontSize;
            if (_lineHeight > 0) line.LineHeight = _lineHeight * line.FontSize / Math.Max(1, _fontSize);
            if (_foreground is not null && (paragraph is null || !IsSet(paragraph, TextElement.ForegroundProperty))) line.Foreground = _foreground;
        }
    }

    private static bool IsSet(DependencyObject o, DependencyProperty p)
    {
        try { return o.ReadLocalValue(p) != DependencyProperty.UnsetValue; }
        catch { return false; }
    }
}
