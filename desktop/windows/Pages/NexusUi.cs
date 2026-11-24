using System.Numerics;
using Microsoft.UI.Composition;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Hosting;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Media.Animation;

namespace Nexus.Pages;

/// <summary>
/// Spacing, type and shape tokens for code-built UI. The same values live in
/// App.xaml (NexusSpace*, NexusFont*, NexusRadius*) for XAML.
/// </summary>
internal static class NexusTokens
{
    // 4px grid.
    public const double Space1 = 4, Space2 = 8, Space3 = 12, Space4 = 16, Space6 = 24, Space8 = 32;

    // Type ramp.
    public const double FontPageTitle = 22, FontSection = 15, FontBody = 13.5, FontCaption = 12, FontMicro = 11;

    public static readonly Thickness PagePadding = new(32, 20, 32, 32);
    public static readonly CornerRadius RadiusSmall = new(6), RadiusMedium = new(10), RadiusLarge = new(14);

    // Readable column for text-heavy pages.
    public const double ContentMaxWidth = 860;

    // Brand colours (logo, landing page, portal, installer). The themed
    // accent tokens in App.xaml derive from these; use the tokens for UI and
    // these only as fallbacks or for theme-independent art.
    public static readonly Windows.UI.Color BrandGold = Windows.UI.Color.FromArgb(255, 0xE8, 0xBB, 0x67);
    public static readonly Windows.UI.Color BrandGoldDeep = Windows.UI.Color.FromArgb(255, 0x8A, 0x5F, 0x12); // gold text on white, 5.6:1
    public static readonly Windows.UI.Color BrandInk = Windows.UI.Color.FromArgb(255, 0x0E, 0x0F, 0x0E);
    public static readonly Windows.UI.Color BrandCream = Windows.UI.Color.FromArgb(255, 0xF4, 0xF1, 0xE9);

    public static Brush Brush(string key, Windows.UI.Color fallback) => ThemeService.Brush(key, fallback);

    public static TextBlock PageTitle(string text) => new()
    {
        Text = text, FontSize = FontPageTitle, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
        TextWrapping = TextWrapping.Wrap, LineHeight = 28
    };

    public static TextBlock SectionTitle(string text) => new()
    {
        Text = text, FontSize = FontSection, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, TextWrapping = TextWrapping.Wrap
    };

    public static TextBlock Caption(string text) => new()
    {
        Text = text, FontSize = FontCaption, TextWrapping = TextWrapping.Wrap, Foreground = NexusPalette.Muted
    };
}

/// <summary>
/// Motion that honours Windows' "Animation effects" setting
/// (UISettings.AnimationsEnabled). Everything here runs on the compositor,
/// off the UI thread, and is skipped entirely when animations are off.
/// </summary>
internal static partial class Motion
{
    private static readonly Windows.UI.ViewManagement.UISettings s_settings = new();

    public static bool Enabled
    {
        get
        {
            try { return s_settings.AnimationsEnabled; }
            catch { return true; }
        }
    }

    public static readonly TimeSpan Fast = TimeSpan.FromMilliseconds(150);
    public static readonly TimeSpan Normal = TimeSpan.FromMilliseconds(240);

    /// <summary>Page change: content rises 12px and fades in.</summary>
    public static void ApplyPageTransitions(ContentControl host)
    {
        host.ContentTransitions = Enabled
            ? new TransitionCollection { new ContentThemeTransition { VerticalOffset = 12, HorizontalOffset = 0 } }
            : null;
    }

    /// <summary>Fades an element in while it rises <paramref name="rise"/> px.</summary>
    public static void FadeIn(UIElement element, double rise = 8, int delayMs = 0, TimeSpan? duration = null)
    {
        if (!Enabled) return;
        try
        {
            var visual = ElementCompositionPreview.GetElementVisual(element);
            var compositor = visual.Compositor;
            var length = duration ?? Normal;
            var ease = compositor.CreateCubicBezierEasingFunction(new Vector2(0.1f, 0.9f), new Vector2(0.2f, 1f));
            var fade = compositor.CreateScalarKeyFrameAnimation();
            fade.InsertKeyFrame(0f, 0f);
            fade.InsertKeyFrame(1f, 1f, ease);
            fade.Duration = length;
            fade.DelayTime = TimeSpan.FromMilliseconds(delayMs);
            fade.DelayBehavior = AnimationDelayBehavior.SetInitialValueBeforeDelay;
            visual.StartAnimation("Opacity", fade);
            if (rise != 0)
            {
                ElementCompositionPreview.SetIsTranslationEnabled(element, true);
                var slide = compositor.CreateVector3KeyFrameAnimation();
                slide.InsertKeyFrame(0f, new Vector3(0, (float)rise, 0));
                slide.InsertKeyFrame(1f, Vector3.Zero, ease);
                slide.Duration = length;
                slide.DelayTime = TimeSpan.FromMilliseconds(delayMs);
                slide.DelayBehavior = AnimationDelayBehavior.SetInitialValueBeforeDelay;
                visual.StartAnimation("Translation", slide);
            }
        }
        catch
        {
            // Motion is decoration; never let it break a page.
        }
    }

