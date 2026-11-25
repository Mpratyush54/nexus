using System.Diagnostics;

namespace Nexus;

/// <summary>
/// User-facing words that differ by platform, in one place. The pages are
/// shared by the Windows head and the Uno (macOS) head; every sentence that
/// names the OS, where Nexus waits in the background, the file manager or a
/// shortcut goes through here, so macOS never reads "system tray" or "Ctrl".
/// The Windows head always gets the Windows wording.
/// </summary>
internal static class PlatformText
{
    public static bool IsMac =>
#if HAS_UNO
        OperatingSystem.IsMacOS();
#else
        false;
#endif

    /// <summary>"Windows" / "macOS".</summary>
    public static string Os => IsMac ? "macOS" : "Windows";

    /// <summary>Where Nexus waits while its window is closed: "the system tray" / "the menu bar".</summary>
    public static string TrayPlace => IsMac ? "the menu bar" : "the system tray";

    /// <summary>"the tray icon" / "the Nexus icon in the menu bar".</summary>
    public static string TrayIcon => IsMac ? "the Nexus icon in the menu bar" : "the tray icon";

    /// <summary>"File Explorer" / "Finder".</summary>
    public static string FileManager => IsMac ? "Finder" : "File Explorer";

    /// <summary>The theme picker's "follow the OS" choice.</summary>
    public static string SystemThemeChoice => IsMac ? "Use macOS setting" : "Use Windows setting";

    /// <summary>The account Nexus runs under: "Windows account" / "Mac user account".</summary>
    public static string UserAccount => IsMac ? "Mac user account" : "Windows account";

    /// <summary>"Ctrl+K" / "⌘K".</summary>
    public static string Shortcut(string key) => Shortcuts.Label(key);

    /// <summary>Opens a file in its default app. Uno's Launcher.LaunchFileAsync
    /// is not implemented on macOS, so the Mac asks `open`.</summary>
    public static async Task OpenFileAsync(string path)
    {
        if (IsMac)
        {
            var info = new ProcessStartInfo("/usr/bin/open") { UseShellExecute = false };
            info.ArgumentList.Add(path);
            Process.Start(info);
            return;
        }
        await Windows.System.Launcher.LaunchFileAsync(await Windows.Storage.StorageFile.GetFileFromPathAsync(path));
    }

    /// <summary>Shows a file (selected) or a folder in File Explorer or Finder.</summary>
    public static void Reveal(string path)
    {
        if (IsMac)
        {
            var info = new ProcessStartInfo("/usr/bin/open") { UseShellExecute = false };
            if (File.Exists(path)) info.ArgumentList.Add("-R");
            info.ArgumentList.Add(path);
            Process.Start(info);
            return;
        }
        if (File.Exists(path)) Process.Start(new ProcessStartInfo("explorer.exe", "/select,\"" + path + "\"") { UseShellExecute = false });
        else Process.Start(new ProcessStartInfo("explorer.exe", "\"" + path + "\"") { UseShellExecute = false });
    }
}
