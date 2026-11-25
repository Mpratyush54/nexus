using System.Runtime.InteropServices;

namespace Nexus.Platform;

/// <summary>
/// The process's real command line. Uno's macOS .app launcher starts the
/// runtime without forwarding argv, so Main's args and
/// Environment.GetCommandLineArgs() are empty there; the C runtime still has
/// them (_NSGetArgc / _NSGetArgv in libSystem). Elsewhere this is
/// Environment.GetCommandLineArgs().
/// </summary>
internal static partial class LaunchArgs
{
    private static string[]? s_all;

    /// <summary>Like Environment.GetCommandLineArgs(): the executable first, then the arguments.</summary>
    public static string[] All() => s_all ??= Read();

    /// <summary>The arguments without the executable.</summary>
    public static string[] Arguments() => All().Skip(1).ToArray();

    private static string[] Read()
    {
        var managed = Environment.GetCommandLineArgs();
        if (!OperatingSystem.IsMacOS()) return managed;
        try
        {
            var native = ReadNative();
            // Prefer whichever saw more: a future launcher may forward them.
            return native.Length > managed.Length ? native : managed;
        }
        catch (Exception ex) when (ex is DllNotFoundException or EntryPointNotFoundException)
        {
            return managed;
        }
    }

    private static unsafe string[] ReadNative()
    {
        var argc = *(int*)_NSGetArgc();
        var argv = *(IntPtr**)_NSGetArgv();
        if (argc <= 0 || argv == null) return Array.Empty<string>();
        var result = new string[argc];
        for (var i = 0; i < argc; i++) result[i] = Marshal.PtrToStringUTF8(argv[i]) ?? "";
        return result;
    }

    [LibraryImport("/usr/lib/libSystem.B.dylib")]
    private static partial IntPtr _NSGetArgc();

    [LibraryImport("/usr/lib/libSystem.B.dylib")]
    private static partial IntPtr _NSGetArgv();
}
