#nullable enable
namespace Nexus.Core;

/// <summary>
/// Quiet automatic retries for one card whose own call was slow or cut off
/// (Home's activity calendar, numbers): a few spaced attempts, then the card
/// keeps its "Try again" button. A probe-confirmed outage is handled by the
/// app (banner, reload on reconnect), not by these retries.
/// </summary>
internal static class CardRetry
{
    private static readonly TimeSpan[] Delays = [TimeSpan.FromSeconds(5), TimeSpan.FromSeconds(15), TimeSpan.FromSeconds(30), TimeSpan.FromSeconds(60)];

    /// <summary>Wait before retry number <paramref name="attempt"/> (0-based); null when the card stops retrying by itself.</summary>
    internal static TimeSpan? NextDelay(int attempt) => attempt >= 0 && attempt < Delays.Length ? Delays[attempt] : null;

    /// <summary>How long to wait before re-reading a calendar whose archived history the server is still reading.</summary>
    internal static readonly TimeSpan ArchivePending = TimeSpan.FromSeconds(8);
}
