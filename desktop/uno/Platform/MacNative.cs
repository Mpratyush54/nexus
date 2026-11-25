using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;

namespace Nexus.Platform;

/// <summary>
/// P/Invoke surface of libnexusmac.dylib (desktop/uno/macos/native/NexusMac.m),
/// the small Objective-C helper that owns the menu bar status item, the app
/// menu, Dock reopen and nexus:// URL events. Every entry point degrades to
/// "not available" when the library is missing (a developer build without it),
/// so the app still runs, just without those features.
/// </summary>
internal static partial class MacNative
{
    private const string Lib = "nexusmac";

    /// <summary>Menu and system commands; keep in sync with NexusMac.m.</summary>
    public enum Command
    {
        Open = 1,
        TogglePause = 2,
        Report = 3,
        Quit = 4,
        Settings = 5,
        Reopen = 6,
        TerminateRequested = 7,
        CloseWindow = 8,
        NewChat = 9,
        Search = 10,
        SwitchProject = 11,
        Undo = 12,
        Redo = 13,
        Cut = 14,
        Copy = 15,
        Paste = 16,
        SelectAll = 17,
    }

    private static Action<Command>? s_onCommand;
    private static Action<string>? s_onUrl;
    private static bool s_missing;

    public static bool Available => OperatingSystem.IsMacOS() && !s_missing;

    /// <summary>Registers the nexus:// handler. Call before the Uno host starts.</summary>
    public static unsafe bool ListenUrls(Action<string> onUrl)
    {
        if (!Available) return false;
        s_onUrl = onUrl;
        return Try(() => nx_mac_listen_urls(&OnUrl));
    }

    /// <summary>Creates the status item and app menu and hooks the app delegate (UI thread).</summary>
    public static unsafe bool Install(Action<Command> onCommand)
    {
        if (!Available) return false;
        s_onCommand = onCommand;
        var ok = false;
        Try(() => ok = nx_mac_install(&OnCommand) != 0);
        return ok;
    }

    public static void SetCapturePaused(bool paused) => Try(() => nx_mac_set_capture_state(paused ? 1 : 0));

    public static bool ShowWindows() => Try(nx_mac_show_windows);

    public static bool HideWindows() => Try(nx_mac_hide_windows);

    private static bool Try(Action call)
    {
        if (!Available) return false;
        try
        {
            call();
            return true;
        }
        catch (Exception ex) when (ex is DllNotFoundException or EntryPointNotFoundException or BadImageFormatException)
        {
            s_missing = true;
            Nexus.Core.Diagnostics.DiagLog.Warn("mac", "libnexusmac is not available; no menu bar item or URL events", ex);
            Console.Error.WriteLine("[nexus-mac] libnexusmac unavailable: " + ex.Message);
            return false;
        }
    }

    [UnmanagedCallersOnly(CallConvs = new[] { typeof(CallConvCdecl) })]
    private static void OnCommand(int command)
    {
        try { s_onCommand?.Invoke((Command)command); }
        catch (Exception ex) { Nexus.Core.Diagnostics.DiagLog.Error("mac", "menu command failed", ex); }
    }

    [UnmanagedCallersOnly(CallConvs = new[] { typeof(CallConvCdecl) })]
    private static void OnUrl(IntPtr utf8)
    {
        try
        {
            var url = Marshal.PtrToStringUTF8(utf8);
            if (!string.IsNullOrEmpty(url)) s_onUrl?.Invoke(url);
        }
        catch (Exception ex) { Nexus.Core.Diagnostics.DiagLog.Error("mac", "url event failed", ex); }
    }

    [LibraryImport(Lib)]
    private static unsafe partial void nx_mac_listen_urls(delegate* unmanaged[Cdecl]<IntPtr, void> callback);

    [LibraryImport(Lib)]
    private static unsafe partial byte nx_mac_install(delegate* unmanaged[Cdecl]<int, void> callback);

    [LibraryImport(Lib)]
    private static partial void nx_mac_set_capture_state(int paused);

    [LibraryImport(Lib)]
    private static partial void nx_mac_show_windows();

    [LibraryImport(Lib)]
    private static partial void nx_mac_hide_windows();
}
