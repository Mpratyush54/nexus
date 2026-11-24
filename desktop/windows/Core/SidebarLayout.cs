namespace Nexus.Core;

internal static class SidebarLayout
{
    // The chat list gets the remaining space, never the footer's space.
    // Keep this independent of WinUI so short/high-DPI layouts are testable.
    internal static double ChatHeight(double viewportHeight, double paneTop, double footerHeight)
    {
        if (!double.IsFinite(viewportHeight) || !double.IsFinite(paneTop) || !double.IsFinite(footerHeight)) return 0;
        return Math.Max(0, viewportHeight - Math.Max(0, paneTop) - Math.Max(0, footerHeight) - 24);
    }
}
