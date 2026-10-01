namespace Nexus;

/// <summary>
/// Velopack auto-update placeholder (product-spec D5 / P2).
/// TODO: reference Velopack NuGet, call UpdateManager on launch / quit.
/// No-ops until the packing pipeline ships (<c>vpk pack</c> on Windows).
/// </summary>
public sealed class UpdateService
{
    public bool IsAvailable => false;

    public Task CheckForUpdatesAsync(CancellationToken ct = default)
    {
        _ = ct;
        return Task.CompletedTask;
    }

    public Task ApplyUpdatesAndRestartAsync(CancellationToken ct = default)
    {
        _ = ct;
        // Velopack applies into a new version directory on quit/relaunch.
        return Task.CompletedTask;
    }
}
