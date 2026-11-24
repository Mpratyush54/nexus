using Velopack;

namespace Nexus;

/// <summary>
/// Velopack auto-update. Installed copies check the release feed in the
/// background and download new versions (as deltas when possible); the
/// update is applied the next time Nexus starts, or at once from Settings.
/// Development builds and unzipped copies without Update.exe are not
/// "installed" and never check.
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
        catch (Exception)
        {
            _manager = null;
        }
    }

    public bool IsAvailable => _manager?.IsInstalled == true;

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
        return info.TargetFullRelease.Version.ToString();
    }

    /// <summary>Background check a little after start; failures are silent (offline, feed down).</summary>
    public void CheckInBackground(TimeSpan delay)
    {
        if (!IsAvailable) return;
        _ = Task.Run(async () =>
        {
            try
            {
                await Task.Delay(delay).ConfigureAwait(false);
                await CheckForUpdatesAsync().ConfigureAwait(false);
            }
            catch (Exception ex)
            {
                System.Diagnostics.Trace.WriteLine("[update] background check failed: " + ex.Message);
            }
        });
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
