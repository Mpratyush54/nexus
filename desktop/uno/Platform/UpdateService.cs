namespace Nexus;

/// <summary>
/// Uno/macOS: no auto-update yet. The Windows head uses Velopack; the macOS
/// release will use Velopack's osx support or Sparkle once the app is
/// signed and notarized. Same surface as the Windows UpdateService.
/// </summary>
public sealed class UpdateService
{
    public static UpdateService Shared { get; } = new();

    private UpdateService() { }

    public bool IsAvailable => false;

    public string CurrentVersion => typeof(UpdateService).Assembly.GetName().Version?.ToString(3) ?? "";

    public string StagedVersion => "";

    public Task<string> CheckForUpdatesAsync(CancellationToken ct = default) => Task.FromResult("");

    public void CheckInBackground(TimeSpan delay) { }

    public void SyncDisplayVersion() { }

    public void ApplyAndRestart() { }
}
