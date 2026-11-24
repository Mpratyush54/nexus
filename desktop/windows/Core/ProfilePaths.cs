#nullable enable
using System.Text.RegularExpressions;

namespace Nexus.Core;

// Opt-in isolated profile for demos, screenshots and side-by-side testing.
//
// NEXUS_PROFILE=<name> runs a fully separate Nexus next to the normal one:
// its own single-instance mutex and pipe, and every per-user folder (the
// shell's prefs and caches, and the Go core's config, capture roots and
// ~/.claude, ~/.codex, ...) under one profile root. NEXUS_PROFILE_ROOT picks
// that root; otherwise it is %TEMP%\nexus-profile-<name>.
//
// When NEXUS_PROFILE is unset every member returns exactly what the OS
// returned before this type existed, so normal installs are unaffected.
internal static partial class ProfilePaths
{
    public const string NameVariable = "NEXUS_PROFILE";
    public const string RootVariable = "NEXUS_PROFILE_ROOT";

    private static readonly Lazy<Profile?> s_current = new(() =>
        Resolve(Environment.GetEnvironmentVariable(NameVariable),
            Environment.GetEnvironmentVariable(RootVariable),
            Path.GetTempPath()));

    internal sealed record Profile(string Name, string Root);

    public static Profile? Current => s_current.Value;

    public static bool Active => Current is not null;

    // Resolve is pure so tests can cover it without touching the process.
    // A name that is empty after trimming means "no profile". Names are kept
    // to a safe character set because they end up in kernel object names.
    internal static Profile? Resolve(string? name, string? root, string tempDir)
    {
        var trimmed = (name ?? "").Trim();
        if (trimmed.Length == 0)
        {
            return null;
        }
        if (!SafeName().IsMatch(trimmed))
        {
            throw new ArgumentException(
                $"{NameVariable} may only use letters, digits, '.', '_' and '-' (max 40 characters).", nameof(name));
        }
        var chosenRoot = string.IsNullOrWhiteSpace(root)
            ? Path.Combine(tempDir, "nexus-profile-" + trimmed)
            : root.Trim();
        return new Profile(trimmed, Path.GetFullPath(chosenRoot));
    }

    // GetFolderPath replacement. Environment.GetFolderPath asks the shell for
    // known folders and ignores environment variables, so a profile has to
    // be applied here rather than through APPDATA/LOCALAPPDATA alone.
    public static string Folder(Environment.SpecialFolder folder)
    {
        var profile = Current;
        return profile is null ? Environment.GetFolderPath(folder) : FolderIn(profile.Root, folder);
    }

    internal static string FolderIn(string root, Environment.SpecialFolder folder) => folder switch
    {
        Environment.SpecialFolder.UserProfile => root,
        Environment.SpecialFolder.ApplicationData => Path.Combine(root, "AppData", "Roaming"),
        Environment.SpecialFolder.LocalApplicationData => Path.Combine(root, "AppData", "Local"),
        Environment.SpecialFolder.MyDocuments => Path.Combine(root, "Documents"),
        _ => throw new ArgumentOutOfRangeException(nameof(folder), folder, "No profile mapping for this folder."),
    };

    // Kernel object names (mutex, pipe) for the single-instance handshake.
    public static string InstanceName(string baseName) => InstanceNameFor(Current, baseName);

    internal static string InstanceNameFor(Profile? profile, string baseName) =>
        profile is null ? baseName : baseName + "-profile-" + profile.Name;

    // The variables the Go core (os.UserHomeDir, os.UserConfigDir,
    // os.UserCacheDir, os.TempDir and direct APPDATA/LOCALAPPDATA reads) and
    // any child process resolve per-user folders from.
    internal static IReadOnlyDictionary<string, string?> EnvironmentFor(string root)
    {
        var local = FolderIn(root, Environment.SpecialFolder.LocalApplicationData);
        var temp = Path.Combine(local, "Temp");
        return new Dictionary<string, string?>(StringComparer.OrdinalIgnoreCase)
        {
            ["USERPROFILE"] = root,
            ["HOME"] = root,
            ["APPDATA"] = FolderIn(root, Environment.SpecialFolder.ApplicationData),
            ["LOCALAPPDATA"] = local,
            ["TEMP"] = temp,
            ["TMP"] = temp,
            // Explicit overrides inherited from the parent shell would point
            // the core back at the real profile; the profile root wins.
            ["NEXUS_CONFIG_DIR"] = null,
            ["NEXUS_CACHE_DIR"] = null,
            ["NEXUS_PROJECT_ROOTS"] = null,
            ["CENTRAL_MEMORY_CONFIG_DIR"] = null,
            ["CODEX_HOME"] = null,
            ["XDG_CONFIG_HOME"] = null,
            ["XDG_DATA_HOME"] = null,
            ["XDG_CACHE_HOME"] = null,
            // Server and credential overrides beat config.json in the core.
            // Inherited ones belong to the normal account; a profile signs
            // in only through its own config.json.
            ["NEXUS_SERVER"] = null,
            ["CENTRAL_SERVER_URL"] = null,
            ["NEXUS_TOKEN"] = null,
            ["CENTRAL_MEMORY_TOKEN"] = null,
        };
    }

    // Applies the profile to this process before the Go core is loaded.
    // A no-op when no profile is set.
    public static void ApplyToProcess()
    {
        var profile = Current;
        if (profile is null)
        {
            return;
        }
        foreach (var (key, value) in EnvironmentFor(profile.Root))
        {
            if (value is not null)
            {
                Directory.CreateDirectory(value);
            }
            Environment.SetEnvironmentVariable(key, value);
        }
        Directory.CreateDirectory(FolderIn(profile.Root, Environment.SpecialFolder.MyDocuments));
        // A capture workspace is honoured only inside the profile.
        var workspace = Environment.GetEnvironmentVariable("NEXUS_WORKSPACE");
        if (!string.IsNullOrWhiteSpace(workspace) && !IsInside(profile.Root, workspace))
        {
            Environment.SetEnvironmentVariable("NEXUS_WORKSPACE", null);
        }
    }

    internal static bool IsInside(string root, string path)
    {
        var full = Path.GetFullPath(path).TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
        var parent = Path.GetFullPath(root).TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
        return full.Equals(parent, StringComparison.OrdinalIgnoreCase)
            || full.StartsWith(parent + Path.DirectorySeparatorChar, StringComparison.OrdinalIgnoreCase);
    }

    [GeneratedRegex("^[A-Za-z0-9._-]{1,40}$")]
    private static partial Regex SafeName();
}
