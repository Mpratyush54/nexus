using Microsoft.UI;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Media;

namespace Nexus;

/// <summary>
/// Appearance: follow Windows, or always light / always dark.
///
/// The choice applies at launch through Application.RequestedTheme. Switching
/// while Nexus runs sets the window root's RequestedTheme (XAML-bound
/// resources follow it), and raises Changed so the shell rebuilds whatever
/// was built in code with brushes from <see cref="Lookup"/>.
/// </summary>
internal static class ThemeService
{
    public const string System = "system", Light = "light", Dark = "dark";

    public static event EventHandler? Changed;

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

    /// <summary>Shows a dialog in the theme in effect now.</summary>
    public static Windows.Foundation.IAsyncOperation<Microsoft.UI.Xaml.Controls.ContentDialogResult> ShowThemedAsync(this Microsoft.UI.Xaml.Controls.ContentDialog dialog)
    {
        Prepare(dialog);
        return dialog.ShowAsync();
    }

    /// <summary>Applies the saved preference to the window (also on system theme changes).</summary>
    public static void Apply(Window window)
    {
        var dark = EffectiveIsDark;
        var changed = s_applied is { } previous && previous != dark;
        s_applied = dark;
        if (window.Content is FrameworkElement root && !HighContrast)
            root.RequestedTheme = dark ? ElementTheme.Dark : ElementTheme.Light;
        ApplyCaptionButtons(window, dark);
        if (changed || s_raiseAlways) Changed?.Invoke(null, EventArgs.Empty);
        s_raiseAlways = false;
    }

    private static bool? s_applied;
    private static bool s_raiseAlways;

    /// <summary>The element theme every surface built in code should use.</summary>
    public static ElementTheme ElementTheme => HighContrast ? ElementTheme.Default : EffectiveIsDark ? ElementTheme.Dark : ElementTheme.Light;

    // Application.Current.Resources resolves theme dictionaries for the theme
    // Nexus was launched with, which cannot change while it runs. Code-built
    // UI therefore looks its brushes up here: the theme dictionary for the
    // theme in effect now (Nexus's own, then WinUI's defaults), falling back
    // to the plain lookup. Pages are rebuilt on Changed, so nothing keeps a
    // brush from the previous theme (this used to recolour shared brushes in
    // place, which left code-built panels black on a light window).
    public static object? Lookup(string key)
    {
        var resources = Application.Current?.Resources;
        if (resources is null) return null;
        var themes = HighContrast ? new[] { "HighContrast" } : EffectiveIsDark ? new[] { "Dark", "Default" } : new[] { "Light" };
        foreach (var theme in themes)
            if (TryFind(resources, theme, key, 0, out var value)) return value;
        return resources.TryGetValue(key, out var any) ? any : null;
    }

    public static Brush Brush(string key, Windows.UI.Color fallback) =>
        Lookup(key) is Brush brush ? brush : new SolidColorBrush(fallback);

    public static Windows.UI.Color Color(string key, Windows.UI.Color fallback) => Lookup(key) switch
    {
        Windows.UI.Color color => color,
        SolidColorBrush brush => brush.Color,
        _ => fallback
    };

    private static bool TryFind(ResourceDictionary dictionary, string theme, string key, int depth, out object? value)
    {
        value = null;
        if (depth > 6) return false;
        if (dictionary.ThemeDictionaries.TryGetValue(theme, out var themed) && themed is ResourceDictionary themeDictionary &&
            themeDictionary.TryGetValue(key, out value))
            return true;
        foreach (var merged in dictionary.MergedDictionaries)
            if (TryFind(merged, theme, key, depth + 1, out value)) return true;
        return false;
    }

    /// <summary>Dialogs live outside the window's element tree, so they take the app's launch theme unless told.</summary>
    public static void Prepare(Microsoft.UI.Xaml.Controls.ContentDialog dialog) => dialog.RequestedTheme = ElementTheme;

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
