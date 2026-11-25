using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;

namespace Nexus;

/// <summary>
/// Custom entry point (DISABLE_XAML_GENERATED_MAIN): the installer's hooks
/// must run before anything else, including the single-instance check.
/// Velopack starts Nexus.exe with --veloapp-* arguments during install,
/// update and uninstall; <see cref="InstallerHooks.Run"/> handles those and
/// exits. A normal launch falls through to the WinUI application.
/// </summary>
public static class Program
{
    [STAThread]
    private static int Main(string[] args)
    {
        InstallerHooks.Run(args);
        WinRT.ComWrappersSupport.InitializeComWrappers();
        Application.Start(_ =>
        {
            var context = new DispatcherQueueSynchronizationContext(DispatcherQueue.GetForCurrentThread());
            SynchronizationContext.SetSynchronizationContext(context);
            new App();
        });
        return 0;
    }
}
