using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Controls.Primitives;
using Microsoft.UI.Xaml.Documents;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// A chat message rendered from Markdown, built to be updated while it
/// streams. Prose goes into selectable rich-text documents (a few dozen
/// blocks each, so a long reply never re-lays out as one huge document);
/// code, tables, quotes, rules and image rows are their own elements.
/// <see cref="Update"/> keeps every block that did not change and rebuilds
/// only from the first one that did, so a streaming reply grows in place and
/// the finished message is the same element (no swap, no jump).
/// </summary>
internal sealed class MarkdownView
{
    // A prose document is closed after this many blocks or characters and the
    // next one starts: each stays cheap to lay out and to update.
    private const int SegmentBlocks = 40;
    private const int SegmentChars = 8_000;

    private sealed class Segment
    {
        public RichTextBlock? Doc;
        public FrameworkElement Element = null!;
        public int Blocks;
        public int Chars;
    }

    private sealed record Rendered(MdBlock Block, bool Partial, Segment Segment, CodeBlockView? Code);

    private readonly MarkdownStream _stream = new();
    private readonly List<Rendered> _rendered = new();
    private readonly List<Segment> _segments = new();
    private readonly double _fontSize;
    private FlyoutBase? _contextFlyout;
    private string _text = "";

    public MarkdownView(string text = "", bool streaming = false, double fontSize = 14)
    {
        _fontSize = fontSize;
        Root = new StackPanel { HorizontalAlignment = HorizontalAlignment.Stretch };
        Update(text, streaming);
    }

    /// The element to place in the chat.
    public StackPanel Root { get; }

    /// The Markdown last shown.
    public string Text => _text;

    /// True while the last update was a streaming one.
    public bool Streaming { get; private set; }

    /// Copy / select menu for the prose (applied to documents added later too).
    public FlyoutBase? ContextFlyout
    {
        get => _contextFlyout;
        set
        {
            _contextFlyout = value;
            foreach (var segment in _segments) if (segment.Doc is { } doc) doc.ContextFlyout = value;
        }
    }

    public string SelectedText => string.Join("\n", _segments.Select(s => s.Doc?.SelectedText ?? "").Where(t => t.Length > 0));

    public void SelectAll()
    {
        foreach (var segment in _segments) segment.Doc?.SelectAll();
    }

    /// Shows `text`. While streaming, the last block is still being written:
    /// its half-typed inline markers are hidden and an open fence grows.
    public void Update(string text, bool streaming)
    {
        text ??= "";
        Streaming = streaming;
        if (string.Equals(text, _text, StringComparison.Ordinal) && _rendered.All(r => !r.Partial || streaming)) return;
        _text = text;
        Interlocked.Increment(ref Nexus.Core.Diagnostics.UiStats.RenderedMessages);
        var blocks = _stream.Update(text);
        var last = blocks.Count - 1;
        var keep = 0;
        while (keep < _rendered.Count && keep < blocks.Count)
        {
            var have = _rendered[keep];
            var want = blocks[keep];
            var partial = streaming && keep == last;
            if (have.Partial == partial && (ReferenceEquals(have.Block, want) || have.Block.SameAs(want))) { keep++; continue; }
            // A fence that is still arriving: grow the same card.
            if (have.Code is { } code && want.Kind == MdKind.Code && keep == _rendered.Count - 1 &&
                string.Equals(code.Language, want.Language, StringComparison.Ordinal))
            {
                code.SetCode(want.Text, want.Language, want.Closed);
                _rendered[keep] = have with { Block = want, Partial = partial };
                keep++;
                continue;
            }
            break;
        }
        Truncate(keep);
        var touched = new HashSet<RichTextBlock>();
        for (var i = keep; i < blocks.Count; i++)
            Append(blocks[i], streaming && i == last, i == 0 ? null : blocks[i - 1], touched);
        foreach (var doc in touched) FileLinks.Attach(doc);
    }

