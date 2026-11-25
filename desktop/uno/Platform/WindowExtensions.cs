using Microsoft.UI.Xaml;

namespace Nexus;

/// <summary>
/// Stand-in for H.NotifyIcon's WindowExtensions (Windows tray package) on the
/// Uno head: show and hide through AppWindow.
/// </summary>
public static partial class WindowExtensions
{
    public static void Show(Window window)
    {
        window.AppWindow.Show();
        window.Activate();
    }

    public static void Hide(Window window) => window.AppWindow.Hide();
}
