using Nexus.Platform;

namespace Nexus;

/// <summary>
/// Start-at-login, delegated to the platform integration. Under
/// NEXUS_PROFILE that integration never touches the per-user Run key,
/// which belongs to the normal install (see DesktopIntegration.Current).
/// </summary>
public static class LoginStart
{
    public static bool IsEnabled() => DesktopIntegration.Current.StartsAtLogin;

    public static void SetEnabled(bool on) => DesktopIntegration.Current.SetStartsAtLogin(on);
}
