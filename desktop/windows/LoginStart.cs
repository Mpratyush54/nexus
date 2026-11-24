using Nexus.Platform;

namespace Nexus;

/// <summary>Start-at-login, delegated to the platform integration.</summary>
public static class LoginStart
{
    public static bool IsEnabled() => DesktopIntegration.Current.StartsAtLogin;

    public static void SetEnabled(bool on) => DesktopIntegration.Current.SetStartsAtLogin(on);
}