    private void Truncate(int keep)
    {
        for (var i = _rendered.Count - 1; i >= keep; i--)
        {
            var segment = _rendered[i].Segment;
            _rendered.RemoveAt(i);
            if (segment.Doc is { } doc && segment.Blocks > 1)
            {
                DocText.RemoveLast(doc);
                segment.Blocks--;
                continue;
            }
            Root.Children.Remove(segment.Element);
            _segments.Remove(segment);
        }
        // Character counts only decide when to start a new document; the
        // open one is recounted after a truncation.
        if (_segments.Count > 0 && _segments[^1].Doc is not null)
        {
            var open = _segments[^1];
            open.Chars = 0;
            for (var i = _rendered.Count - 1; i >= 0 && _rendered[i].Segment == open; i--) open.Chars += _rendered[i].Block.Text.Length;
        }
    }

    private void Append(MdBlock block, bool partial, MdBlock? previous, HashSet<RichTextBlock> touched)
    {
        var gap = Gap(previous, block);
        if (Prose(block, partial))
        {
            var paragraph = MarkdownRender.Paragraph(block, partial, _fontSize);
            var segment = _segments.Count > 0 && _segments[^1].Doc is not null && _segments[^1].Blocks < SegmentBlocks && _segments[^1].Chars < SegmentChars
                ? _segments[^1] : null;
            if (segment is null)
            {
                var doc = MarkdownRender.NewDocument(_fontSize);
                doc.ContextFlyout = _contextFlyout;
                segment = new Segment { Doc = doc, Element = doc };
                doc.Margin = new Thickness(0, gap, 0, 0);
                paragraph.Margin = new Thickness(paragraph.Margin.Left, 0, 0, 0);
                AddSegment(segment);
            }
            else
            {
                paragraph.Margin = new Thickness(paragraph.Margin.Left, gap, 0, 0);
            }
            DocText.Add(segment.Doc!, paragraph);
            segment.Blocks++;
            segment.Chars += block.Text.Length;
            touched.Add(segment.Doc!);
            _rendered.Add(new Rendered(block, partial, segment, null));
            return;
        }
        CodeBlockView? code = null;
        FrameworkElement element;
        if (block.Kind == MdKind.Code)
        {
            // A saved reply folds a long block; a streamed one stays open so
            // finishing never collapses what the reader was looking at.
            code = new CodeBlockView(block.Text, block.Language, block.Closed, foldLong: !Streaming);
            element = code.View;
        }
        else if (partial && block.Kind == MdKind.Paragraph && MarkdownBlocks.IsTableRow(block.Text))
        {
            // A table header that is still arriving: nothing until its rule does.
            element = new Border { Height = 0 };
            gap = 0;
        }
        else
        {
            element = MarkdownRender.Element(block, _fontSize);
        }
        element.Margin = new Thickness(0, gap, 0, 0);
        var item = new Segment { Element = element };
        AddSegment(item);
        _rendered.Add(new Rendered(block, partial, item, code));
    }

    private void AddSegment(Segment segment)
    {
        _segments.Add(segment);
        Root.Children.Add(segment.Element);
    }

    // Prose blocks go into a document; the rest are elements.
    private static bool Prose(MdBlock block, bool partial) => block.Kind switch
    {
        MdKind.Paragraph => !(partial && MarkdownBlocks.IsTableRow(block.Text)) && !MarkdownRender.IsImageRow(block),
        MdKind.Heading or MdKind.ListItem => true,
        _ => false,
    };

    // Space above a block, from the block before it.
    internal static double Gap(MdBlock? previous, MdBlock block)
    {
        if (previous is null) return 0;
        if (block.Kind == MdKind.Heading) return block.Level <= 2 ? 20 : 16;
        if (previous.Kind == MdKind.Heading) return 6;
        if (block.Kind == MdKind.ListItem && previous.Kind == MdKind.ListItem) return 4;
        if (block.Kind == MdKind.ListItem && previous.Kind == MdKind.Paragraph) return 6;
        return 12;
    }
}

/// <summary>Builds the WinUI pieces of a Markdown message.</summary>
internal static class MarkdownRender
{
    public static FontFamily MonoFont => WorkLogVisuals.MonoFont;

