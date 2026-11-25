using System.Runtime.InteropServices;
using System.Runtime.Versioning;
using System.Security;
using System.Text;
using Microsoft.Win32;

namespace Nexus.Platform;

[SupportedOSPlatform("windows")]
public sealed class WindowsDesktopIntegration : IDesktopIntegration
{
    private const string RunKey = @"Software\Microsoft\Windows\CurrentVersion\Run";
    private const string RunValue = "Nexus";
    private const string Scheme = "nexus";
    private const string ShortcutName = "Nexus.lnk";

    private static string Executable => Environment.ProcessPath ?? "";

    // ---- Start at login (HKCU Run, launched hidden to the tray)

    public bool StartsAtLogin
    {
        get
        {
            using var key = Registry.CurrentUser.OpenSubKey(RunKey, false);
            return key?.GetValue(RunValue) is string;
        }
    }

    public void SetStartsAtLogin(bool enabled)
    {
        using var key = Registry.CurrentUser.CreateSubKey(RunKey, true)
            ?? throw new InvalidOperationException("The Windows startup list (HKCU Run) is unavailable.");
        if (!enabled)
        {
            key.DeleteValue(RunValue, false);
            return;
        }
        key.SetValue(RunValue, $"\"{Executable}\" --background");
    }

    /// <summary>The Run entry's executable, or "" when there is none.</summary>
    public static string StartupTarget()
    {
        using var key = Registry.CurrentUser.OpenSubKey(RunKey, false);
        return key?.GetValue(RunValue) is string command ? CommandExecutable(command) : "";
    }

    // ---- Desktop shortcut

    public bool SupportsDesktopShortcut => true;

    public static string DesktopShortcutPath =>
        Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.DesktopDirectory), ShortcutName);

    public bool HasDesktopShortcut => File.Exists(DesktopShortcutPath);

    public void SetDesktopShortcut(bool enabled)
    {
        if (!enabled)
        {
            if (File.Exists(DesktopShortcutPath)) File.Delete(DesktopShortcutPath);
            return;
        }
        ShellLinks.Write(DesktopShortcutPath, Executable, "Nexus: your coding agents' memory");
    }

    // ---- nexus:// links

    public void RegisterUrlScheme()
    {
        if (string.IsNullOrWhiteSpace(Executable)) return;
        try
        {
            using var root = Registry.CurrentUser.CreateSubKey($@"Software\Classes\{Scheme}");
            if (root is null) return;
            root.SetValue(null, "URL:Nexus Protocol");
            root.SetValue("URL Protocol", "");
            using (var icon = root.CreateSubKey("DefaultIcon")) icon?.SetValue(null, $"\"{Executable}\",0");
            using var command = root.CreateSubKey(@"shell\open\command");
            command?.SetValue(null, $"\"{Executable}\" \"%1\"");
        }
        catch (Exception ex) when (ex is UnauthorizedAccessException or SecurityException or IOException)
        {
            // Links stay with whichever copy registered last; not fatal.
        }
    }

    public void UnregisterUrlScheme(string installRoot)
    {
        try
        {
            string command;
            using (var key = Registry.CurrentUser.OpenSubKey($@"Software\Classes\{Scheme}\shell\open\command", false))
                command = key?.GetValue(null) as string ?? "";
            if (command.Length == 0 || !IsUnder(CommandExecutable(command), installRoot)) return;
            Registry.CurrentUser.DeleteSubKeyTree($@"Software\Classes\{Scheme}", false);
        }
        catch (Exception ex) when (ex is UnauthorizedAccessException or SecurityException or IOException)
        {
        }
    }

    // ---- helpers

    internal static string CommandExecutable(string command)
    {
        command = command.Trim();
        if (command.StartsWith('"'))
        {
            var end = command.IndexOf('"', 1);
            return end > 1 ? command[1..end] : command.Trim('"');
        }
        var space = command.IndexOf(' ');
        return space > 0 ? command[..space] : command;
    }

    internal static bool IsUnder(string path, string root)
    {
        if (path.Length == 0 || root.Length == 0) return false;
        try
        {
            var full = Path.GetFullPath(path);
            var dir = Path.GetFullPath(root).TrimEnd(Path.DirectorySeparatorChar) + Path.DirectorySeparatorChar;
            return full.StartsWith(dir, StringComparison.OrdinalIgnoreCase);
        }
        catch (Exception ex) when (ex is ArgumentException or NotSupportedException or PathTooLongException)
        {
            return false;
        }
    }
}

