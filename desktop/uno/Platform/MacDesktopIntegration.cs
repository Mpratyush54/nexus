namespace Nexus.Platform;

/// <summary>
/// macOS desktop integration, first cut.
/// - nexus:// is declared in the bundle's Info.plist (CFBundleURLTypes), so
///   registration is a no-op here.
/// - Start at login: TODO(mac) write ~/Library/LaunchAgents/&lt;bundle id&gt;.plist
///   (or SMAppService once signed); until then the option reports off and
///   Settings shows the error.
/// - No desktop shortcuts on macOS; the Dock and /Applications cover it.
/// </summary>
public sealed class MacDesktopIntegration : IDesktopIntegration
{
    public bool StartsAtLogin => false;

    public void SetStartsAtLogin(bool enabled)
    {
        if (enabled) throw new PlatformNotSupportedException("Start at login is not available on this Mac build yet.");
    }

    public bool SupportsDesktopShortcut => false;
    public bool HasDesktopShortcut => false;
    public void SetDesktopShortcut(bool enabled) { }
    public void RegisterUrlScheme() { }
    public void UnregisterUrlScheme(string installRoot) { }
}
