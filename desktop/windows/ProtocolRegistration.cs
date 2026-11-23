using Microsoft.Win32;
using System.Security;

namespace Nexus;

public static class ProtocolRegistration
{
    private const string Scheme = "nexus";

    public static void Register()
    {
        try
        {
            var executable = Environment.ProcessPath;
            if (string.IsNullOrWhiteSpace(executable))
            {
                return;
            }
            using var root = Registry.CurrentUser.CreateSubKey($@"Software\Classes\{Scheme}");
            if (root is null)
            {
                return;
            }
            root.SetValue(null, "URL:Nexus Protocol");
            root.SetValue("URL Protocol", "");
            using var command = root.CreateSubKey(@"shell\open\command");
            command?.SetValue(null, $"\"{executable}\" \"%1\"");
        }
        catch (UnauthorizedAccessException)
        {
        }
        catch (SecurityException)
        {
        }
        catch (IOException)
        {
        }
    }
}
