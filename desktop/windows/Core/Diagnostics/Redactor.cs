#nullable enable
using System.Text.RegularExpressions;

namespace Nexus.Core.Diagnostics;

/// <summary>
/// C# port of internal/diagnostics/redact.go: the same rules, in the same
/// order, so a log line or report is redacted on this computer exactly as
/// the server would redact it. Keep the two files in step; the shared test
/// vectors live in diagnostics_test.go and RedactorTests.cs.
/// </summary>
internal static class Redactor
{
    public const string Redacted = "[redacted]";

    private const RegexOptions Opts = RegexOptions.CultureInvariant | RegexOptions.Compiled;
    private const RegexOptions OptsI = Opts | RegexOptions.IgnoreCase;

    // Order matters: specific token shapes before the generic key=value rule,
    // paths last so a token inside a path is already gone.
    private static readonly (Regex Re, string Replacement)[] Rules =
    [
        // Authorization headers and "Bearer <token>" anywhere.
        (new Regex(@"\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}", OptsI), "$1 " + Redacted),
        // JSON Web Tokens.
        (new Regex(@"\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}", Opts), Redacted),
        // Nexus API tokens.
        (new Regex(@"\bnxs_[A-Za-z0-9_-]{6,}", Opts), "nxs_" + Redacted),
        // OpenAI / Anthropic / OpenRouter style keys.
        (new Regex(@"\bsk-[A-Za-z0-9_-]{12,}", Opts), "sk-" + Redacted),
        // GitHub tokens.
        (new Regex(@"\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})", Opts), Redacted),
        // AWS access key IDs.
        (new Regex(@"\b(AKIA|ASIA)[0-9A-Z]{16}\b", Opts), Redacted),
        // Slack tokens.
        (new Regex(@"\bxox[abprs]-[A-Za-z0-9-]{10,}", Opts), Redacted),
        // Private key blocks.
        (new Regex(@"-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)", Opts), Redacted),
        // Credentials in URLs: scheme://user:pass@host.
        (new Regex(@"([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@", Opts), "$1" + Redacted + "@"),
        // Secret-looking query parameters.
        (new Regex(@"([?&](?:token|access_token|refresh_token|id_token|api_key|apikey|key|secret|password|sig|signature|code|otp|auth)=)[^&#\s""']+", OptsI), "${1}" + Redacted),
        // key=value / "key": "value" pairs whose key names a secret.
        (new Regex(@"(""?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|auth[_-]?token|client[_-]?secret|secret|password|passwd|pwd|authorization|cookie|set-cookie|session[_-]?token|private[_-]?key)""?\s*[:=]\s*""?)[^\s""',;&]{3,}", OptsI), "${1}" + Redacted),
        // Email addresses.
        (new Regex(@"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}", Opts), "[email]"),
        // Home directories → ~ (escaped Windows, Windows, macOS/Linux, /root).
        (new Regex(@"\b[A-Z]:\\\\(?:Users|Documents and Settings)\\\\[^\\""'<>|\r\n]+", OptsI), "~"),
        (new Regex(@"\b[A-Z]:[\\/](?:Users|Documents and Settings)[\\/][^\\/""'<>|:\r\n]+", OptsI), "~"),
        (new Regex(@"(^|[^A-Za-z0-9._~-])/(?:Users|home)/[^/\s""'<>:]+", Opts), "$1~"),
        (new Regex(@"(^|[\s""'(=:])/root\b", Opts), "$1~"),
    ];

    /// Strips credentials, email addresses and home directories. Idempotent.
    public static string Redact(string? text)
    {
        if (string.IsNullOrEmpty(text)) return text ?? "";
        foreach (var (re, replacement) in Rules)
        {
            text = re.Replace(text, replacement);
        }
        return text;
    }
}
