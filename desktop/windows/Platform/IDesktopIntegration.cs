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
    // A NEXUS_PROFILE instance (demos, side-by-side tests) must never touch
    // the real Run key, desktop or nexus:// handler of the normal install.
    public static IDesktopIntegration Current { get; set; } =
        Nexus.Core.ProfilePaths.Current is { } profile ? new ProfileDesktopIntegration(profile.Root)
#if HAS_UNO
        // Uno head (desktop/uno): macOS integration; the Windows preview of
        // that head never touches the registry.
        : OperatingSystem.IsMacOS() ? new MacDesktopIntegration()
#else
        : OperatingSystem.IsWindows() ? new WindowsDesktopIntegration()
#endif
        : new NoDesktopIntegration();
}

/// <summary>
/// NEXUS_PROFILE stand-in: remembers the choices in the profile folder so
/// the setup guide and Settings behave normally, and changes nothing on the
/// real system.
/// </summary>
public sealed class ProfileDesktopIntegration : IDesktopIntegration
{
    private readonly string _state;

    public ProfileDesktopIntegration(string profileRoot) => _state = Path.Combine(profileRoot, "desktop-integration");

    private bool Flag(string name) => File.Exists(Path.Combine(_state, name));

    private void SetFlag(string name, bool on)
    {
        Directory.CreateDirectory(_state);
        var path = Path.Combine(_state, name);
        if (on) File.WriteAllText(path, "on");
        else if (File.Exists(path)) File.Delete(path);
    }

    public bool StartsAtLogin => Flag("start-at-login");
    public void SetStartsAtLogin(bool enabled) => SetFlag("start-at-login", enabled);
    public bool SupportsDesktopShortcut => true;
    public bool HasDesktopShortcut => Flag("desktop-shortcut");
    public void SetDesktopShortcut(bool enabled) => SetFlag("desktop-shortcut", enabled);
    public void RegisterUrlScheme() { }
    public void UnregisterUrlScheme(string installRoot) { }
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
