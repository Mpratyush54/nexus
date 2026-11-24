using System.Runtime.CompilerServices;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Documents;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

// A page that can show a file in its own side panel (the chat page).
internal interface IFilePreviewHost
{
    void ShowFilePreview(FileRef reference);
}

// Turns file references in rendered messages into quiet links: accent text,
// underlined only under the pointer. The text itself is never changed.
internal static class FileLinks
{
    private static readonly ConditionalWeakTable<Run, Hyperlink> RunLinks = new();
    private static readonly ConditionalWeakTable<Hyperlink, FrameworkElement> Owners = new();
    private static readonly ConditionalWeakTable<RichTextBlock, object> Attached = new();
    private static FontFamily Mono => new("Cascadia Mono, Consolas");

    // Plain or inline-code text, with any file references linked.
    public static void AddText(InlineCollection inlines, string text, bool mono)
    {
        var start = 0;
        foreach (var span in FileRefs.Find(text))
        {
            if (span.Index > start) inlines.Add(TextRun(text[start..span.Index], mono));
            inlines.Add(Link(text.Substring(span.Index, span.Length), span.Ref, mono));
            start = span.Index + span.Length;
        }
        if (start < text.Length) inlines.Add(TextRun(text[start..], mono));
    }

    public static Hyperlink Link(string text, FileRef reference, bool mono = false)
    {
        var link = new Hyperlink { UnderlineStyle = UnderlineStyle.None, Foreground = NexusPalette.Accent };
        var run = TextRun(text, mono);
        link.Inlines.Add(run);
        RunLinks.AddOrUpdate(run, link);
        link.Click += (sender, _) =>
        {
            Owners.TryGetValue(sender, out var owner);
            Open(owner, sender.XamlRoot, reference);
        };
        return link;
    }

    // Call once a document is built: links learn their owner (to find the
    // chat page on click) and get the hover underline. Table cells too.
    public static void Attach(RichTextBlock document)
    {
        if (Attached.TryGetValue(document, out _)) return;
        Attached.Add(document, new object());
        var any = false;
        foreach (var block in document.Blocks)
            if (block is Paragraph paragraph) any |= Walk(paragraph.Inlines, document);
        if (!any) return;
        Hyperlink? hovered = null;
        void Hover(Hyperlink? next)
        {
            if (ReferenceEquals(next, hovered)) return;
            if (hovered is not null) hovered.UnderlineStyle = UnderlineStyle.None;
            hovered = next;
            if (hovered is not null) hovered.UnderlineStyle = UnderlineStyle.Single;
        }
        document.PointerMoved += (_, e) =>
        {
            try
            {
                var position = document.GetPositionFromPoint(e.GetCurrentPoint(document).Position);
                Hover(position?.Parent switch
                {
                    Hyperlink link => link,
                    Run run when RunLinks.TryGetValue(run, out var link) => link,
                    _ => null
                });
            }
            catch (Exception) { Hover(null); }
        };
        document.PointerExited += (_, _) => Hover(null);
    }

    private static bool Walk(InlineCollection inlines, RichTextBlock owner)
    {
        var any = false;
        foreach (var inline in inlines)
        {
            switch (inline)
            {
                case Hyperlink link:
                    Owners.AddOrUpdate(link, owner);
                    any = true;
                    break;
                case Span span:
                    any |= Walk(span.Inlines, owner);
                    break;
                case InlineUIContainer { Child: Panel panel }:
                    foreach (var cell in panel.Children.OfType<RichTextBlock>()) Attach(cell);
                    break;
            }
        }
        return any;
    }

    // Shows the file in the nearest chat page's panel, or in a dialog when
    // the message is rendered elsewhere (e.g. memory review).
    public static void Open(DependencyObject? source, XamlRoot? root, FileRef reference)
    {
        for (var node = source; node is not null; node = VisualTreeHelper.GetParent(node))
        {
            if (node is IFilePreviewHost host)
            {
                host.ShowFilePreview(reference);
                return;
            }
        }
        if (root is not null) _ = FilePreviewPanel.ShowDialogAsync(root, reference, "");
    }

    private static Run TextRun(string text, bool mono)
    {
        var run = new Run { Text = text };
        if (mono) { run.FontFamily = Mono; run.FontSize = 12.5; }
        return run;
    }
}
