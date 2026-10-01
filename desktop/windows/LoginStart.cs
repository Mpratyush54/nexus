using Microsoft.Win32;

namespace Nexus;

public static class LoginStart
{
    private const string ValueName = "Nexus";

    public static bool IsEnabled()
    {
        using var key = Registry.CurrentUser.OpenSubKey(@"Software\Microsoft\Windows\CurrentVersion\Run", false);
        return key?.GetValue(ValueName) is string;
    }

    public static void SetEnabled(bool on)
    {
        using var key = Registry.CurrentUser.OpenSubKey(@"Software\Microsoft\Windows\CurrentVersion\Run", true)
            ?? throw new InvalidOperationException("HKCU Run key is missing");
        if (!on)
        {
            key.DeleteValue(ValueName, false);
            return;
        }
        var exe = Environment.ProcessPath ?? "";
        key.SetValue(ValueName, $"\"{exe}\" --background");
    }
}
