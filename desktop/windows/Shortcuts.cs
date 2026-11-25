using Microsoft.UI.Xaml;
using Windows.System;

namespace Nexus;

/// <summary>
/// The primary shortcut modifier: Ctrl on Windows, Command on macOS (the Uno
/// head reports Command as <see cref="VirtualKeyModifiers.Windows"/>). The
/// Windows head always gets Ctrl, so nothing changes there.
/// </summary>
internal static class Shortcuts
{
    public static bool UseCommand =>
#if HAS_UNO
        OperatingSystem.IsMacOS();
#else
        false;
#endif

    public static VirtualKeyModifiers Primary => UseCommand ? VirtualKeyModifiers.Windows : VirtualKeyModifiers.Control;

    /// <summary>"Ctrl+P" on Windows, "⌘P" on macOS.</summary>
    public static string Label(string key) => UseCommand ? "⌘" + key : "Ctrl+" + key;

    /// <summary>On macOS, moves accelerators declared with Ctrl (XAML) to Command.</summary>
    public static void UseCommandKey(UIElement element)
    {
        if (!UseCommand) return;
        foreach (var accelerator in element.KeyboardAccelerators)
        {
            if ((accelerator.Modifiers & VirtualKeyModifiers.Control) != 0)
            {
                accelerator.Modifiers = (accelerator.Modifiers & ~VirtualKeyModifiers.Control) | VirtualKeyModifiers.Windows;
            }
        }
    }

    /// <summary>Whether the primary modifier is held (for KeyDown handlers that do not use accelerators).</summary>
    public static bool IsPrimaryDown()
    {
        static bool Down(VirtualKey key) =>
            Microsoft.UI.Input.InputKeyboardSource.GetKeyStateForCurrentThread(key).HasFlag(Windows.UI.Core.CoreVirtualKeyStates.Down);
        return UseCommand ? Down(VirtualKey.LeftWindows) || Down(VirtualKey.RightWindows) || Down(VirtualKey.Control) : Down(VirtualKey.Control);
    }
}
