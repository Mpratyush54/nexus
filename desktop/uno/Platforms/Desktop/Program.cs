using Nexus.Platform;
using Uno.UI.Hosting;

namespace Nexus;

/// <summary>
/// Skia desktop entry point (macOS AppKit, and Win32 for the Windows
/// preview). The Windows head's Program.cs runs Velopack's installer hooks
/// first; there is no installer on this head yet.
/// </summary>
public static class Program
{
    [STAThread]
    public static int Main(string[] args)
    {
        if (OperatingSystem.IsMacOS())
        {
            if (args.Length == 0) args = Environment.GetCommandLineArgs().Skip(1).ToArray();
            // Flags only: links can carry sign-in codes.
            var flags = string.Join(" ", args.Where(a => a.StartsWith("--", StringComparison.Ordinal)).Select(a => a.Split('=')[0]));
            Console.Error.WriteLine($"[nexus-mac] launch: {args.Length} argument(s) {flags}");
            // NEXUS_PROFILE before anything reads per-user folders.
            Nexus.Core.ProfilePaths.ApplyToProcess();

            // Command-line form of Settings > Start at login (same code path),
            // used by scripts and CI: --set-start-at-login=on|off.
            var login = args.FirstOrDefault(a => a.StartsWith("--set-start-at-login=", StringComparison.OrdinalIgnoreCase));
            if (login is not null)
            {
                return SetStartAtLogin(login["--set-start-at-login=".Length..]);
            }

            // Before the host exists, so a second launch never shows a Dock icon.
            if (!SingleInstance.ClaimOrForward(args))
            {
                Console.Error.WriteLine("[nexus-mac] Nexus is already running; handed this launch to it");
                return 0;
            }
            MacShell.ListenForLinks();
        }

        var host = UnoPlatformHostBuilder.Create()
            .App(() => new App())
            .UseMacOS()
            .UseWin32()
            .UseX11()
            .Build();
        host.Run();
        return 0;
    }

    private static int SetStartAtLogin(string value)
    {
        var on = value.Equals("on", StringComparison.OrdinalIgnoreCase) || value == "1" || value.Equals("true", StringComparison.OrdinalIgnoreCase);
        try
        {
            LoginStart.SetEnabled(on);
            var where = DesktopIntegration.Current is MacDesktopIntegration mac ? mac.AgentPath : DesktopIntegration.Current.GetType().Name;
            Console.WriteLine($"start-at-login: {(LoginStart.IsEnabled() ? "on" : "off")} ({where})");
            return LoginStart.IsEnabled() == on ? 0 : 1;
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine("start-at-login: " + ex.Message);
            return 1;
        }
    }
}
