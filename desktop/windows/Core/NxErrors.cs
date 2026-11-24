#nullable enable
namespace Nexus.Core;

/// Classifies nx_call errors the UI must not show as an empty page.
internal static class NxErrors
{
    /// True when the server rejected the saved sign-in (expired, revoked or
    /// missing token): the person has to sign in again, nothing is "empty".
    internal static bool IsSignedOut(string? error)
    {
        if (string.IsNullOrWhiteSpace(error)) return false;
        var e = error;
        return e.Contains("invalid bearer token", StringComparison.OrdinalIgnoreCase) ||
            e.Contains("missing bearer", StringComparison.OrdinalIgnoreCase) ||
            e.Contains("401 Unauthorized", StringComparison.OrdinalIgnoreCase) ||
            e.Contains("token expired", StringComparison.OrdinalIgnoreCase) ||
            e.Contains("not signed in", StringComparison.OrdinalIgnoreCase);
    }

    internal const string SignedOutMessage = "The Nexus server did not accept this device's sign-in (it may have expired). Refresh to retry; if it keeps happening, open Settings and sign in again.";
}
