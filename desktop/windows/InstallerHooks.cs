using System.Runtime.InteropServices;
using Nexus.Platform;
using Velopack;

namespace Nexus;

/// <summary>
/// Velopack install / update / uninstall hooks. Each runs in a short-lived
/// Nexus.exe process (30 second limit) and then the process exits; they
/// never start the UI or the Go core.
///
/// Install layout: %LOCALAPPDATA%\NexusDesktop\current\Nexus.exe. The pack
/// id is deliberately not "Nexus": %LOCALAPPDATA%\Nexus already holds the
/// shell's preferences and caches, and Velopack deletes its whole install
/// root on uninstall.
/// </summary>
public static class InstallerHooks
{
    public static void Run(string[] args)
    {
        VelopackApp.Build()
            .SetArgs(args)
            .OnAfterInstallFastCallback(_ => AfterInstall())
            .OnAfterUpdateFastCallback(_ => AfterInstall())
            .OnBeforeUninstallFastCallback(_ => BeforeUninstall())
            .Run();
    }

    // Install hooks always act on the real system: a NEXUS_PROFILE in the
    // environment isolates a running app, not the installation itself.
    private static IDesktopIntegration RealSystem() =>
        OperatingSystem.IsWindows() ? new WindowsDesktopIntegration() : DesktopIntegration.Current;

    // AppContext.BaseDirectory is the folder of Nexus.exe (...\NexusDesktop\current\),
    // so its parent is the install root. (Two levels up would be %LOCALAPPDATA%:
    // then everything under it, %TEMP% included, looked like "this install".)
    internal static string InstallRoot =>
        Path.GetDirectoryName(AppContext.BaseDirectory.TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar)) ?? "";

    private static void AfterInstall()
    {
        var integration = RealSystem();
        integration.RegisterUrlScheme();
        if (OperatingSystem.IsWindows()) LegacyInstall.Retire(integration);
    }

    private static void BeforeUninstall()
    {
        var root = InstallRoot;
        var integration = RealSystem();
        Try(() => integration.UnregisterUrlScheme(root));
        if (OperatingSystem.IsWindows())
        {
            Try(() =>
            {
                if (WindowsDesktopIntegration.IsUnder(WindowsDesktopIntegration.StartupTarget(), root))
                    integration.SetStartsAtLogin(false);
            });
            Try(() =>
            {
                if (WindowsDesktopIntegration.IsUnder(ShellLinks.Target(WindowsDesktopIntegration.DesktopShortcutPath), root))
                    integration.SetDesktopShortcut(false);
            });
            if (UserData.AskToRemove()) UserData.Remove();
        }
    }

    internal static void Try(Action action)
    {
        try { action(); }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or InvalidOperationException or COMException or System.Security.SecurityException)
        {
            // Best effort: an uninstall must never fail half way.
        }
    }
}

/// <summary>
/// Shortcuts left by the pre-installer era: scripts/install-windows.ps1
/// (Nexus.exe in %LOCALAPPDATA%\Nexus\bin) and the retired Fyne shell
/// (nexus-desktop*.exe, e.g. Start Menu\Programs\Nexus\Nexus Desktop.lnk).
/// After a real install they would open or autostart an old copy, or show
/// up as duplicate Start entries, so they are removed. A legacy autostart
/// or desktop shortcut is moved over to the installed copy so the user's
/// choice survives. The old binaries stay (the CLI on PATH lives there).
/// Runs after every install and update.
/// </summary>
[System.Runtime.Versioning.SupportedOSPlatform("windows")]
internal static class LegacyInstall
{
    private static string LegacyBin =>
        Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Nexus", "bin");

    public static void Retire(IDesktopIntegration integration)
    {
        var installRoot = InstallerHooks.InstallRoot;
        bool Legacy(string link) => IsLegacyTarget(ShellLinks.Target(link), installRoot, LegacyBin);

        var startup = Environment.GetFolderPath(Environment.SpecialFolder.Startup);
        foreach (var link in Links(startup, "*Nexus*.lnk").Where(Legacy))
        {
            InstallerHooks.Try(() => integration.SetStartsAtLogin(true));
            InstallerHooks.Try(() => File.Delete(link));
        }
        if (WindowsDesktopIntegration.IsUnder(WindowsDesktopIntegration.StartupTarget(), LegacyBin))
            InstallerHooks.Try(() => integration.SetStartsAtLogin(true));

        var programs = Environment.GetFolderPath(Environment.SpecialFolder.Programs);
        var folder = Path.Combine(programs, "Nexus");
        foreach (var link in Links(folder, "*.lnk").Concat(Links(programs, "Nexus Desktop.lnk")).Where(Legacy))
            InstallerHooks.Try(() => File.Delete(link));
        InstallerHooks.Try(() => { if (Directory.Exists(folder) && !Directory.EnumerateFileSystemEntries(folder).Any()) Directory.Delete(folder); });

        var desktop = Environment.GetFolderPath(Environment.SpecialFolder.DesktopDirectory);
        var hadDesktop = false;
        foreach (var link in Links(desktop, "Nexus*.lnk").Where(Legacy))
        {
            hadDesktop = true;
            InstallerHooks.Try(() => File.Delete(link));
        }
        if (hadDesktop) InstallerHooks.Try(() => integration.SetDesktopShortcut(true));
    }

