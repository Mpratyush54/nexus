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
        // Logging and the AppDomain/TaskScheduler crash hooks come first, so
        // anything that fails from here on leaves a trace.
        AppDiagnostics.Init();
        InitializeComponent();
        UnhandledException += OnUnhandledException;
        // The theme must be chosen before the first window exists.
        AppPrefs.Load();
        ThemeService.ApplyAtLaunch(this);
    }

    private void OnUnhandledException(object sender, Microsoft.UI.Xaml.UnhandledExceptionEventArgs e)
    {
        // Written synchronously: the next launch offers to send it.
        AppDiagnostics.RecordCrash(Nexus.Core.Diagnostics.ProblemReport.KindCrash, e.Exception, "Application.UnhandledException");
        if (_window is not null)
        {
            e.Handled = true;
        }
    }

    protected override void OnLaunched(LaunchActivatedEventArgs args)
    {
        // NEXUS_PROFILE: must run before anything loads the Go core, which
        // reads per-user folders from this process's environment.
        ProfilePaths.ApplyToProcess();
#if HAS_UNO
        // Uno's macOS launcher does not forward argv to the runtime.
        var argv = Nexus.Platform.LaunchArgs.All();
#else
        var argv = Environment.GetCommandLineArgs();
#endif
        if (!SingleInstance.ClaimOrForward(argv))
        {
            Nexus.Core.Diagnostics.DiagLog.Info("startup", "another Nexus is running; handed the launch to it");
            Environment.Exit(0);
            return;
        }
        if (argv.Any(arg => arg.Equals("--quit", StringComparison.OrdinalIgnoreCase)))
        {
            SingleInstance.Stop();
            Environment.Exit(0);
            return;
        }

        // A profiled instance never claims the per-user nexus:// handler,
        // which belongs to the normal install.
        if (!ProfilePaths.Active)
        {
            ProtocolRegistration.Register();
        }
        var background = argv.Any(a => string.Equals(a, "--background", StringComparison.OrdinalIgnoreCase));
        ReadDeepLink(argv);

        AppPrefs.Load();
        AppPrefs.ApplyToShell(ShellState.Current);

        if (!InitializeCore())
            Nexus.Core.Diagnostics.DiagLog.Error("startup", "nx_init failed; the engine is not running");
        var window = new MainWindow();
        Nexus.Core.Diagnostics.DiagLog.Info("startup", background ? "window created (hidden: --background)" : "window created");
        _window = window;
        SingleInstance.Listen(payload =>
            window.DispatcherQueue.TryEnqueue(() => window.HandleRemote(payload)));
        // --background still creates the HWND so the tray icon exists, then hides it.
        window.Activate();
        if (background)
        {
#if HAS_UNO
            WindowExtensions.Hide(window);
#else
            window.AppWindow.Hide();
#endif
        }
    }

    public static bool InitializeCore()
    {
        try
        {
            return Core.Init(JsonSerializer.Serialize(new
            {
                online = true,
                // An explicit workspace is safest. During development, accepting
                // the current directory only when it is a git worktree keeps an
                // installed Nexus.exe from accidentally harvesting its own files.
                workspace_root = CaptureWorkspaceRoot(),
                // The core logs to logs\core-*.log here and uploads reports\pending.
                diagnostics_dir = AppDiagnostics.BaseDir,
                app_version = AppDiagnostics.Version,
            }));
        }
        catch (Exception ex) when (ex is DllNotFoundException or EntryPointNotFoundException or BadImageFormatException)
        {
            Nexus.Core.Diagnostics.DiagLog.Error("startup", "could not load nexuscore.dll", ex);
            throw;
        }
    }

    public static void ClearPendingAuth() => PendingAuthUrl = null;

    public static void ClearPending()
    {
        PendingSessionId = null;
        PendingTeleportId = null;
        PendingAuthUrl = null;
    }

    internal static string CaptureWorkspaceRoot()
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
