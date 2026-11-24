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
            .OnAfterUpdateFastCallback(_ => RealSystem().RegisterUrlScheme())
            .OnBeforeUninstallFastCallback(_ => BeforeUninstall())
            .Run();
    }

    // Install hooks always act on the real system: a NEXUS_PROFILE in the
    // environment isolates a running app, not the installation itself.
    private static IDesktopIntegration RealSystem() =>
        OperatingSystem.IsWindows() ? new WindowsDesktopIntegration() : DesktopIntegration.Current;

    private static string InstallRoot =>
        // ...\NexusDesktop\current\Nexus.exe -> ...\NexusDesktop
        Path.GetDirectoryName(Path.GetDirectoryName(AppContext.BaseDirectory.TrimEnd(Path.DirectorySeparatorChar)) ?? "") ?? "";

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
/// The pre-installer setup (scripts/install-windows.ps1) put Nexus.exe in
/// %LOCALAPPDATA%\Nexus\bin with Startup / Start menu / desktop shortcuts.
/// After a real install those would start the old copy at sign-in or open
/// it from the Start menu, so they are moved over to the installed copy.
/// The old files stay (the CLI on PATH lives there).
/// </summary>
[System.Runtime.Versioning.SupportedOSPlatform("windows")]
internal static class LegacyInstall
{
    private static string LegacyBin =>
        Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Nexus", "bin");

    public static void Retire(IDesktopIntegration integration)
    {
        var startup = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.Startup), "Nexus.lnk");
        if (PointsAtLegacy(startup))
        {
            InstallerHooks.Try(() => integration.SetStartsAtLogin(true));
            InstallerHooks.Try(() => File.Delete(startup));
        }
        if (WindowsDesktopIntegration.IsUnder(WindowsDesktopIntegration.StartupTarget(), LegacyBin))
            InstallerHooks.Try(() => integration.SetStartsAtLogin(true));
        var menu = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.Programs), "Nexus", "Nexus.lnk");
        if (PointsAtLegacy(menu))
        {
            InstallerHooks.Try(() => File.Delete(menu));
            InstallerHooks.Try(() => Directory.Delete(Path.GetDirectoryName(menu)!, false));
        }
        if (PointsAtLegacy(WindowsDesktopIntegration.DesktopShortcutPath))
            InstallerHooks.Try(() => integration.SetDesktopShortcut(true));
    }

    private static bool PointsAtLegacy(string link) =>
        File.Exists(link) && WindowsDesktopIntegration.IsUnder(ShellLinks.Target(link), LegacyBin);
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
