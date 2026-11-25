using Microsoft.UI.Dispatching;
using Nexus.Core;
using Nexus.Core.Diagnostics;
using Nexus.Pages;

namespace Nexus;

/// <summary>
/// Quick ask: a global hotkey (Alt+Space on Windows, Option+Space on macOS by
/// default; Settings › General changes or turns it off) opens a small
/// floating prompt over any app (<see cref="QuickAskWindow"/>). It starts a
/// chat with the chosen agent and project and either opens it or keeps it
/// working in the background with a notification when it finishes.
///
/// Windows registers the key with RegisterHotKey on the main window; macOS
/// with Carbon RegisterEventHotKey in libnexusmac. Both are released when
/// Nexus quits (and by the OS if it dies). A key another app already owns is
/// reported in Settings and once as a notice; Nexus keeps working without it.
/// </summary>
internal static partial class QuickAsk
{
    private static MainWindow? s_main;
    private static QuickAskWindow? s_window;
    private static string s_registered = "";
    private static bool s_warned;

    /// <summary>"" when registered (or off); otherwise why it is not.</summary>
    public static string Problem { get; private set; } = "";

    public static bool Registered => s_registered.Length > 0;

    public static void Init(MainWindow main)
    {
        s_main = main;
        ShellPrefs.Changed += () => main.DispatcherQueue.TryEnqueue(Apply);
        // After the window has a handle and is up.
        main.DispatcherQueue.TryEnqueue(DispatcherQueuePriority.Low, Apply);
    }

    /// <summary>Registers (or re-registers, or releases) the hotkey from the current settings.</summary>
    public static void Apply()
    {
        if (s_main is null) return;
        var wanted = ShellPrefs.Current.QuickAskEnabled && Environment.GetEnvironmentVariable("NEXUS_NO_HOTKEY") != "1" ? ShellPrefs.QuickAskKeys : "";
        if (wanted == s_registered && Problem.Length == 0) return;
        Release();
        Problem = "";
        if (wanted.Length == 0) return;
        if (HotkeyText.Parse(wanted) is not { } keys)
        {
            Problem = $"“{wanted}” isn't a shortcut Nexus can use.";
            return;
        }
        var error = RegisterPlatform(s_main, keys);
        if (error.Length == 0)
        {
            s_registered = wanted;
            DiagLog.Info("quickask", "hotkey registered: " + wanted);
            return;
        }
        Problem = error;
        DiagLog.Warn("quickask", $"hotkey {wanted} not registered: {error}");
        if (!s_warned)
        {
            s_warned = true;
            Toast.Show($"{keys.Format(OperatingSystem.IsMacOS())} is already used by another app, so quick ask is off. Pick another shortcut in Settings.",
                onUndo: () => s_main?.ShowSettings("general"), actionLabel: "Settings");
        }
    }

    public static void Release()
    {
        if (s_registered.Length == 0) return;
        try { UnregisterPlatform(); }
        catch (Exception ex) { DiagLog.Warn("quickask", "hotkey release failed", ex); }
        DiagLog.Info("quickask", "hotkey released: " + s_registered);
        s_registered = "";
    }

    public static void Shutdown()
    {
        Release();
        try { s_window?.CloseForGood(); } catch { /* quitting anyway */ }
        s_window = null;
    }

    /// <summary>The hotkey (or the palette, tray, menu bar) asks for the prompt.</summary>
    public static void Open(MainWindow main)
    {
        s_main ??= main;
        try
        {
            s_window ??= new QuickAskWindow(main);
            s_window.ShowPrompt();
        }
        catch (Exception ex)
        {
            // A platform without secondary windows: the palette-sized prompt
            // inside the main window does the same job.
            DiagLog.Warn("quickask", "floating window unavailable; using the main window", ex);
            s_window = null;
            main.ShowQuickAskInWindow();
        }
    }

    private static void OnHotkey()
    {
        if (s_main is null) return;
        s_main.DispatcherQueue.TryEnqueue(() =>
        {
            // Pressed again while the prompt is up: put it away.
            if (s_window is { IsShowing: true } shown) { shown.HidePrompt(); return; }
            Open(s_main);
        });
    }
}
