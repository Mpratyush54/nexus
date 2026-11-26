#if !HAS_UNO
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core.Diagnostics;
using Nexus.Pages;

namespace Nexus;

/// <summary>
/// "Open in new window" from a chat's menu: the chat on its own, next to the
/// main window. It uses a page of its own (not the cached one the sidebar
/// shows), so both can be open at once; the agent keeps one run per chat.
/// Windows only for now.
/// </summary>
internal static class ChatWindow
{
    private static readonly Dictionary<string, Window> s_open = new(StringComparer.Ordinal);

    public static void Open(string sessionId, string title)
    {
        if (s_open.TryGetValue(sessionId, out var existing))
        {
            existing.Activate();
            return;
        }
        try
        {
            var page = new SessionPage();
            var frame = new Frame { Content = page, Background = ThemeService.Brush("NexusCanvasBrush", NexusTokens.BrandInk) };
            var root = new Grid { RequestedTheme = ThemeService.ElementTheme, Background = ThemeService.Brush("NexusCanvasBrush", NexusTokens.BrandInk) };
            root.Children.Add(frame);
            var window = new Window { Title = Nexus.Core.NxJson.Scrub(title) + " · Nexus", Content = root };
            try { window.AppWindow.SetIcon(Path.Combine(AppContext.BaseDirectory, "Assets", "Nexus.ico")); } catch { /* default icon */ }
            window.AppWindow.Resize(new Windows.Graphics.SizeInt32(900, 820));
            s_open[sessionId] = window;
            window.Closed += (_, _) => s_open.Remove(sessionId);
            page.Enter(sessionId);
            window.Activate();
            DiagLog.Info("shell", "chat opened in its own window");
        }
        catch (Exception ex)
        {
            DiagLog.Warn("shell", "could not open the chat in a new window", ex);
            Toast.Show("Couldn't open a new window. The chat is open here instead.");
            MainWindow.Current?.OpenSession(sessionId);
        }
    }
}
#endif
