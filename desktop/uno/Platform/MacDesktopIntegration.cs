using System.Security;
using System.Text;

namespace Nexus.Platform;

/// <summary>
/// macOS desktop integration.
/// - Start at login: a per-user LaunchAgent, ~/Library/LaunchAgents/com.nexus.desktop.login.plist,
///   that starts Nexus with --background (menu bar item, no window) when the user logs in.
///   launchd reads that folder at login, so writing or deleting the file is the whole switch;
///   nothing is loaded now. SMAppService (macOS 13+) is the successor, but it needs a signed
///   bundle; it replaces this once Nexus ships with a Developer ID.
/// - Under NEXUS_PROFILE the agent goes to &lt;profile&gt;/Library/LaunchAgents with a profile
///   label and the profile's variables, so a profile never touches the real login items.
/// - nexus:// is declared in the bundle's Info.plist (CFBundleURLTypes); registration is a no-op.
/// - No desktop shortcuts on macOS; the Dock and /Applications cover it.
/// </summary>
public sealed class MacDesktopIntegration : IDesktopIntegration
{
    public const string BaseLabel = "com.nexus.desktop.login";
    private readonly string _agentsDir;
    private readonly string _label;
    private readonly Nexus.Core.ProfilePaths.Profile? _profile;

    public MacDesktopIntegration()
        : this(Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.UserProfile), "Library", "LaunchAgents"), BaseLabel, null)
    {
    }

    internal MacDesktopIntegration(string agentsDir, string label, Nexus.Core.ProfilePaths.Profile? profile)
    {
        _agentsDir = agentsDir;
        _label = label;
        _profile = profile;
    }

    internal static MacDesktopIntegration ForProfile(Nexus.Core.ProfilePaths.Profile profile) =>
        new(Path.Combine(profile.Root, "Library", "LaunchAgents"), BaseLabel + ".profile-" + profile.Name, profile);

    public string AgentPath => Path.Combine(_agentsDir, _label + ".plist");

    public bool StartsAtLogin => File.Exists(AgentPath);

    public void SetStartsAtLogin(bool enabled)
    {
        if (!enabled)
        {
            if (File.Exists(AgentPath)) File.Delete(AgentPath);
            return;
        }
        var exe = Environment.ProcessPath;
        if (string.IsNullOrEmpty(exe) || !File.Exists(exe))
        {
            throw new InvalidOperationException("Could not find the Nexus executable to start at login.");
        }
        // Gatekeeper runs a quarantined app from a random read-only copy; that
        // path is gone after a restart.
        if (exe.Contains("/AppTranslocation/", StringComparison.Ordinal))
        {
            throw new InvalidOperationException("Move Nexus to the Applications folder first, then turn this on again.");
        }
        Directory.CreateDirectory(_agentsDir);
        var temp = AgentPath + ".tmp";
        File.WriteAllText(temp, AgentPlist(exe), new UTF8Encoding(false));
        File.Move(temp, AgentPath, true);
    }

    internal string AgentPlist(string exe)
    {
        static string X(string s) => SecurityElement.Escape(s) ?? "";
        var env = new StringBuilder();
        if (_profile is not null)
        {
            env.Append("  <key>EnvironmentVariables</key>\n  <dict>\n");
            env.Append($"    <key>{Nexus.Core.ProfilePaths.NameVariable}</key><string>{X(_profile.Name)}</string>\n");
            env.Append($"    <key>{Nexus.Core.ProfilePaths.RootVariable}</key><string>{X(_profile.Root)}</string>\n");
            env.Append("  </dict>\n");
        }
        var sb = new StringBuilder();
        sb.Append("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n");
        sb.Append("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n");
        sb.Append("<plist version=\"1.0\">\n<dict>\n");
        sb.Append($"  <key>Label</key><string>{X(_label)}</string>\n");
        sb.Append("  <key>ProgramArguments</key>\n  <array>\n");
        sb.Append($"    <string>{X(exe)}</string>\n");
        sb.Append("    <string>--background</string>\n  </array>\n");
        sb.Append("  <key>RunAtLoad</key><true/>\n");
        sb.Append("  <key>LimitLoadToSessionType</key><string>Aqua</string>\n");
        sb.Append("  <key>ProcessType</key><string>Interactive</string>\n");
        sb.Append(env);
        sb.Append("</dict>\n</plist>\n");
        return sb.ToString();
    }

    public bool SupportsDesktopShortcut => false;
    public bool HasDesktopShortcut => false;
    public void SetDesktopShortcut(bool enabled) { }
    public void RegisterUrlScheme() { }
    public void UnregisterUrlScheme(string installRoot) { }
}