    /// <summary>Staggered entrance for the first rows of a freshly shown list.</summary>
    public static void Stagger(IEnumerable<UIElement> elements, int stepMs = 22, int maxItems = 12)
    {
        var index = 0;
        foreach (var element in elements)
        {
            if (index >= maxItems) break;
            FadeIn(element, 6, index * stepMs, TimeSpan.FromMilliseconds(200));
            index++;
        }
    }

    /// <summary>A gentle scale on press for card-like buttons (hover stays the control's own).</summary>
    public static void AddPressFeedback(UIElement element)
    {
        if (!Enabled) return;
        element.PointerPressed += (_, _) => Scale(element, 0.985f);
        element.PointerReleased += (_, _) => Scale(element, 1f);
        element.PointerCaptureLost += (_, _) => Scale(element, 1f);
        element.PointerExited += (_, _) => Scale(element, 1f);
    }

    private static void Scale(UIElement element, float to)
    {
        try
        {
            var visual = ElementCompositionPreview.GetElementVisual(element);
            if (element is FrameworkElement fe) visual.CenterPoint = new Vector3((float)fe.ActualWidth / 2, (float)fe.ActualHeight / 2, 0);
            var animation = visual.Compositor.CreateVector3KeyFrameAnimation();
            animation.InsertKeyFrame(1f, new Vector3(to, to, 1));
            animation.Duration = TimeSpan.FromMilliseconds(to < 1 ? 90 : 160);
            visual.StartAnimation("Scale", animation);
        }
        catch { }
    }

    // Connected animation from a chat row's title to the chat header.
    public const string ChatTitleKey = "nexus.chat.title";

    public static void PrepareChatTitle(UIElement source)
    {
        if (!Enabled) return;
        try
        {
            var service = ConnectedAnimationService.GetForCurrentView();
            service.DefaultDuration = TimeSpan.FromMilliseconds(260);
            service.PrepareToAnimate(ChatTitleKey, source);
        }
        catch { }
    }

    public static void StartChatTitle(UIElement destination)
    {
        try
        {
            var animation = ConnectedAnimationService.GetForCurrentView().GetAnimation(ChatTitleKey);
            if (animation is null) return;
            if (!Enabled) { animation.Cancel(); return; }
            animation.Configuration = new BasicConnectedAnimationConfiguration();
            animation.TryStart(destination);
        }
        catch { }
    }
}

/// <summary>
/// Skeleton placeholders with a soft shimmer, shown while content loads
/// instead of "Loading…" text. The shimmer is a composition gradient sweep
/// (no UI-thread work) and is static when animations are off.
/// </summary>
internal static class Skeleton
{
    public static FrameworkElement Block(double width, double height, double radius = 6)
    {
        var block = new Border
        {
            Width = double.IsNaN(width) ? double.NaN : width,
            Height = height,
            CornerRadius = new CornerRadius(radius),
            Background = NexusTokens.Brush("NexusSkeletonBrush", Windows.UI.Color.FromArgb(20, 255, 255, 255)),
            HorizontalAlignment = double.IsNaN(width) ? HorizontalAlignment.Stretch : HorizontalAlignment.Left
        };
        AttachShimmer(block, radius);
        return block;
    }

