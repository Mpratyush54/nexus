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
    public static void Main(string[] args)
    {
        var host = UnoPlatformHostBuilder.Create()
            .App(() => new App())
            .UseMacOS()
            .UseWin32()
            .UseX11()
            .Build();
        host.Run();
    }
}
