using System.Text.Json;
using Microsoft.UI.Xaml;
using Nexus.Core;

namespace Nexus;

public partial class App : Application
{
    private Window? _window;
    public static NxClient Core { get; } = new();
    public static string? PendingSessionId { get; private set; }
    public static string? PendingTeleportId { get; private set; }
    public static string? PendingAuthUrl { get; private set; }

    public App()
    {
        InitializeComponent();
    }

    protected override void OnLaunched(LaunchActivatedEventArgs args)
    {
        var argv = Environment.GetCommandLineArgs();
        if (!SingleInstance.ClaimOrForward(argv))
        {
            Environment.Exit(0);
            return;
        }

        var background = argv.Any(a => string.Equals(a, "--background", StringComparison.OrdinalIgnoreCase));
        ReadDeepLink(argv);

        Core.Init(JsonSerializer.Serialize(new
        {
            online = true,
            // An explicit workspace is safest. During development, accepting
            // the current directory only when it is a git worktree keeps an
            // installed Nexus.exe from accidentally harvesting its own files.
            workspace_root = CaptureWorkspaceRoot()
        }));
        var window = new MainWindow();
        _window = window;
        SingleInstance.Listen(payload =>
            window.DispatcherQueue.TryEnqueue(() => window.HandleRemote(payload)));
        // --background still creates the HWND so the tray icon exists, then hides it.
        window.Activate();
        if (background)
        {
            window.AppWindow.Hide();
        }
    }

    public static void ClearPending()
    {
        PendingSessionId = null;
        PendingTeleportId = null;
        PendingAuthUrl = null;
    }

    private static string CaptureWorkspaceRoot()
    {
        var configured = Environment.GetEnvironmentVariable("NEXUS_WORKSPACE");
        if (!string.IsNullOrWhiteSpace(configured) && Directory.Exists(configured))
        {
            return configured;
        }
        var current = Directory.GetCurrentDirectory();
        return Directory.Exists(Path.Combine(current, ".git")) ? current : "";
    }

    public static void ReadDeepLink(string[] argv)
    {
        foreach (var arg in argv)
        {
            if (!arg.StartsWith("nexus://", StringComparison.OrdinalIgnoreCase))
            {
                continue;
            }
            var path = arg["nexus://".Length..].Trim('/');
            var parts = path.Split('/', 2, StringSplitOptions.RemoveEmptyEntries);
            if (parts.Length == 2 && parts[0].Equals("session", StringComparison.OrdinalIgnoreCase))
            {
                PendingSessionId = Uri.UnescapeDataString(parts[1]);
            }
            else if (parts.Length == 2 && parts[0].Equals("teleport", StringComparison.OrdinalIgnoreCase))
            {
                PendingTeleportId = Uri.UnescapeDataString(parts[1]);
            }
            else if (parts.Length >= 1 && parts[0].Equals("auth", StringComparison.OrdinalIgnoreCase))
            {
                PendingAuthUrl = arg;
            }
        }
    }
}
