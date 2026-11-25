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
    internal static readonly TimeSpan[] Delays = [TimeSpan.FromSeconds(2), TimeSpan.FromSeconds(5), TimeSpan.FromSeconds(15)];

    /// The wait before retry number <paramref name="attempt"/> (0-based), or null when retries are used up.
    internal static TimeSpan? NextDelay(int attempt) => attempt >= 0 && attempt < Delays.Length ? Delays[attempt] : null;

    /// The status line under the list after a failed request.
    /// <param name="project">A single project is shown (not "All projects").</param>
    /// <param name="shown">How many chats are on screen (a partial preview).</param>
    internal static string Status(bool project, int shown, bool unreachable, bool willRetry, string? error)
    {
        if (willRetry)
            return project ? (shown > 0 ? "Loading this project's earlier chats…" : "Loading this project's chats…") : shown > 0 ? "" : "Loading your chats…";
        if (unreachable)
            return shown == 0 ? "Your chats appear here when Nexus reconnects."
                : project ? "Some of this project's chats couldn't load. They appear when Nexus reconnects, or choose the project again to retry." : "";
        var why = string.IsNullOrWhiteSpace(error) ? "Couldn't load chats." : error!;
        return project && shown > 0 ? "Some of this project's chats couldn't load. Choose the project again to retry. " + why : why;
    }
}
