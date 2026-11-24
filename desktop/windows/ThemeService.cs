using Microsoft.UI;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Media;

namespace Nexus;

/// <summary>
/// Appearance: follow Windows, or always light / always dark.
///
/// The choice applies at launch through Application.RequestedTheme, so
/// every brush resolves for the right theme. Switching while Nexus runs sets
/// the window root's RequestedTheme (XAML-bound resources follow it) and
/// recolours the brushes of the app's active theme dictionary in place, so
/// pages that looked brushes up in code follow too, without a restart.
/// </summary>
internal static class ThemeService
{
    public const string System = "system", Light = "light", Dark = "dark";

    public static event EventHandler? Changed;

    private static readonly Dictionary<SolidColorBrush, Windows.UI.Color> s_original = new(ReferenceEqualityComparer.Instance);
    private static readonly Windows.UI.ViewManagement.UISettings s_ui = new();

    public static string Preference => Normalize(AppPrefs.Data.Theme);

    public static string Normalize(string? value) => (value ?? "").Trim().ToLowerInvariant() switch
    {
        Light => Light,
        Dark => Dark,
        _ => System
    };

    /// <summary>Called from App's constructor, before any window exists.</summary>
    public static void ApplyAtLaunch(Application app)
    {
        switch (Preference)
        {
            case Light: app.RequestedTheme = ApplicationTheme.Light; break;
            case Dark: app.RequestedTheme = ApplicationTheme.Dark; break;
        }
    }

    public static bool SystemIsDark()
    {
        try
        {
            var background = s_ui.GetColorValue(Windows.UI.ViewManagement.UIColorType.Background);
            return background.R + background.G + background.B < 384;
        }
        catch { return true; }
    }

    public static bool EffectiveIsDark => Preference switch
    {
        Light => false,
        Dark => true,
        _ => SystemIsDark()
    };

    private static bool HighContrast
    {
        get
        {
            try { return new Windows.UI.ViewManagement.AccessibilitySettings().HighContrast; }
            catch { return false; }
        }
    }

    /// <summary>Follows Windows' light/dark switch while the preference is "system".</summary>
    public static void Watch(Window window)
    {
        s_ui.ColorValuesChanged += (_, _) =>
        {
            if (Preference == System) window.DispatcherQueue.TryEnqueue(() => Apply(window));
        };
    }

    /// <summary>Saves and applies a new preference to a running window.</summary>
    public static void Set(Window window, string preference)
    {
        AppPrefs.Data.Theme = Normalize(preference);
        AppPrefs.Save();
        Apply(window);
    }

    /// <summary>Applies the saved preference to the window (also on system theme changes).</summary>
    public static void Apply(Window window)
    {
        var dark = EffectiveIsDark;
        if (window.Content is FrameworkElement root && !HighContrast)
            root.RequestedTheme = dark ? ElementTheme.Dark : ElementTheme.Light;
        MirrorActiveDictionary(dark);
        ApplyCaptionButtons(window, dark);
        Changed?.Invoke(null, EventArgs.Empty);
    }

    // Brushes code looked up through Application.Resources come from the
    // theme dictionary of the launch theme. When the effective theme differs,
    // copy the other dictionary's colours onto those same brush objects.
    private static void MirrorActiveDictionary(bool dark)
    {
        if (HighContrast) return;
        var resources = Application.Current.Resources;
        var launchDark = Application.Current.RequestedTheme == ApplicationTheme.Dark;
        if (!resources.ThemeDictionaries.TryGetValue(launchDark ? "Dark" : "Light", out var activeObject) || activeObject is not ResourceDictionary active)
            return;
        if (dark == launchDark)
        {
            foreach (var (brush, color) in s_original) brush.Color = color;
            return;
        }
        if (!resources.ThemeDictionaries.TryGetValue(dark ? "Dark" : "Light", out var sourceObject) || sourceObject is not ResourceDictionary source)
            return;
        foreach (var (key, value) in source)
        {
            if (value is not SolidColorBrush wanted) continue;
            // Both the app dictionary's brush and whatever a code lookup
            // resolves to (it can come from the WinUI defaults) are recoloured.
            if (active.TryGetValue(key, out var current) && current is SolidColorBrush brush) Recolor(brush, wanted.Color);
            if (resources.TryGetValue(key, out var resolved) && resolved is SolidColorBrush lookedUp) Recolor(lookedUp, wanted.Color);
        }
    }

    private static void Recolor(SolidColorBrush brush, Windows.UI.Color color)
    {
        if (!s_original.ContainsKey(brush)) s_original[brush] = brush.Color;
        brush.Color = color;
    }

    // The content runs under the caption buttons, so they are coloured to
    // match the theme instead of the system defaults.
    public static void ApplyCaptionButtons(Window window, bool dark)
    {
        try
        {
            var bar = window.AppWindow.TitleBar;
            var ink = dark ? Windows.UI.Color.FromArgb(255, 0xE5, 0xE5, 0xE5) : Windows.UI.Color.FromArgb(255, 0x27, 0x27, 0x2A);
            bar.ButtonBackgroundColor = Colors.Transparent;
            bar.ButtonInactiveBackgroundColor = Colors.Transparent;
            bar.ButtonForegroundColor = ink;
            bar.ButtonInactiveForegroundColor = Windows.UI.Color.FromArgb(255, 0x8A, 0x8A, 0x8F);
            bar.ButtonHoverBackgroundColor = dark ? Windows.UI.Color.FromArgb(24, 255, 255, 255) : Windows.UI.Color.FromArgb(18, 0, 0, 0);
            bar.ButtonHoverForegroundColor = ink;
            bar.ButtonPressedBackgroundColor = dark ? Windows.UI.Color.FromArgb(14, 255, 255, 255) : Windows.UI.Color.FromArgb(10, 0, 0, 0);
            bar.ButtonPressedForegroundColor = ink;
        }
        catch
        {
            // Older Windows builds without title bar customisation.
        }
    }
}
