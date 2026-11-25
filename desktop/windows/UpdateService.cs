using Velopack;

namespace Nexus;

/// <summary>
/// Velopack auto-update. Installed copies check the release feed in the
/// background and download new versions (as deltas when possible); the
/// update is applied the next time Nexus starts, or at once from Settings.
/// Development builds and unzipped copies without Update.exe are not
/// "installed" and never check.
///
/// The Uno head (macOS) compiles this same file. There the feed is the same
/// folder with its own channels (releases.osx.json / releases.osx-beta.json,
/// pack id NexusMac), UpdateMac inside Nexus.app swaps the whole bundle, and
/// a copy that can't be replaced in place (still on the disk image, or run
/// translocated from Downloads) doesn't check; see Platform/MacInstall.cs.
/// </summary>
public sealed class UpdateService
{
    // Velopack feed (releases.{channel}.json + packages) published by
    // .github/workflows/release-desktop.yml. NEXUS_UPDATE_URL overrides it
    // for testing a feed.
    private const string DefaultFeed = "https://central-memory-releases.s3.ap-south-1.amazonaws.com/desktop/velopack";

    public static UpdateService Shared { get; } = new();

    private readonly UpdateManager? _manager;
    private UpdateInfo? _staged;

    private UpdateService()
    {
        try
        {
            var feed = Environment.GetEnvironmentVariable("NEXUS_UPDATE_URL");
            _manager = new UpdateManager(string.IsNullOrWhiteSpace(feed) ? DefaultFeed : feed.Trim());
        }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Warn("update", "update manager unavailable", ex);
            _manager = null;
        }
    }

    public bool IsAvailable => _manager?.IsInstalled == true && CanReplaceInPlace;

#if HAS_UNO
    private static bool CanReplaceInPlace => Nexus.Platform.MacInstall.CanSelfUpdate;
#else
    private static bool CanReplaceInPlace => true;
#endif

    /// <summary>Settings › Updates when <see cref="IsAvailable"/> is false: why, in one sentence.</summary>
    public string UnavailableText =>
        _manager?.IsInstalled == true && !CanReplaceInPlace
            ? "Move Nexus to your Applications folder to get updates automatically."
            : OperatingSystem.IsMacOS()
                ? "Updates come with the Nexus download for Mac. This copy wasn't installed from it, so it doesn't update itself."
                : "Updates are installed by the Nexus installer. This copy was not installed with it, so it does not update itself.";

    public string CurrentVersion => _manager?.CurrentVersion?.ToString() ?? "";

    /// <summary>The version downloaded and waiting for a restart, or "".</summary>
    public string StagedVersion => _staged?.TargetFullRelease.Version.ToString()
        ?? _manager?.UpdatePendingRestart?.Version.ToString() ?? "";

    /// <summary>Checks the feed and downloads a newer version. Returns its version, or "" when up to date.</summary>
    public async Task<string> CheckForUpdatesAsync(CancellationToken ct = default)
    {
        if (!IsAvailable || _manager is null) return "";
        var info = await _manager.CheckForUpdatesAsync().ConfigureAwait(false);
        if (info is null) return "";
        await _manager.DownloadUpdatesAsync(info, null, ct).ConfigureAwait(false);
        _staged = info;
        var version = info.TargetFullRelease.Version.ToString();
        Nexus.Core.Diagnostics.DiagLog.Info("update", $"version {version} downloaded; it is applied when Nexus restarts (running {CurrentVersion})");
        return version;
    }

    /// <summary>
    /// Background check a little after start, then every few hours. Failures
    /// (offline, feed down or not published) are never shown: they are logged
    /// once per kind of failure, not on every attempt, so a missing feed
    /// does not fill the log either.
    /// </summary>
    public void CheckInBackground(TimeSpan delay)
    {
        Nexus.Core.Diagnostics.DiagLog.Info("update", IsAvailable
            ? $"installed version {CurrentVersion}; checking in {delay.TotalSeconds:0}s"
            : "not an installed copy; no update checks");
        if (!IsAvailable) return;
        if (OperatingSystem.IsMacOS()) Console.Error.WriteLine($"[nexus-mac] update: installed version {CurrentVersion}");
        _ = Task.Run(async () =>
        {
            await Task.Delay(delay).ConfigureAwait(false);
            var lastFailure = "";
            while (true)
            {
                try
                {
                    await CheckForUpdatesAsync().ConfigureAwait(false);
                    lastFailure = "";
                }
                catch (Exception ex)
                {
                    if (ex.Message != lastFailure) Nexus.Core.Diagnostics.DiagLog.Warn("update", "background update check failed", ex);
                    lastFailure = ex.Message;
                }
                if (StagedVersion.Length > 0) return;
                await Task.Delay(TimeSpan.FromHours(6)).ConfigureAwait(false);
            }
        });
    }

    /// <summary>
    /// Velopack writes Apps &amp; features' DisplayVersion without the prerelease
    /// label (0.4.0 for 0.4.0-beta.6). Keep the full version there, including
    /// after background updates. Never under NEXUS_PROFILE.
    /// </summary>
    public void SyncDisplayVersion()
    {
        if (!IsAvailable || _manager is null || !OperatingSystem.IsWindows() || Nexus.Core.ProfilePaths.Active) return;
        try
        {
            var version = CurrentVersion;
            using var key = Microsoft.Win32.Registry.CurrentUser.OpenSubKey(@"Software\Microsoft\Windows\CurrentVersion\Uninstall\" + _manager.AppId, true);
            if (key is not null && version.Length > 0 && !string.Equals(key.GetValue("DisplayVersion") as string, version, StringComparison.Ordinal))
                key.SetValue("DisplayVersion", version);
        }
        catch (Exception ex) when (ex is UnauthorizedAccessException or System.Security.SecurityException or IOException)
        {
        }
    }

    /// <summary>Restarts into the downloaded version. The caller should shut the core down first.</summary>
    public void ApplyAndRestart()
    {
        if (_manager is null) return;
        var target = _staged?.TargetFullRelease ?? _manager.UpdatePendingRestart;
        if (target is null) return;
        _manager.ApplyUpdatesAndRestart(target);
    }
}