    /// <summary>A few lines of text-shaped placeholders.</summary>
    public static FrameworkElement Lines(int count = 3, double lineHeight = 12)
    {
        var stack = new StackPanel { Spacing = 10 };
        var widths = new[] { 0.92, 0.78, 0.86, 0.6, 0.7 };
        for (var i = 0; i < count; i++)
        {
            var row = new Grid();
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(widths[i % widths.Length], GridUnitType.Star) });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1 - widths[i % widths.Length], GridUnitType.Star) });
            row.Children.Add(Block(double.NaN, lineHeight, lineHeight / 2));
            stack.Children.Add(row);
        }
        return Wrap(stack, "Loading");
    }

    /// <summary>Placeholder rows shaped like the sidebar's chat rows.</summary>
    public static FrameworkElement ChatRows(int count = 6)
    {
        var stack = new StackPanel { Spacing = 18, Margin = new Thickness(10, 10, 10, 0) };
        for (var i = 0; i < count; i++)
        {
            var row = new StackPanel { Spacing = 7 };
            row.Children.Add(Block(90 + (i * 37 % 50), 9, 4));
            row.Children.Add(Block(170 + (i * 53 % 70), 12, 5));
            row.Children.Add(Block(64, 9, 4));
            stack.Children.Add(row);
        }
        return Wrap(stack, "Loading chats");
    }

    /// <summary>Placeholder cards for list pages (memories, agents, shared chats).</summary>
    public static FrameworkElement Cards(int count = 3)
    {
        var stack = new StackPanel { Spacing = 10 };
        for (var i = 0; i < count; i++)
        {
            var inner = new StackPanel { Spacing = 10 };
            inner.Children.Add(Block(160 + (i * 41 % 90), 13, 6));
            inner.Children.Add(Block(double.NaN, 10, 5));
            inner.Children.Add(Block(260 + (i * 29 % 120), 10, 5));
            stack.Children.Add(new Border
            {
                Padding = new Thickness(18, 16, 18, 16), CornerRadius = NexusTokens.RadiusLarge, BorderThickness = new Thickness(1),
                BorderBrush = NexusTokens.Brush("CardStrokeColorDefaultBrush", Windows.UI.Color.FromArgb(20, 255, 255, 255)),
                Child = inner
            });
        }
        return Wrap(stack, "Loading");
    }

    private static FrameworkElement Wrap(FrameworkElement content, string name)
    {
        AutomationProperties.SetName(content, name);
        AutomationProperties.SetLiveSetting(content, Microsoft.UI.Xaml.Automation.Peers.AutomationLiveSetting.Polite);
        return content;
    }

    private static void AttachShimmer(Border host, double radius)
    {
        if (!Motion.Enabled) return;
        SpriteVisual? sprite = null;
        CompositionLinearGradientBrush? brush = null;
        void Size()
        {
            if (sprite is null || brush is null) return;
            var w = (float)host.ActualWidth; var h = (float)host.ActualHeight;
            sprite.Size = new Vector2(w, h);
            brush.StartPoint = new Vector2(0, 0);
            brush.EndPoint = new Vector2(Math.Max(w, 1), 0);
            if (sprite.Clip is CompositionGeometricClip { Geometry: CompositionRoundedRectangleGeometry rounded }) rounded.Size = new Vector2(w, h);
            var sweep = sprite.Compositor.CreateScalarKeyFrameAnimation();
            sweep.InsertKeyFrame(0f, -w);
            sweep.InsertKeyFrame(1f, w);
            sweep.Duration = TimeSpan.FromMilliseconds(1400);
            sweep.IterationBehavior = AnimationIterationBehavior.Forever;
            brush.StartAnimation("Offset.X", sweep);
        }
        host.Loaded += (_, _) =>
        {
            try
            {
                var compositor = ElementCompositionPreview.GetElementVisual(host).Compositor;
                var highlight = ThemeService.Color("NexusSkeletonHighlightColor", Windows.UI.Color.FromArgb(36, 255, 255, 255));
                var clear = Windows.UI.Color.FromArgb(0, highlight.R, highlight.G, highlight.B);
                brush = compositor.CreateLinearGradientBrush();
                brush.MappingMode = CompositionMappingMode.Absolute;
                brush.ColorStops.Add(compositor.CreateColorGradientStop(0.25f, clear));
                brush.ColorStops.Add(compositor.CreateColorGradientStop(0.5f, highlight));
                brush.ColorStops.Add(compositor.CreateColorGradientStop(0.75f, clear));
                sprite = compositor.CreateSpriteVisual();
                sprite.Brush = brush;
                var geometry = compositor.CreateRoundedRectangleGeometry();
                geometry.CornerRadius = new Vector2((float)radius, (float)radius);
                sprite.Clip = compositor.CreateGeometricClip(geometry);
                ElementCompositionPreview.SetElementChildVisual(host, sprite);
                Size();
            }
            catch { }
        };
        host.SizeChanged += (_, _) => Size();
        host.Unloaded += (_, _) =>
        {
            ElementCompositionPreview.SetElementChildVisual(host, null);
            sprite?.Dispose(); brush?.Dispose();
            sprite = null; brush = null;
        };
    }
}

