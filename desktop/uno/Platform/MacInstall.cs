using System.Diagnostics;
using Nexus.Core.Diagnostics;

namespace Nexus.Platform;

/// <summary>
/// Where this copy of Nexus.app lives, and "Move to Applications".
///
/// The DMG asks people to drag Nexus to Applications, but some open it
/// straight from the disk image or from Downloads. Such a copy can't update
/// itself: a disk image is read-only, and a quarantined app that was never
/// moved runs from a random read-only copy (App Translocation). So the first
/// screen offers to move it, the way LetsMove-style apps do: copy the bundle
/// to /Applications (or ~/Applications when /Applications isn't writable),
/// clear the quarantine flag on that copy (the person already chose to open
/// it), move a Downloads original to the Trash, then relaunch from the new
/// place once this process has exited (and eject the disk image).
/// </summary>
internal static class MacInstall
{
    private static readonly Lazy<Location> s_location = new(Detect);

    private sealed record Location(string Bundle, string Original, bool Translocated);

    /// <summary>The running .app bundle, or "" (a development build outside a bundle).</summary>
    public static string BundlePath => s_location.Value.Bundle;

    /// <summary>Where the person put the bundle (differs from <see cref="BundlePath"/> under translocation).</summary>
    public static string OriginalPath => s_location.Value.Original;

    public static bool IsTranslocated => s_location.Value.Translocated;

    public static bool IsOnDiskImage => OriginalPath.StartsWith("/Volumes/", StringComparison.Ordinal);

    public static bool InApplications =>
        !IsTranslocated && (OriginalPath.StartsWith("/Applications/", StringComparison.Ordinal)
            || OriginalPath.StartsWith(UserApplications + "/", StringComparison.Ordinal));

    /// <summary>
    /// A copy that can't update itself and that people would lose track of:
    /// translocated, on a disk image, or left in Downloads.
    /// </summary>
    public static bool ShouldOfferMove =>
        OperatingSystem.IsMacOS() && BundlePath.Length > 0 && !InApplications
        && (IsTranslocated || IsOnDiskImage || OriginalPath.StartsWith(Downloads + "/", StringComparison.Ordinal));

    /// <summary>Velopack can replace the bundle in place only where it is writable.</summary>
    public static bool CanSelfUpdate => !OperatingSystem.IsMacOS() || (BundlePath.Length > 0 && !IsTranslocated && !IsOnDiskImage);

    private static string Home => Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
    private static string UserApplications => Path.Combine(Home, "Applications");
    private static string Downloads => Path.Combine(Home, "Downloads");

    private static Location Detect()
    {
        if (!OperatingSystem.IsMacOS()) return new("", "", false);
        var exe = Environment.ProcessPath ?? "";
        var ix = exe.IndexOf(".app/", StringComparison.OrdinalIgnoreCase);
        if (ix <= 0) return new("", "", false);
        var bundle = exe[..(ix + 4)];
        var translocated = MacNative.IsTranslocated(bundle, out var original);
        if (original.Length == 0) original = bundle;
        MacShell.Log($"bundle: {(translocated ? "translocated" : "in place")} at {Describe(original)}");
        return new(bundle, original, translocated);
    }

    private static string Describe(string path) =>
        path.StartsWith(Home + "/", StringComparison.Ordinal) ? "~" + path[Home.Length..] : path;

    /// <summary>
    /// Copies the bundle to Applications and prepares the relaunch. On success
    /// the caller quits the app; the new copy opens once this process is gone.
    /// Returns "" on success, or a sentence saying what went wrong.
    /// </summary>
    public static async Task<string> MoveToApplicationsAsync()
    {
        if (BundlePath.Length == 0) return "Nexus isn't running from an app bundle.";
        var name = Path.GetFileName(OriginalPath.TrimEnd('/'));
        if (!name.EndsWith(".app", StringComparison.OrdinalIgnoreCase)) name = "Nexus.app";
        var folder = Writable("/Applications") ? "/Applications" : UserApplications;
        var dest = Path.Combine(folder, name);
        MacShell.Log($"move to applications: {Describe(dest)}");
        try
        {
            Directory.CreateDirectory(folder);
            if (Directory.Exists(dest) && !MacNative.Trash(dest))
            {
                Directory.Delete(dest, true);
            }
            // ditto keeps the bundle exactly: symlinks (sq.version), modes, signatures.
            var copy = await RunAsync("/usr/bin/ditto", BundlePath, dest);
            if (copy.Length > 0) return "Nexus couldn't be copied to " + Describe(folder) + ". " + copy;
            // The person already chose to open this copy; without the flag the
            // moved app opens without another Gatekeeper prompt.
            await RunAsync("/usr/bin/xattr", "-dr", "com.apple.quarantine", dest);
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
        {
            DiagLog.Warn("mac", "move to applications failed", ex);
            return "Nexus couldn't be copied to " + Describe(folder) + ". " + ex.Message;
        }

        // A Downloads original goes to the Trash; a disk image is read-only
        // and is ejected after the relaunch instead.
        var volume = "";
        if (IsOnDiskImage)
        {
            var parts = OriginalPath.Split('/', StringSplitOptions.RemoveEmptyEntries);
            if (parts.Length >= 2) volume = "/Volumes/" + parts[1];
        }
        else if (OriginalPath != dest && Directory.Exists(OriginalPath))
        {
            MacNative.Trash(OriginalPath);
        }

        // Relaunch from the new place once this process (and its
        // single-instance lock) is gone.
        var script = "while kill -0 \"$1\" 2>/dev/null; do sleep 0.2; done; /usr/bin/open \"$2\"; "
                     + "if [ -n \"$3\" ]; then sleep 2; /usr/bin/hdiutil detach \"$3\" -quiet || true; fi";
        var relaunch = new ProcessStartInfo("/bin/sh") { UseShellExecute = false };
        foreach (var arg in new[] { "-c", script, "sh", Environment.ProcessId.ToString(), dest, volume }) relaunch.ArgumentList.Add(arg);
        Process.Start(relaunch);
        MacShell.Log("moved; relaunching from Applications");
        return "";
    }

    private static bool Writable(string folder)
    {
        try
        {
            var probe = Path.Combine(folder, $".nexus-write-test-{Environment.ProcessId}");
            File.WriteAllText(probe, "");
            File.Delete(probe);
            return true;
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
        {
            return false;
        }
    }

    private static async Task<string> RunAsync(string file, params string[] args)
    {
        var info = new ProcessStartInfo(file) { UseShellExecute = false, RedirectStandardError = true, RedirectStandardOutput = true };
        foreach (var arg in args) info.ArgumentList.Add(arg);
        using var process = Process.Start(info);
        if (process is null) return $"{Path.GetFileName(file)} didn't start.";
        var error = await process.StandardError.ReadToEndAsync();
        await process.WaitForExitAsync();
        return process.ExitCode == 0 ? "" : error.Trim();
    }
}