    private static IEnumerable<string> Links(string dir, string pattern)
    {
        try { return Directory.Exists(dir) ? Directory.GetFiles(dir, pattern) : Array.Empty<string>(); }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException) { return Array.Empty<string>(); }
    }

    /// <summary>
    /// A shortcut target from an old Nexus: the script install's bin folder,
    /// the Fyne shell / daemon executables, or a Nexus.exe that no longer
    /// exists. Never the current installation.
    /// </summary>
    internal static bool IsLegacyTarget(string target, string installRoot, string legacyBin)
    {
        if (string.IsNullOrWhiteSpace(target)) return false;
        if (installRoot.Length > 0 && WindowsDesktopIntegration.IsUnder(target, installRoot)) return false;
        if (WindowsDesktopIntegration.IsUnder(target, legacyBin)) return true;
        var name = Path.GetFileName(target);
        if (name.StartsWith("nexus-desktop", StringComparison.OrdinalIgnoreCase) || name.StartsWith("nexus-daemon", StringComparison.OrdinalIgnoreCase)) return true;
        // A dangling Nexus.exe from an old manual install; nothing broader, so
        // other apps' shortcuts (e.g. "Nexus Mods") are never touched.
        return name.Equals("Nexus.exe", StringComparison.OrdinalIgnoreCase) && !File.Exists(target);
    }
}

/// <summary>Where Nexus keeps user data, and the uninstall-time choice to remove it.</summary>
[System.Runtime.Versioning.SupportedOSPlatform("windows")]
internal static class UserData
{
    private const uint MB_YESNO = 0x4, MB_ICONQUESTION = 0x20, MB_DEFBUTTON2 = 0x100, MB_SETFOREGROUND = 0x10000, MB_TOPMOST = 0x40000;
    private const int IDYES = 6;

    // Undocumented but stable since Windows XP; a plain MessageBox could
    // outlive Velopack's 30 second hook limit. Timing out keeps the data.
    [DllImport("user32.dll", EntryPoint = "MessageBoxTimeoutW", CharSet = CharSet.Unicode)]
    private static extern int MessageBoxTimeout(IntPtr hwnd, string text, string caption, uint type, ushort language, uint milliseconds);

    public static IEnumerable<string> Locations()
    {
        var roaming = Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData);
        var local = Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData);
        yield return Path.Combine(roaming, "central-memory"); // sign-in, server, capture choices (shared with the CLI)
        yield return Path.Combine(roaming, "Nexus");          // continuations, owned files
        yield return Path.Combine(local, "Nexus");            // window preferences, caches, logs
    }

    public static bool AskToRemove()
    {
        var text =
            "Nexus is being uninstalled.\n\n" +
            "Also remove your Nexus data from this computer? This signs this computer out and deletes local settings, caches and the capture choices in:\n\n" +
            string.Join("\n", Locations()) + "\n\n" +
            "Your chats and memories stay in your Nexus account either way.\n\n" +
            "Choose No to keep them for a later reinstall.";
        // Branded task dialog first; a plain message box if it can't show.
        if (UninstallPrompt.AskRemoveData(Locations()) is { } choice) return choice;
        try
        {
            return MessageBoxTimeout(IntPtr.Zero, text, "Uninstall Nexus", MB_YESNO | MB_ICONQUESTION | MB_DEFBUTTON2 | MB_SETFOREGROUND | MB_TOPMOST, 0, 25_000) == IDYES;
        }
        catch (Exception ex) when (ex is EntryPointNotFoundException or DllNotFoundException)
        {
            return false;
        }
    }

    public static void Remove()
    {
        var legacyBin = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Nexus", "bin");
        foreach (var dir in Locations())
        {
            if (!Directory.Exists(dir)) continue;
            foreach (var entry in Directory.EnumerateFileSystemEntries(dir))
            {
                // The legacy CLI install (on PATH) is not user data.
                if (string.Equals(entry, legacyBin, StringComparison.OrdinalIgnoreCase)) continue;
                InstallerHooks.Try(() =>
                {
                    if (Directory.Exists(entry)) Directory.Delete(entry, true);
                    else File.Delete(entry);
                });
            }
            InstallerHooks.Try(() => { if (!Directory.EnumerateFileSystemEntries(dir).Any()) Directory.Delete(dir); });
        }
    }
}