/// <summary>Minimal IShellLinkW wrapper: write a .lnk and read its target.</summary>
[SupportedOSPlatform("windows")]
internal static class ShellLinks
{
    public static void Write(string linkPath, string target, string description)
    {
        var link = (IShellLinkW)new ShellLinkCoClass();
        try
        {
            link.SetPath(target);
            link.SetWorkingDirectory(Path.GetDirectoryName(target) ?? "");
            link.SetDescription(description);
            link.SetIconLocation(target, 0);
            ((IPersistFile)link).Save(linkPath, true);
        }
        finally
        {
            Marshal.FinalReleaseComObject(link);
        }
    }

    public static string Target(string linkPath)
    {
        if (!File.Exists(linkPath)) return "";
        var link = (IShellLinkW)new ShellLinkCoClass();
        try
        {
            ((IPersistFile)link).Load(linkPath, 0);
            var buffer = new StringBuilder(1024);
            link.GetPath(buffer, buffer.Capacity, IntPtr.Zero, 0);
            return buffer.ToString();
        }
        catch (COMException)
        {
            return "";
        }
        finally
        {
            Marshal.FinalReleaseComObject(link);
        }
    }

    [ComImport, Guid("00021401-0000-0000-C000-000000000046")]
    private class ShellLinkCoClass
    {
    }

    [ComImport, InterfaceType(ComInterfaceType.InterfaceIsIUnknown), Guid("000214F9-0000-0000-C000-000000000046")]
    private interface IShellLinkW
    {
        void GetPath([Out, MarshalAs(UnmanagedType.LPWStr)] StringBuilder file, int maxPath, IntPtr findData, int flags);
        void GetIDList(out IntPtr idList);
        void SetIDList(IntPtr idList);
        void GetDescription([Out, MarshalAs(UnmanagedType.LPWStr)] StringBuilder name, int maxName);
        void SetDescription([MarshalAs(UnmanagedType.LPWStr)] string name);
        void GetWorkingDirectory([Out, MarshalAs(UnmanagedType.LPWStr)] StringBuilder dir, int maxPath);
        void SetWorkingDirectory([MarshalAs(UnmanagedType.LPWStr)] string dir);
        void GetArguments([Out, MarshalAs(UnmanagedType.LPWStr)] StringBuilder args, int maxPath);
        void SetArguments([MarshalAs(UnmanagedType.LPWStr)] string args);
        void GetHotkey(out short hotkey);
        void SetHotkey(short hotkey);
        void GetShowCmd(out int showCmd);
        void SetShowCmd(int showCmd);
        void GetIconLocation([Out, MarshalAs(UnmanagedType.LPWStr)] StringBuilder iconPath, int maxPath, out int iconIndex);
        void SetIconLocation([MarshalAs(UnmanagedType.LPWStr)] string iconPath, int iconIndex);
        void SetRelativePath([MarshalAs(UnmanagedType.LPWStr)] string relativePath, int reserved);
        void Resolve(IntPtr hwnd, int flags);
        void SetPath([MarshalAs(UnmanagedType.LPWStr)] string file);
    }

    [ComImport, InterfaceType(ComInterfaceType.InterfaceIsIUnknown), Guid("0000010b-0000-0000-C000-000000000046")]
    private interface IPersistFile
    {
        void GetClassID(out Guid classId);
        [PreserveSig] int IsDirty();
        void Load([MarshalAs(UnmanagedType.LPWStr)] string fileName, int mode);
        void Save([MarshalAs(UnmanagedType.LPWStr)] string fileName, [MarshalAs(UnmanagedType.Bool)] bool remember);
        void SaveCompleted([MarshalAs(UnmanagedType.LPWStr)] string fileName);
        void GetCurFile([MarshalAs(UnmanagedType.LPWStr)] out string fileName);
    }
}
