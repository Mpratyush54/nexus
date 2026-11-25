using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Media;

namespace Nexus.Pages;

internal static class NexusPalette
{
    private static Brush Brush(string key) => ThemeService.Brush(key, Windows.UI.Color.FromArgb(0x80, 0x80, 0x80, 0x80));
    internal static Brush Accent => Brush("NexusAccentBrush");
    internal static Brush Teal => Brush("NexusTealBrush");
    internal static Brush Amber => Brush("NexusAmberBrush");
    internal static Brush UserFill => Brush("NexusUserFillBrush");
    internal static Brush AccentBorder => Brush("NexusAccentBorderBrush");
    internal static Brush AccentFill => Brush("NexusAccentFillBrush");
    internal static Brush Hairline => Brush("NexusHairlineBrush");
    internal static Brush Muted => Brush("NexusMutedTextBrush");
    internal static Brush Surface => Brush("NexusSurfaceBrush");

    // Work-log tints: quiet on black, still readable on white.
    private static bool IsDark => ThemeService.EffectiveIsDark;
    private static Brush Tint(byte dr, byte dg, byte db, byte lr, byte lg, byte lb) =>
        new SolidColorBrush(IsDark ? Windows.UI.Color.FromArgb(255, dr, dg, db) : Windows.UI.Color.FromArgb(255, lr, lg, lb));
    internal static Brush Danger => Tint(0xF2, 0x8B, 0x8B, 0xB9, 0x1C, 0x1C);
    internal static Brush DiffAdded => Tint(0x6E, 0xD3, 0x8F, 0x15, 0x80, 0x3D);
    internal static Brush DiffRemoved => Tint(0xF2, 0x8B, 0x8B, 0xB9, 0x1C, 0x1C);

    // Project badge colours are bright for dark surfaces; on white they are
    // darkened so the letters keep their contrast.
    internal static Windows.UI.Color BadgeInk(Windows.UI.Color color) => ThemeService.EffectiveIsDark
        ? color
        : Windows.UI.Color.FromArgb(255, (byte)(color.R * 0.55), (byte)(color.G * 0.55), (byte)(color.B * 0.55));
}
