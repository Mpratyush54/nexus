using Nexus.Platform;

namespace Nexus;

/// <summary>nexus:// link registration, delegated to the platform integration.</summary>
public static class ProtocolRegistration
{
    public static void Register() => DesktopIntegration.Current.RegisterUrlScheme();
}