    public static RichTextBlock NewDocument(double fontSize) => new()
    {
        IsTextSelectionEnabled = true,
        TextWrapping = TextWrapping.Wrap,
        FontSize = fontSize,
        LineHeight = Math.Round(fontSize * 1.6),
        HorizontalAlignment = HorizontalAlignment.Stretch,
    };

    /// <summary>
    /// The whole message as one selectable document (memory detail, file
    /// preview, work-log notes): code, tables and quotes sit inline.
    /// </summary>
    public static RichTextBlock Document(string markdown, double fontSize = 14)
    {
        var doc = NewDocument(fontSize);
        MdBlock? previous = null;
        foreach (var block in MarkdownBlocks.Parse(markdown))
        {
            var gap = MarkdownView.Gap(previous, block);
            previous = block;
            if (block.Kind is MdKind.Paragraph or MdKind.Heading or MdKind.ListItem && !IsImageRow(block))
            {
                var paragraph = Paragraph(block, partial: false, fontSize);
                paragraph.Margin = new Thickness(paragraph.Margin.Left, gap, 0, 0);
                doc.Blocks.Add(paragraph);
                continue;
            }
            FrameworkElement element = block.Kind == MdKind.Code
                ? new CodeBlockView(block.Text, block.Language, block.Closed).View
                : Element(block, fontSize);
#if HAS_UNO
            // Uno has no InlineUIContainer; the shim takes the element as a block.
            element.Margin = new Thickness(0, gap, 0, 0);
            doc.AddElement(element);
#else
            var host = new Paragraph { Margin = new Thickness(0, gap, 0, 0) };
            host.Inlines.Add(new InlineUIContainer { Child = element });
            doc.Blocks.Add(host);
            // An inline element is measured on its own: give cards the
            // document's width so they line up with the text.
            if (block.Kind is MdKind.Code or MdKind.Quote or MdKind.Rule)
                doc.SizeChanged += (_, e) => element.Width = Math.Max(0, e.NewSize.Width - 4);
#endif
        }
        FileLinks.Attach(doc);
        return doc;
    }

    public static Paragraph Paragraph(MdBlock block, bool partial, double fontSize)
    {
        var paragraph = new Paragraph();
        var text = partial ? MarkdownStream.Partial(block.Text) : block.Text;
        switch (block.Kind)
        {
            case MdKind.Heading:
                paragraph.FontSize = block.Level switch { 1 => fontSize + 6, 2 => fontSize + 3, 3 => fontSize + 1, _ => fontSize };
                paragraph.FontWeight = Microsoft.UI.Text.FontWeights.SemiBold;
                break;
            case MdKind.ListItem:
            {
                var marker = block.Task switch
                {
                    // U+FE0E: the text form, not the colour emoji.
                    true => "☑︎",
                    false => "☐︎",
                    _ => block.Ordinal.Length > 0 ? block.Ordinal : block.Number.Length > 0 ? block.Number : (block.Indent % 3) switch { 0 => "•", 1 => "◦", _ => "▪" },
                };
                // Hanging indent: wrapped lines line up under the text, not the marker.
                var hang = Math.Round((block.Task is null ? marker.Length : 1.4) * fontSize * 0.6 + fontSize * 0.55);
                paragraph.Margin = new Thickness(6 + hang + block.Indent * 18, 0, 0, 0);
#if !HAS_UNO
                paragraph.TextIndent = -hang;
#endif
                var run = new Run { Text = marker + " " };
                if (block.Number.Length == 0) run.Foreground = NexusPalette.Muted;
#if !HAS_UNO
                // Windows draws the ballot boxes as colour emoji; the icon
                // font's check boxes match the rest of the app.
                if (block.Task is { } done)
                {
                    paragraph.Inlines.Add(new Run { Text = done ? "" : "", FontFamily = WorkLogVisuals.IconFont, FontSize = fontSize - 1, Foreground = NexusPalette.Muted });
                    run.Text = " ";
                }
#endif
                paragraph.Inlines.Add(run);
                break;
            }
        }
        AddInlines(paragraph.Inlines, MarkdownInline.Parse(text.Length > 0 ? text : " "));
        if (block.Kind == MdKind.ListItem && block.Task == true)
            foreach (var inline in paragraph.Inlines.Skip(1)) inline.Foreground = NexusPalette.Muted;
        return paragraph;
    }

