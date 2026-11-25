using System.Runtime.InteropServices;
using System.Runtime.Versioning;

namespace Nexus;

/// <summary>
/// The uninstall question as a native task dialog: Nexus icon, two command
/// links and a live countdown. It runs inside Velopack's 30 second
/// uninstall hook, so after 25 seconds it answers "keep" by itself.
/// </summary>
[SupportedOSPlatform("windows")]
internal static class UninstallPrompt
{
    private const int KeepId = 1001, RemoveId = 1002;
    private const int TimeoutMs = 25_000;
    private const int TDF_USE_HICON_MAIN = 0x2, TDF_USE_COMMAND_LINKS = 0x10, TDF_CALLBACK_TIMER = 0x800, TDF_SIZE_TO_CONTENT = 0x1000000;
    private const int TDN_TIMER = 4;
    private const uint WM_USER = 0x400, TDM_CLICK_BUTTON = WM_USER + 102, TDM_SET_ELEMENT_TEXT = WM_USER + 108;
    private const int TDE_FOOTER = 2;

    [StructLayout(LayoutKind.Sequential, Pack = 1, CharSet = CharSet.Unicode)]
    private struct TaskDialogButton
    {
        public int Id;
        public IntPtr Text;
    }

    [UnmanagedFunctionPointer(CallingConvention.StdCall)]
    private delegate int Callback(IntPtr hwnd, int notification, IntPtr wParam, IntPtr lParam, IntPtr data);

    [StructLayout(LayoutKind.Sequential, Pack = 1, CharSet = CharSet.Unicode)]
    private struct TaskDialogConfig
    {
        public uint cbSize;
        public IntPtr hwndParent;
        public IntPtr hInstance;
        public int dwFlags;
        public int dwCommonButtons;
        public string pszWindowTitle;
        public IntPtr hMainIcon;
        public string pszMainInstruction;
        public string pszContent;
        public uint cButtons;
        public IntPtr pButtons;
        public int nDefaultButton;
        public uint cRadioButtons;
        public IntPtr pRadioButtons;
        public int nDefaultRadioButton;
        public string? pszVerificationText;
        public string? pszExpandedInformation;
        public string? pszExpandedControlText;
        public string? pszCollapsedControlText;
        public IntPtr hFooterIcon;
        public string pszFooter;
        public Callback pfCallback;
        public IntPtr lpCallbackData;
        public uint cxWidth;
    }

    [DllImport("comctl32.dll", CharSet = CharSet.Unicode, ExactSpelling = true)]
    private static extern int TaskDialogIndirect(ref TaskDialogConfig config, out int button, out int radio, out bool verification);

    [DllImport("shell32.dll", CharSet = CharSet.Unicode)]
    private static extern uint ExtractIconEx(string file, int index, out IntPtr large, IntPtr small, uint count);

    [DllImport("user32.dll")] private static extern bool DestroyIcon(IntPtr icon);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern IntPtr SendMessage(IntPtr hwnd, uint msg, IntPtr wParam, string lParam);
    [DllImport("user32.dll")] private static extern IntPtr SendMessage(IntPtr hwnd, uint msg, IntPtr wParam, IntPtr lParam);

    /// <summary>True when the user chose to remove local data. Null when the dialog could not be shown.</summary>
    public static bool? AskRemoveData(IEnumerable<string> locations)
    {
        var keep = Marshal.StringToHGlobalUni("Keep my data\nReinstall later and pick up where you left off.");
        var remove = Marshal.StringToHGlobalUni("Remove my data from this PC\nSigns this PC out and deletes local settings, caches and capture choices.");
        var buttons = new[] { new TaskDialogButton { Id = KeepId, Text = keep }, new TaskDialogButton { Id = RemoveId, Text = remove } };
        var size = Marshal.SizeOf<TaskDialogButton>();
        var array = Marshal.AllocHGlobal(size * buttons.Length);
        IntPtr icon = IntPtr.Zero;
        var lastSecond = -1;
        try
        {
            for (var i = 0; i < buttons.Length; i++) Marshal.StructureToPtr(buttons[i], array + i * size, false);
            ExtractIconEx(Environment.ProcessPath ?? "", 0, out icon, IntPtr.Zero, 1);
            Callback callback = (hwnd, note, wParam, _, _) =>
            {
                if (note != TDN_TIMER) return 0;
                var left = Math.Max(0, (TimeoutMs - (int)wParam) / 1000);
                if (left != lastSecond)
                {
                    lastSecond = left;
                    SendMessage(hwnd, TDM_SET_ELEMENT_TEXT, (IntPtr)TDE_FOOTER, $"Keeping your data in {left} s if you don't choose.");
                }
                if ((int)wParam >= TimeoutMs) SendMessage(hwnd, TDM_CLICK_BUTTON, (IntPtr)KeepId, IntPtr.Zero);
                return 0;
            };
            var config = new TaskDialogConfig
            {
                cbSize = (uint)Marshal.SizeOf<TaskDialogConfig>(),
                dwFlags = TDF_USE_COMMAND_LINKS | TDF_CALLBACK_TIMER | TDF_SIZE_TO_CONTENT | (icon != IntPtr.Zero ? TDF_USE_HICON_MAIN : 0),
                pszWindowTitle = "Uninstall Nexus",
                hMainIcon = icon,
                pszMainInstruction = "Keep your Nexus data on this PC?",
                pszContent = "Your chats and memories stay in your Nexus account either way. Local data lives in:\n" + string.Join("\n", locations),
                cButtons = (uint)buttons.Length,
                pButtons = array,
                nDefaultButton = KeepId,
                pszFooter = $"Keeping your data in {TimeoutMs / 1000} s if you don't choose.",
                pfCallback = callback,
            };
            var hr = TaskDialogIndirect(ref config, out var chosen, out _, out _);
            GC.KeepAlive(callback);
            if (hr != 0) return null;
            return chosen == RemoveId;
        }
        catch (Exception ex) when (ex is EntryPointNotFoundException or DllNotFoundException or MarshalDirectiveException)
        {
            return null;
        }
        finally
        {
            Marshal.FreeHGlobal(array);
            Marshal.FreeHGlobal(keep);
            Marshal.FreeHGlobal(remove);
            if (icon != IntPtr.Zero) DestroyIcon(icon);
        }
    }
}
