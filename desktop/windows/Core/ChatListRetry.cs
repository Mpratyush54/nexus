#nullable enable
namespace Nexus.Core;

/// <summary>
/// What the sidebar does when a chat-list request fails. Switching projects
/// first shows the chats of that project already in memory (from the
/// all-projects list, usually only a few), then asks the server for the
/// project's own list. When that request failed (a slow server timing out),
/// the few chats stayed on screen as if they were all of them, with no "more"
/// and no retry, so the project's earlier chats never appeared. Now the
/// request is retried a few times and the list says it is incomplete.
/// </summary>
internal static class ChatListRetry
{
    internal const string UnreachableEmpty = "Your chats appear here when Nexus reconnects.";

    /// A slow first answer: after this long the reachability probe runs, so
    /// a silent outage shows as one within a few seconds.
    internal static readonly TimeSpan FirstAnswer = TimeSpan.FromSeconds(3);

    /// Signing in reloads the list; retrying a signed-out request can't help.
    internal static bool ShouldRetry(string? error) => !NxErrors.IsSignedOut(error);

    internal static readonly TimeSpan[] Delays = [TimeSpan.FromSeconds(2), TimeSpan.FromSeconds(5), TimeSpan.FromSeconds(15)];

    /// The wait before retry number <paramref name="attempt"/> (0-based), or null when retries are used up.
    internal static TimeSpan? NextDelay(int attempt) => attempt >= 0 && attempt < Delays.Length ? Delays[attempt] : null;

    /// The status line under the list after a failed request.
    /// <param name="project">A single project is shown (not "All projects").</param>
    /// <param name="shown">How many chats are on screen (a partial preview).</param>
    internal static string Status(bool project, int shown, bool unreachable, bool willRetry, string? error)
    {
        // Signed out, or nothing to show and the server is unreachable: say
        // so now. Retrying keeps going in the background, but "Loading your
        // chats…" for the ~20 s of retries read as a hang.
        if (NxErrors.IsSignedOut(error)) return NxErrors.SignedOutMessage;
        if (unreachable && shown == 0) return UnreachableEmpty;
        if (willRetry)
            return project ? (shown > 0 ? "Loading this project's earlier chats…" : "Loading this project's chats…") : shown > 0 ? "" : "Loading your chats…";
        if (unreachable)
            return shown == 0 ? UnreachableEmpty
                : project ? "Some of this project's chats couldn't load. They appear when Nexus reconnects, or choose the project again to retry." : "";
        var why = string.IsNullOrWhiteSpace(error) ? "Couldn't load chats." : error!;
        return project && shown > 0 ? "Some of this project's chats couldn't load. Choose the project again to retry. " + why : why;
    }
}