    /// Code (handled by the caller), tables, quotes, rules and image rows.
    public static FrameworkElement Element(MdBlock block, double fontSize) => block.Kind switch
    {
        MdKind.Table => Table(block, fontSize),
        MdKind.Quote => Quote(block, fontSize),
        MdKind.Rule => new Border
        {
            Height = 1, Background = NexusPalette.Hairline, Margin = new Thickness(0, 4, 0, 4),
            HorizontalAlignment = HorizontalAlignment.Stretch
        },
        MdKind.Code => new CodeBlockView(block.Text, block.Language, block.Closed).View,
        MdKind.Paragraph when IsImageRow(block) => Images(block),
        _ => Fallback(block, fontSize),
    };

    private static FrameworkElement Fallback(MdBlock block, double fontSize)
    {
        var doc = NewDocument(fontSize);
        doc.Blocks.Add(Paragraph(block, false, fontSize));
        return doc;
    }

    private static FrameworkElement Quote(MdBlock block, double fontSize)
    {
        var doc = NewDocument(fontSize);
        doc.Foreground = NexusPalette.Muted;
        var nested = MarkdownBlocks.Parse(block.Text);
        MdBlock? previous = null;
        foreach (var inner in nested.Count > 0 ? nested : new List<MdBlock> { new() { Kind = MdKind.Paragraph, Text = block.Text } })
        {
            var paragraph = Paragraph(inner.Kind is MdKind.Paragraph or MdKind.Heading or MdKind.ListItem ? inner : new MdBlock { Kind = MdKind.Paragraph, Text = inner.Text }, false, fontSize);
            paragraph.Margin = new Thickness(paragraph.Margin.Left, MarkdownView.Gap(previous, inner), 0, 0);
            previous = inner;
            doc.Blocks.Add(paragraph);
        }
        FileLinks.Attach(doc);
        return new Border
        {
            Child = doc, BorderThickness = new Thickness(3, 0, 0, 0), BorderBrush = NexusPalette.Hairline,
            Padding = new Thickness(14, 2, 0, 2), HorizontalAlignment = HorizontalAlignment.Stretch
        };
    }

