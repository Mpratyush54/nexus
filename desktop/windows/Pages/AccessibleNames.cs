using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Controls.Primitives;
using Microsoft.UI.Xaml.Media;

namespace Nexus.Pages;

/// <summary>
/// A safety net for accessible names. Controls built in code with a panel
/// or an icon as content have no name for screen readers; this gives each
/// unnamed interactive control one from what it shows (its text, tooltip,
/// header or placeholder). Explicit AutomationProperties.Name always wins.
/// </summary>
internal static class AccessibleNames
{
    public static void Fill(DependencyObject? root)
    {
        if (root is null) return;
        var stack = new Stack<DependencyObject>();
        stack.Push(root);
        var visited = 0;
        while (stack.Count > 0 && visited++ < 6000)
        {
            var node = stack.Pop();
            if (node is Control control && IsInteractive(control) && string.IsNullOrWhiteSpace(AutomationProperties.GetName(control)))
            {
                var name = Derive(control);
                if (name.Length > 0) AutomationProperties.SetName(control, name);
            }
            var count = VisualTreeHelper.GetChildrenCount(node);
            for (var i = 0; i < count; i++) stack.Push(VisualTreeHelper.GetChild(node, i));
        }
    }

    private static bool IsInteractive(Control control) =>
        control is ButtonBase or ComboBox or ToggleSwitch or Slider or AutoSuggestBox or TextBox or PasswordBox;

    private static string Derive(Control control)
    {
        switch (control)
        {
            case ContentControl { Content: string text } when text.Trim().Length > 0:
                return "";
            case ComboBox combo:
                return First(combo.Header as string, combo.PlaceholderText, Tip(combo));
            case PasswordBox password:
                return First(password.Header as string, password.PlaceholderText, Tip(password));
            case TextBox box:
                return First(box.Header as string, box.PlaceholderText, Tip(box));
            case ToggleSwitch toggle:
                return First(toggle.Header as string, Tip(toggle));
        }
        var tip = Tip(control);
        if (tip.Length > 0) return tip;
        if (control is ContentControl content && content.Content is DependencyObject inner) return Text(inner);
        return "";
    }

    private static string Tip(DependencyObject element) => ToolTipService.GetToolTip(element) switch
    {
        string text => text.Split('\n')[0].Trim(),
        ToolTip { Content: string text } => text.Split('\n')[0].Trim(),
        _ => ""
    };

    private static string First(params string?[] values) => values.FirstOrDefault(v => !string.IsNullOrWhiteSpace(v))?.Trim() ?? "";

    // The visible words inside a control, in reading order, kept short.
    private static string Text(DependencyObject root)
    {
        var parts = new List<string>();
        void Walk(DependencyObject node)
        {
            if (parts.Count >= 3) return;
            if (node is TextBlock { Text: { Length: > 0 } text } block && block.Visibility == Visibility.Visible && !IsGlyph(block)) parts.Add(text.Trim());
            if (node is Panel panel) foreach (var child in panel.Children) Walk(child);
            else if (node is Border { Child: { } child }) Walk(child);
            else if (node is ContentControl { Content: DependencyObject content }) Walk(content);
        }
        Walk(root);
        var joined = string.Join(", ", parts.Where(p => p.Length > 0));
        return joined.Length > 120 ? joined[..120] + "…" : joined;
    }

    private static bool IsGlyph(TextBlock block) => block.Text.Length <= 2 && block.Text.All(c => c >= '\uE000' && c <= '\uF8FF');
}

/// <summary>Keeps something the person just expanded on screen.</summary>
internal static class Reveal
{
    /// <summary>
    /// After the next layout pass, scrolls so the expanded element is in
    /// view: its top under the fold when it is taller than the viewport,
    /// otherwise just enough to show all of it. The chat keeps its bottom
    /// anchored, so without this an expanded work log grew upward and its
    /// header slid out of view.
    /// </summary>
    public static void AfterExpand(FrameworkElement element, bool alignTop = false)
    {
        void Once(object? sender, object e)
        {
            element.LayoutUpdated -= Once;
            var viewport = FindScroller(element)?.ViewportHeight ?? 0;
            var tall = alignTop || (viewport > 0 && element.ActualHeight > viewport * 0.9);
            element.StartBringIntoView(tall
                ? new BringIntoViewOptions { VerticalAlignmentRatio = 0.02, AnimationDesired = Motion.Enabled }
                : new BringIntoViewOptions { AnimationDesired = Motion.Enabled });
        }
        // The chat follows its newest message; an expanded item in the
        // middle must not be scrolled away again by that.
        for (DependencyObject? node = element; node is not null; node = VisualTreeHelper.GetParent(node))
            if (node is SessionPage page) { page.StopFollowingBottom("expanded an item"); break; }
        element.LayoutUpdated += Once;
        element.InvalidateMeasure();
    }

    private static ScrollViewer? FindScroller(DependencyObject? node)
    {
        while (node is not null)
        {
            node = VisualTreeHelper.GetParent(node);
            if (node is ScrollViewer scroller) return scroller;
        }
        return null;
    }
}