/// <summary>Empty and error states with one clear next step.</summary>
internal static class StateView
{
    public static FrameworkElement Empty(string glyph, string title, string body, string? actionLabel = null, Action? action = null, bool primary = false)
    {
        var stack = new StackPanel { Spacing = 6, HorizontalAlignment = HorizontalAlignment.Left, MaxWidth = 520 };
        stack.Children.Add(new Border
        {
            Width = 40, Height = 40, CornerRadius = new CornerRadius(12), Margin = new Thickness(0, 0, 0, 8),
            Background = NexusPalette.AccentFill, HorizontalAlignment = HorizontalAlignment.Left,
            Child = new FontIcon { Glyph = glyph, FontSize = 18, Opacity = 0.85, HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center }
        });
        stack.Children.Add(NexusTokens.SectionTitle(title));
        if (body.Length > 0) stack.Children.Add(new TextBlock { Text = body, FontSize = NexusTokens.FontBody, Opacity = 0.7, TextWrapping = TextWrapping.Wrap, LineHeight = 20 });
        if (actionLabel is { Length: > 0 } && action is not null)
        {
            var button = new Button { Content = actionLabel, Margin = new Thickness(0, 10, 0, 0), Padding = new Thickness(14, 6, 14, 7) };
            if (primary && Application.Current.Resources.TryGetValue("AccentButtonStyle", out var style) && style is Style accent) button.Style = accent;
            button.Click += (_, _) => action();
            stack.Children.Add(button);
        }
        var host = new Border { Padding = new Thickness(4, 24, 4, 24), Child = stack };
        AutomationProperties.SetName(host, title);
        Motion.FadeIn(host, 6);
        return host;
    }

    public static FrameworkElement Error(string title, string? detail, Action? retry)
    {
        var plain = Nexus.Core.NxErrors.IsSignedOut(detail) ? Nexus.Core.NxErrors.SignedOutMessage : Friendly(detail);
        var stack = new StackPanel { Spacing = 6, MaxWidth = 560, HorizontalAlignment = HorizontalAlignment.Left };
        var head = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
        head.Children.Add(new FontIcon { Glyph = "", FontSize = 16, Foreground = NexusTokens.Brush("NexusDangerBrush", Windows.UI.Color.FromArgb(255, 0xF2, 0x8B, 0x8B)), VerticalAlignment = VerticalAlignment.Center });
        head.Children.Add(NexusTokens.SectionTitle(title));
        stack.Children.Add(head);
        if (plain.Length > 0) stack.Children.Add(new TextBlock { Text = plain, FontSize = NexusTokens.FontBody, Opacity = 0.75, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true });
        if (retry is not null)
        {
            var button = new Button { Content = "Try again", Margin = new Thickness(0, 8, 0, 0) };
            button.Click += (_, _) => retry();
            stack.Children.Add(button);
        }
        var host = new Border
        {
            Padding = new Thickness(18, 16, 18, 16), CornerRadius = NexusTokens.RadiusLarge, BorderThickness = new Thickness(1),
            Background = NexusTokens.Brush("NexusDangerFillBrush", Windows.UI.Color.FromArgb(26, 0xF2, 0x8B, 0x8B)),
            BorderBrush = NexusTokens.Brush("NexusHairlineBrush", Windows.UI.Color.FromArgb(20, 255, 255, 255)),
            Child = stack
        };
        AutomationProperties.SetName(host, title);
        AutomationProperties.SetLiveSetting(host, Microsoft.UI.Xaml.Automation.Peers.AutomationLiveSetting.Assertive);
        return host;
    }

    /// <summary>Turns transport errors into a sentence a person can act on.</summary>
    public static string Friendly(string? error)
    {
        var text = (error ?? "").Trim();
        if (text.Length == 0) return "";
        var lower = text.ToLowerInvariant();
        if (lower.Contains("no such host") || lower.Contains("connection refused") || lower.Contains("actively refused") ||
            lower.Contains("timeout") || lower.Contains("timed out") || lower.Contains("network"))
            return "Nexus couldn't reach the server. Check your connection; Nexus keeps retrying in the background.";
        return text;
    }
}
