using System.Runtime.CompilerServices;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;

namespace Nexus.Pages;

// Transcript text size (Ctrl/Cmd + / − / 0, Settings › General › Text size).
// The transcript's text is built in code with explicit font sizes, so the
// scale is applied by walking the transcript and multiplying each text
// element's own size, remembered the first time it is seen. New messages are
// scaled as they arrive. At 100% nothing is walked.
public sealed partial class SessionPage
{
    private static readonly ConditionalWeakTable<DependencyObject, StrongBox<(double Font, double Line)>> s_baseFontSize = new();
    private double _textScale = 1;
    private bool _textScaleHooked;
    private bool _textScaleQueued;
    private int _textScaledChildren;

    /// <summary>Sets the transcript text scale (1 = 100%).</summary>
    internal void ApplyTextScale(double scale)
    {
        if (!_textScaleHooked)
        {
            _textScaleHooked = true;
            // Streaming and paging add or grow messages: rescale what changed.
            _chat.LayoutUpdated += (_, _) => QueueTextScale();
        }
        if (Math.Abs(scale - _textScale) < 0.001 && Math.Abs(scale - 1) < 0.001) return;
        _textScale = scale;
        var scaled = ScaleTree(_chat);
        _textScaledChildren = _chat.Children.Count;
        Nexus.Core.Diagnostics.DiagLog.Info("zoom", $"transcript text {Math.Round(scale * 100)}% ({scaled} text elements)");
    }

    private void QueueTextScale()
    {
        if (Math.Abs(_textScale - 1) < 0.001 || _textScaleQueued || DispatcherQueue is null) return;
        _textScaleQueued = true;
        DispatcherQueue.TryEnqueue(Microsoft.UI.Dispatching.DispatcherQueuePriority.Low, () =>
        {
            _textScaleQueued = false;
            var count = _chat.Children.Count;
            if (count != _textScaledChildren)
            {
                // Messages added (or older ones prepended): everything once.
                ScaleTree(_chat);
                _textScaledChildren = count;
                return;
            }
            // Otherwise only the newest few can be growing (a streaming reply).
            for (var i = Math.Max(0, count - 3); i < count; i++) ScaleTree(_chat.Children[i]);
        });
    }

    private int ScaleTree(DependencyObject root)
    {
        var scaled = 0;
        var stack = new Stack<DependencyObject>();
        stack.Push(root);
        var guard = 0;
        while (stack.Count > 0 && guard++ < 60000)
        {
            var node = stack.Pop();
            switch (node)
            {
                case TextBlock text: scaled++; Scale(text, text.FontSize, text.LineHeight, v => text.FontSize = v, v => text.LineHeight = v); break;
                case RichTextBlock rich:
                    scaled++;
                    Scale(rich, rich.FontSize, rich.LineHeight, v => rich.FontSize = v, v => rich.LineHeight = v);
                    // Paragraphs carry their own size (headings, code).
                    foreach (var block in rich.Blocks)
                    {
                        Scale(block, block.FontSize, block.LineHeight, v => block.FontSize = v, v => block.LineHeight = v);
                        if (block is Microsoft.UI.Xaml.Documents.Paragraph paragraph)
                            foreach (var inline in paragraph.Inlines)
                                if (inline.ReadLocalValue(Microsoft.UI.Xaml.Documents.TextElement.FontSizeProperty) != DependencyProperty.UnsetValue)
                                    Scale(inline, inline.FontSize, 0, v => inline.FontSize = v, _ => { });
                    }
                    break;
                case Control control when node is not ScrollViewer: Scale(control, control.FontSize, 0, v => control.FontSize = v, _ => { }); break;
            }
            var count = VisualTreeHelper.GetChildrenCount(node);
            for (var i = 0; i < count; i++) stack.Push(VisualTreeHelper.GetChild(node, i));
        }
        return scaled;
    }

    private void Scale(DependencyObject element, double current, double line, Action<double> set, Action<double> setLine)
    {
        if (current <= 0 || double.IsNaN(current)) return;
        var box = s_baseFontSize.GetValue(element, _ => new StrongBox<(double, double)>((current, line)));
        var target = Math.Round(box.Value.Font * _textScale, 1);
        if (Math.Abs(current - target) > 0.05) set(target);
        // A fixed line height would squeeze bigger text: it grows with it.
        if (box.Value.Line > 0)
        {
            var lineTarget = Math.Round(box.Value.Line * _textScale, 1);
            if (Math.Abs(line - lineTarget) > 0.05) setLine(lineTarget);
        }
    }
}
