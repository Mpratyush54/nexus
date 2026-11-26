using System.Runtime.InteropServices;
using Nexus.Core;

namespace Nexus;

// The global hotkey, per platform.
internal static partial class QuickAsk
{
#if HAS_UNO
    private static string RegisterPlatform(MainWindow main, HotkeyText keys)
    {
        if (!OperatingSystem.IsMacOS()) return "Global shortcuts aren't available on this system.";
        if (MacKeyCode(keys.Key) is not { } code) return $"{keys.Key} can't be used for the quick-ask shortcut.";
        uint mods = 0;
        if (keys.Primary) mods |= 0x0100; // cmdKey
        if (keys.Shift) mods |= 0x0200;   // shiftKey
        if (keys.Alt) mods |= 0x0800;     // optionKey
        var status = Nexus.Platform.MacNative.RegisterHotkey(code, mods, OnHotkey);
        return status switch
        {
            0 => "",
            -9878 => "Another app already uses that shortcut.", // eventHotKeyExistsErr
            int.MinValue => "This build of Nexus can't register global shortcuts.",
            _ => $"macOS didn't accept the shortcut (error {status})."
        };
    }

    private static void UnregisterPlatform() => Nexus.Platform.MacNative.UnregisterHotkey();

    // Carbon virtual key codes (HIToolbox kVK_*).
    private static uint? MacKeyCode(string key) => key.ToUpperInvariant() switch
    {
        "SPACE" => 49, "A" => 0, "S" => 1, "D" => 2, "F" => 3, "H" => 4, "G" => 5, "Z" => 6, "X" => 7, "C" => 8, "V" => 9,
        "B" => 11, "Q" => 12, "W" => 13, "E" => 14, "R" => 15, "Y" => 16, "T" => 17, "O" => 31, "U" => 32, "I" => 34,
        "P" => 35, "L" => 37, "J" => 38, "K" => 40, "N" => 45, "M" => 46,
        "1" => 18, "2" => 19, "3" => 20, "4" => 21, "5" => 23, "6" => 22, "7" => 26, "8" => 28, "9" => 25, "0" => 29,
        _ => null
    };
#else
    private const int HotkeyId = 0x4E51; // "NQ"
    private const uint WmHotkey = 0x0312;
    private const uint ModAlt = 0x1, ModControl = 0x2, ModShift = 0x4, ModNoRepeat = 0x4000;
    private const int ErrorHotkeyAlreadyRegistered = 1409;
    private static IntPtr s_hwnd;
    private static SubclassProc? s_proc; // kept alive while subclassed

    private delegate IntPtr SubclassProc(IntPtr hWnd, uint msg, IntPtr wParam, IntPtr lParam, UIntPtr id, UIntPtr data);

    [DllImport("user32.dll", SetLastError = true)]
    private static extern bool RegisterHotKey(IntPtr hWnd, int id, uint modifiers, uint vk);

    [DllImport("user32.dll", SetLastError = true)]
    private static extern bool UnregisterHotKey(IntPtr hWnd, int id);

    [DllImport("comctl32.dll", SetLastError = true)]
    private static extern bool SetWindowSubclass(IntPtr hWnd, SubclassProc proc, UIntPtr id, UIntPtr data);

    [DllImport("comctl32.dll", SetLastError = true)]
    private static extern bool RemoveWindowSubclass(IntPtr hWnd, SubclassProc proc, UIntPtr id);

    [DllImport("comctl32.dll")]
    private static extern IntPtr DefSubclassProc(IntPtr hWnd, uint msg, IntPtr wParam, IntPtr lParam);

    private static string RegisterPlatform(MainWindow main, HotkeyText keys)
    {
        if (WindowsKey(keys.Key) is not { } vk) return $"{keys.Key} can't be used for the quick-ask shortcut.";
        var hwnd = WinRT.Interop.WindowNative.GetWindowHandle(main);
        if (hwnd == IntPtr.Zero) return "The Nexus window isn't ready yet.";
        if (s_proc is null || s_hwnd != hwnd)
        {
            s_proc = Proc;
            if (!SetWindowSubclass(hwnd, s_proc, (UIntPtr)HotkeyId, UIntPtr.Zero)) return "Windows didn't let Nexus listen for the shortcut.";
            s_hwnd = hwnd;
        }
        uint mods = ModNoRepeat;
        if (keys.Primary) mods |= ModControl;
        if (keys.Shift) mods |= ModShift;
        if (keys.Alt) mods |= ModAlt;
        if (RegisterHotKey(hwnd, HotkeyId, mods, vk)) return "";
        var error = Marshal.GetLastWin32Error();
        return error == ErrorHotkeyAlreadyRegistered ? "Another app already uses that shortcut." : $"Windows didn't accept the shortcut (error {error}).";
    }

    private static void UnregisterPlatform()
    {
        if (s_hwnd != IntPtr.Zero) UnregisterHotKey(s_hwnd, HotkeyId);
    }

    private static IntPtr Proc(IntPtr hWnd, uint msg, IntPtr wParam, IntPtr lParam, UIntPtr id, UIntPtr data)
    {
        if (msg == WmHotkey && wParam.ToInt64() == HotkeyId)
        {
            OnHotkey();
            return IntPtr.Zero;
        }
        return DefSubclassProc(hWnd, msg, wParam, lParam);
    }

    private static uint? WindowsKey(string key)
    {
        var k = key.ToUpperInvariant();
        if (k == "SPACE") return 0x20;
        if (k.Length == 1 && (char.IsAsciiLetterUpper(k[0]) || char.IsAsciiDigit(k[0]))) return k[0];
        if (k.Length >= 2 && k[0] == 'F' && int.TryParse(k[1..], out var f) && f is >= 1 and <= 12) return (uint)(0x70 + f - 1);
        return null;
    }
#endif
}
