namespace Nexus.Platform;

/// <summary>
/// The operating-system integration the shell and the setup wizard need:
/// start at login, a desktop shortcut, and the nexus:// link scheme.
/// Windows uses the registry and .lnk files (<see cref="WindowsDesktopIntegration"/>).
/// A macOS port supplies its own implementation (login item, Dock,
/// CFBundleURLTypes) and sets <see cref="DesktopIntegration.Current"/>;
/// nothing else in the UI needs to change.
/// </summary>
public interface IDesktopIntegration
{
    /// <summary>True when Nexus starts (in the background) when the user signs in.</summary>
    bool StartsAtLogin { get; }

    void SetStartsAtLogin(bool enabled);

    /// <summary>False on platforms without desktop shortcuts; the UI hides the option.</summary>
    bool SupportsDesktopShortcut { get; }

    bool HasDesktopShortcut { get; }

    void SetDesktopShortcut(bool enabled);

    /// <summary>Points nexus:// links at this copy of Nexus.</summary>
    void RegisterUrlScheme();

    /// <summary>
    /// Removes the nexus:// registration, but only when it points into
    /// <paramref name="installRoot"/>, so uninstalling one copy never breaks another.
    /// </summary>
    void UnregisterUrlScheme(string installRoot);
}

public static class DesktopIntegration
{
    public static IDesktopIntegration Current { get; set; } =
        OperatingSystem.IsWindows() ? new WindowsDesktopIntegration() : new NoDesktopIntegration();
}

/// <summary>Fallback for hosts without any integration (tests, unsupported OS).</summary>
public sealed class NoDesktopIntegration : IDesktopIntegration
{
    public bool StartsAtLogin => false;
    public void SetStartsAtLogin(bool enabled) => throw new PlatformNotSupportedException("Start at login is not available on this system.");
    public bool SupportsDesktopShortcut => false;
    public bool HasDesktopShortcut => false;
    public void SetDesktopShortcut(bool enabled) { }
    public void RegisterUrlScheme() { }
    public void UnregisterUrlScheme(string installRoot) { }
}
