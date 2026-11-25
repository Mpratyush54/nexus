using Microsoft.UI.Xaml;
using Nexus.Platform;

namespace Nexus;

/// <summary>
/// Stand-in for H.NotifyIcon's WindowExtensions (Windows tray package) on the
/// Uno head. On macOS the window is ordered out and back in through AppKit
/// (libnexusmac), so it returns from the menu bar item or the Dock; elsewhere
/// through AppWindow.
/// </summary>
public static partial class WindowExtensions
{
    public static void Show(Window window)
    {
        if (MacNative.Available && MacNative.ShowWindows())
        {
            window.Activate();
            return;
        }
        window.AppWindow.Show();
        window.Activate();
    }

    public static void Hide(Window window)
    {
        if (MacNative.Available && MacNative.HideWindows()) return;
        window.AppWindow.Hide();
    }
}
