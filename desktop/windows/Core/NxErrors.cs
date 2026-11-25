#nullable enable
using System.Security.Cryptography;
using System.Text.RegularExpressions;

namespace Nexus.Core;

/// Classifies nx_call errors and turns them into sentences people can act
/// on. Raw engine text (Go/HTTP errors such as "dial tcp … connectex") is
/// written to the log under a short reference id and never shown.
internal static partial class NxErrors
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
            e.Contains("not signed in", StringComparison.OrdinalIgnoreCase) ||
            e.Contains("You're signed out of Nexus", StringComparison.Ordinal);
    }

    internal const string SignedOutMessage = "You're signed out of Nexus on this computer (the sign-in may have expired). Sign in again in Settings > Account.";
    internal const string UnreachableMessage = "Can't reach the Nexus server right now. Nexus keeps trying and reconnects by itself.";
    internal const string UnsupportedMessage = "Your Nexus server doesn't support this yet.";

    /// True for failures that mean the server could not be reached at all
    /// (as opposed to the server answering with an error).
    internal static bool IsUnreachable(string? error)
    {
        if (string.IsNullOrWhiteSpace(error)) return false;
        var e = error.ToLowerInvariant();
        return e.Contains("dial tcp") || e.Contains("connectex") || e.Contains("connection refused") ||
            e.Contains("actively refused") || e.Contains("no such host") || e.Contains("i/o timeout") ||
            e.Contains("context deadline exceeded") || e.Contains("client.timeout") || e.Contains("tls handshake timeout") ||
            e.Contains("connection reset") || e.Contains("forcibly closed") || e.Contains("network is unreachable") ||
            e.Contains("nexus is offline") || e.Contains("unexpected eof") || e.EndsWith(": eof") ||
            e.Contains("502 bad gateway") || e.Contains("503 service unavailable") || e.Contains("504 gateway");
    }

    /// The server (or this engine build) has no such feature.
    internal static bool IsUnsupported(string? error)
    {
        if (string.IsNullOrWhiteSpace(error)) return false;
        var e = error.ToLowerInvariant();
        return e.Contains("unknown nx_call method") || e.Contains("404 page not found") || e.StartsWith("404 not found") ||
            e.Contains("method not allowed") || e.Contains("doesn't support this yet");
    }

    // Engine and transport text that reads as a program talking, not a sentence.
    private static readonly string[] TechnicalMarkers =
    [
        "http://", "https://", "get \"", "post \"", "put \"", "delete \"", "tcp ", "x509", "json:", "unexpected end",
        "invalid character", "nil pointer", "panic", "sql", "pq:", "%!", "status code", ".go:", "exit status", "rpc error",
        "cannot unmarshal", "goroutine", "context canceled", "<html", "{\"", "internal server error"
    ];

    private static bool LooksTechnical(string error)
    {
        var e = error.ToLowerInvariant();
        return TechnicalMarkers.Any(e.Contains) || StatusLine().IsMatch(error);
    }

    [GeneratedRegex(@"^\s*[45]\d\d\b")]
    private static partial Regex StatusLine();

    [GeneratedRegex(@"\(ref ([A-Za-z0-9-]{4,64})\)")]
    private static partial Regex ServerRef();

    [GeneratedRegex(@"\b5\d\d\b")]
    private static partial Regex ServerStatus();

    /// The sentence to show for a failed call, plus the reference id it was
    /// logged under ("" when the text is already a plain sentence from the
    /// server or the app; that keeps its own "(ref …)" if it had one).
    internal static (string Message, string Ref) Friendly(string? raw)
    {
        var text = (raw ?? "").Trim();
        if (text.Length == 0) return ("", "");
        if (IsSignedOut(text)) return (SignedOutMessage, "");
        string? message = null;
        if (IsUnreachable(text)) message = UnreachableMessage;
        else if (IsUnsupported(text)) message = UnsupportedMessage;
        else if (text.Contains("403", StringComparison.Ordinal) || text.Contains("forbidden", StringComparison.OrdinalIgnoreCase))
            message = "You don't have access to this.";
        else if (LooksTechnical(text))
            message = ServerStatus().IsMatch(text) ? "The Nexus server ran into a problem. Try again in a moment." : "Something went wrong.";
        if (message is null) return (text, "");
        var reference = ServerRef().Match(text) is { Success: true } hit ? hit.Groups[1].Value : NewRef();
        return (message + " (ref " + reference + ")", reference);
    }

    private const string RefAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";

    internal static string NewRef()
    {
        Span<byte> bytes = stackalloc byte[8];
        RandomNumberGenerator.Fill(bytes);
        var chars = new char[8];
        for (var i = 0; i < chars.Length; i++) chars[i] = RefAlphabet[bytes[i] % RefAlphabet.Length];
        return new string(chars);
    }
}
