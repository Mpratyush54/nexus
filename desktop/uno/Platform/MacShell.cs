using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Nexus.Core.Diagnostics;

namespace Nexus.Platform;

/// <summary>
/// The macOS shell around MainWindow: the menu bar status item (in place of
/// the Windows tray), the app menu, Dock reopen and nexus:// links. Commands
/// map onto the same MainWindow commands the Windows tray uses, and links go
/// through <see cref="MainWindow.HandleRemote"/>, the handler a forwarded
/// Windows launch uses.
/// </summary>
internal static class MacShell
{
    private static MainWindow? s_window;
    private static readonly List<string> s_early = new();
    private static readonly object s_gate = new();

    /// <summary>True once the status item exists: closing the window then hides it.</summary>
    public static bool HasStatusItem { get; private set; }

    /// <summary>Program.Main, before the host exists: links that launch the app arrive early.</summary>
    public static void ListenForLinks() => MacNative.ListenUrls(DeliverUrl);

    public static void Attach(MainWindow window)
    {
        if (!OperatingSystem.IsMacOS()) return;
        lock (s_gate) s_window = window;
        HasStatusItem = MacNative.Install(command => window.DispatcherQueue.TryEnqueue(() => Run(window, command)));
        Log(HasStatusItem ? "menu bar item and app menu installed" : "menu bar item unavailable; closing the window quits");
        if (HasStatusItem)
        {
            MacNative.SetCapturePaused(ShellState.Current.CapturePaused);
            ShellState.Changed += (_, _) => window.DispatcherQueue.TryEnqueue(() => MacNative.SetCapturePaused(ShellState.Current.CapturePaused));
        }
        string[] early;
        lock (s_gate)
        {
            early = s_early.ToArray();
            s_early.Clear();
        }
        if (early.Length > 0)
        {
            window.DispatcherQueue.TryEnqueue(DispatcherQueuePriority.Low, () => window.HandleRemote(string.Join("\n", early)));
        }
    }

    private static void Run(MainWindow window, MacNative.Command command)
    {
        Log($"command: {command}");
        switch (command)
        {
            case MacNative.Command.Open:
            case MacNative.Command.Reopen:
                if (command == MacNative.Command.Reopen) Log("dock reopen");
                window.TrayOpenCommand.Execute(null);
                break;
            case MacNative.Command.TogglePause:
                window.TrayPauseCommand.Execute(null);
                break;
            case MacNative.Command.Report:
                window.TrayReportCommand.Execute(null);
                break;
            case MacNative.Command.Settings:
                window.TrayOpenCommand.Execute(null);
                window.ShowSettings();
                break;
            case MacNative.Command.CloseWindow:
                if (HasStatusItem) WindowExtensions.Hide(window);
                else window.TrayQuitCommand.Execute(null);
                break;
            case MacNative.Command.Quit:
            case MacNative.Command.TerminateRequested:
                Log(command == MacNative.Command.Quit ? "quit from the menu" : "quit from the system");
                window.TrayQuitCommand.Execute(null);
                break;
            // File: the same code as the Ctrl+N / Ctrl+K / Ctrl+P accelerators.
            case MacNative.Command.NewChat:
                window.StartNewChat();
                break;
            case MacNative.Command.Search:
                _ = window.ShowSearchAsync();
                break;
            case MacNative.Command.SwitchProject:
                window.OpenProjectSwitcher();
                break;
            default:
                Edit(window, command);
                break;
        }
    }

    /// <summary>
    /// Edit menu (and its Command shortcuts, which the menu receives first):
    /// acts on the focused text control through its own API, so it works
    /// wherever focus is and never depends on how a key event is routed.
    /// A PasswordBox (a TextBox subclass on Uno) never copies or cuts.
    /// </summary>
    private static void Edit(MainWindow window, MacNative.Command command)
    {
        if (window.Content?.XamlRoot is not { } root) return;
        var focused = FocusManager.GetFocusedElement(root);
        var done = true;
        switch (focused)
        {
            case PasswordBox password:
                if (command == MacNative.Command.Paste) password.PasteFromClipboard();
                else if (command == MacNative.Command.SelectAll) password.SelectAll();
                else done = false;
                break;
            case TextBox box:
                switch (command)
                {
                    case MacNative.Command.Undo: box.Undo(); break;
                    case MacNative.Command.Redo: box.Redo(); break;
                    case MacNative.Command.Cut: box.CutSelectionToClipboard(); break;
                    case MacNative.Command.Copy: box.CopySelectionToClipboard(); break;
                    case MacNative.Command.Paste: _ = PasteAsync(box); break;
                    case MacNative.Command.SelectAll: box.SelectAll(); break;
                    default: done = false; break;
                }
                break;
            case TextBlock text when text.IsTextSelectionEnabled:
                if (command == MacNative.Command.Copy) text.CopySelectionToClipboard();
                else if (command == MacNative.Command.SelectAll) text.SelectAll();
                else done = false;
                break;
            case RichTextBlock rich when rich.IsTextSelectionEnabled:
                if (command == MacNative.Command.Copy) rich.CopySelectionToClipboard();
                else if (command == MacNative.Command.SelectAll) rich.SelectAll();
                else done = false;
                break;
            default:
                done = false;
                break;
        }
        Log($"edit {command}: {(done ? focused?.GetType().Name : "nothing focused that can do it")}");
    }

    /// <summary>A nexus:// URL from LaunchServices (kAEGetURL or application:openURLs:).</summary>
    public static void DeliverUrl(string url)
    {
        if (!url.StartsWith("nexus://", StringComparison.OrdinalIgnoreCase))
        {
            Log("url event ignored (not nexus://)");
            return;
        }
        // Only the route: links can carry sign-in codes.
        var route = url["nexus://".Length..].Split('/', '?', '#')[0];
        Log($"url event: nexus://{route}");
        MainWindow? window;
        lock (s_gate)
        {
            window = s_window;
            if (window is null)
            {
                s_early.Add(url);
                return;
            }
        }
        window.DispatcherQueue.TryEnqueue(() => window.HandleRemote(url));
    }

    private static async Task PasteAsync(TextBox box)
    {
        try
        {
            if (Shortcuts.PasteHandler(box) is { } custom && await custom()) return;
        }
        catch (Exception ex)
        {
            DiagLog.Warn("mac", "custom paste failed; pasting text", ex);
        }
        box.PasteFromClipboard();
    }

    internal static void Log(string message)
    {
        DiagLog.Info("mac", message);
        Console.Error.WriteLine("[nexus-mac] " + message);
    }
}