    private static FrameworkElement Table(MdBlock block, double fontSize)
    {
        var cells = block.Rows.Select(MarkdownBlocks.TableCells).ToList();
        var columns = cells.Count == 0 ? 0 : cells.Max(r => r.Count);
        var grid = new Grid { HorizontalAlignment = HorizontalAlignment.Left };
        for (var c = 0; c < columns; c++)
            grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto, MaxWidth = 420 });
        for (var r = 0; r < cells.Count; r++)
        {
            grid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
            var band = new Border
            {
                Background = r == 0 ? CodePalette.HeaderFill : null,
                BorderBrush = NexusPalette.Hairline,
                BorderThickness = new Thickness(0, r == 0 ? 0 : 1, 0, 0),
            };
            Grid.SetRow(band, r);
            Grid.SetColumnSpan(band, Math.Max(1, columns));
            grid.Children.Add(band);
            for (var c = 0; c < columns; c++)
            {
                // TextBlock, not RichTextBlock: tables have many cells.
                var cell = new TextBlock
                {
                    IsTextSelectionEnabled = true, TextWrapping = TextWrapping.Wrap, FontSize = fontSize - 0.5,
                    LineHeight = Math.Round((fontSize - 0.5) * 1.45), Margin = new Thickness(12, 7, 12, 7), MaxWidth = 396,
                };
                if (r == 0) cell.FontWeight = Microsoft.UI.Text.FontWeights.SemiBold;
                var text = c < cells[r].Count ? cells[r][c] : "";
                AddInlines(cell.Inlines, MarkdownInline.Parse(text.Length > 0 ? text : " "));
                Grid.SetRow(cell, r);
                Grid.SetColumn(cell, c);
                grid.Children.Add(cell);
            }
        }
        var scroll = new ScrollViewer
        {
            Content = grid,
            HorizontalScrollMode = ScrollMode.Auto, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto,
            VerticalScrollMode = ScrollMode.Disabled, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled,
        };
        var card = new Border
        {
            Child = scroll, CornerRadius = new CornerRadius(8), BorderThickness = new Thickness(1),
            BorderBrush = CodePalette.Border, HorizontalAlignment = HorizontalAlignment.Left, MaxWidth = 900,
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(card, $"Table, {Math.Max(0, cells.Count - 1)} rows, {columns} columns");
        return card;
    }

    // A paragraph that is only images (and spaces): a row of thumbnails when
    // the files are on this computer.
    public static bool IsImageRow(MdBlock block)
    {
        if (block.Kind != MdKind.Paragraph || !block.Text.Contains("![", StringComparison.Ordinal)) return false;
        var items = MarkdownInline.Parse(block.Text);
        return items.Any(i => i.Kind == MdSpan.Image) &&
               items.All(i => i.Kind == MdSpan.Image ? LocalImage(i.Url) is not null : string.IsNullOrWhiteSpace(i.Text));
    }

    private static FrameworkElement Images(MdBlock block)
    {
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        foreach (var image in MarkdownInline.Parse(block.Text).Where(i => i.Kind == MdSpan.Image))
            if (LocalImage(image.Url) is { } path)
                row.Children.Add(ChatImages.Thumbnail(path, image.Text.Length > 0 ? image.Text : System.IO.Path.GetFileName(path), 160));
        return new ScrollViewer
        {
            Content = row, HorizontalScrollMode = ScrollMode.Auto, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto,
            VerticalScrollMode = ScrollMode.Disabled, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled,
        };
    }

    private static string? LocalImage(string url)
    {
        try
        {
            var path = url.StartsWith("file:", StringComparison.OrdinalIgnoreCase) && Uri.TryCreate(url, UriKind.Absolute, out var uri) ? uri.LocalPath : url;
            path = Uri.UnescapeDataString(path);
            if (!System.IO.Path.IsPathRooted(path)) return null;
            var ext = System.IO.Path.GetExtension(path).ToLowerInvariant();
            if (ext is not (".png" or ".jpg" or ".jpeg" or ".gif" or ".webp" or ".bmp")) return null;
            return System.IO.File.Exists(path) ? path : null;
        }
        catch (Exception)
        {
            return null;
        }
    }

    public static void AddInlines(InlineCollection inlines, IEnumerable<MdInline> items)
    {
        foreach (var item in items)
        {
            switch (item.Kind)
            {
                case MdSpan.Text:
                    FileLinks.AddText(inlines, item.Text, mono: false);
                    break;
                case MdSpan.LineBreak:
                    inlines.Add(new LineBreak());
                    break;
                case MdSpan.Bold:
                {
                    var span = new Span { FontWeight = Microsoft.UI.Text.FontWeights.SemiBold };
                    AddInlines(span.Inlines, item.Items);
                    inlines.Add(span);
                    break;
                }
                case MdSpan.Italic:
                {
                    var span = new Span { FontStyle = Windows.UI.Text.FontStyle.Italic };
                    AddInlines(span.Inlines, item.Items);
                    inlines.Add(span);
                    break;
                }
                case MdSpan.Strike:
                {
                    var span = new Span { TextDecorations = Windows.UI.Text.TextDecorations.Strikethrough };
                    AddInlines(span.Inlines, item.Items);
                    inlines.Add(span);
                    break;
                }
                case MdSpan.Code:
                {
                    var span = new Span { Foreground = CodePalette.InlineInk };
                    FileLinks.AddText(span.Inlines, item.Text, mono: true);
                    inlines.Add(span);
                    break;
                }
                case MdSpan.Link:
                    inlines.Add(Link(item));
                    break;
                case MdSpan.Image:
                {
                    var label = "🖼 " + (item.Text.Length > 0 ? item.Text : "image");
                    if (LocalImage(item.Url) is { } path)
                    {
                        var link = new Hyperlink { UnderlineStyle = UnderlineStyle.None, Foreground = NexusPalette.Accent };
                        link.Inlines.Add(new Run { Text = label });
                        link.Click += async (sender, _) => await ChatImages.ShowAsync(sender.XamlRoot, path, item.Text);
                        inlines.Add(link);
                    }
                    else if (MarkdownInline.IsWebUrl(item.Url))
                    {
                        inlines.Add(WebLink(label, item.Url));
                    }
                    else
                    {
                        inlines.Add(new Run { Text = label, Foreground = NexusPalette.Muted });
                    }
                    break;
                }
            }
        }
    }

    private static Inline Link(MdInline item)
    {
        if (MarkdownInline.IsWebUrl(item.Url)) return WebLink(item, item.Url);
        if (FileRefs.ParseLinkTarget(item.Url) is { } target) return FileLinks.Link(MarkdownInline.Plain(item.Items.Count > 0 ? item.Items : new[] { item }), target);
        var span = new Span { Foreground = NexusPalette.Accent };
        AddInlines(span.Inlines, item.Items.Count > 0 ? item.Items : new[] { new MdInline(MdSpan.Text, item.Text) });
        return span;
    }

    private static Hyperlink WebLink(MdInline item, string url)
    {
        var link = WebLink("", url);
        link.Inlines.Clear();
        // A hyperlink cannot hold another one: its children are text and emphasis.
        foreach (var child in item.Items.Count > 0 ? item.Items : new[] { new MdInline(MdSpan.Text, item.Text) })
        {
            if (child.Kind is MdSpan.Link or MdSpan.Image) link.Inlines.Add(new Run { Text = MarkdownInline.Plain(new[] { child }) });
            else
            {
                var span = new Span();
                AddPlainInlines(span.Inlines, child);
                link.Inlines.Add(span);
            }
        }
        return link;
    }

    private static void AddPlainInlines(InlineCollection inlines, MdInline item)
    {
        switch (item.Kind)
        {
            case MdSpan.Bold:
            {
                var span = new Span { FontWeight = Microsoft.UI.Text.FontWeights.SemiBold };
                foreach (var c in item.Items) AddPlainInlines(span.Inlines, c);
                inlines.Add(span);
                break;
            }
            case MdSpan.Italic:
            {
                var span = new Span { FontStyle = Windows.UI.Text.FontStyle.Italic };
                foreach (var c in item.Items) AddPlainInlines(span.Inlines, c);
                inlines.Add(span);
                break;
            }
            case MdSpan.Code:
                inlines.Add(new Run { Text = item.Text, FontFamily = MonoFont, FontSize = 12.5 });
                break;
            case MdSpan.LineBreak:
                inlines.Add(new LineBreak());
                break;
            default:
                inlines.Add(new Run { Text = MarkdownInline.Plain(new[] { item }) });
                break;
        }
    }

    private static Hyperlink WebLink(string text, string url)
    {
        var link = new Hyperlink { UnderlineStyle = UnderlineStyle.None, Foreground = NexusPalette.Accent };
        try { link.NavigateUri = new Uri(url); }
        catch (UriFormatException) { }
        if (text.Length > 0) link.Inlines.Add(new Run { Text = text });
        return link;
    }
}

/// Adding and removing paragraphs of a document that is already on screen.
/// (The Uno head's RichTextBlock is a panel of text blocks; see its shim.)
internal static class DocText
{
    public static void Add(RichTextBlock doc, Block block)
    {
#if HAS_UNO
        doc.AppendBlock(block);
#else
        doc.Blocks.Add(block);
#endif
    }

    public static void RemoveLast(RichTextBlock doc)
    {
#if HAS_UNO
        doc.RemoveLastBlock();
#else
        if (doc.Blocks.Count > 0) doc.Blocks.RemoveAt(doc.Blocks.Count - 1);
#endif
    }
}
