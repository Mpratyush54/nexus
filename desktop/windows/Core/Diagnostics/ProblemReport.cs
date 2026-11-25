#nullable enable
using System.Security.Cryptography;
using System.Text.Encodings.Web;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Text.RegularExpressions;

namespace Nexus.Core.Diagnostics;

/// <summary>
/// One problem report: the exact wire body of POST /v1/diagnostics
/// (internal/diagnostics.Report). It is redacted and capped here, before
/// it is written to disk or shown in a preview, with the server's limits.
/// </summary>
internal sealed class ProblemReport
{
    public const string KindCrash = "crash";
    public const string KindError = "error";
    public const string KindUserReport = "user_report";

    public const int MaxMessageChars = 2000;
    public const int MaxStackChars = 32 * 1024;
    public const int MaxLogLines = 200;
    public const int MaxLogLineChars = 1000;
    public const int MaxUserNoteChars = 4000;
    public const int MaxContextKeys = 16;
    public const int MaxContextValueChars = 256;

    [JsonPropertyName("report_id")] public string ReportId { get; set; } = NewReportId();
    [JsonPropertyName("kind")] public string Kind { get; set; } = KindError;
    [JsonPropertyName("source")] public string Source { get; set; } = "desktop";
    [JsonPropertyName("app_version")] public string AppVersion { get; set; } = "";
    [JsonPropertyName("os")] public string Os { get; set; } = "";
    [JsonPropertyName("message")] public string Message { get; set; } = "";
    [JsonPropertyName("stack")] public string? Stack { get; set; }
    [JsonPropertyName("logs")] public List<string>? Logs { get; set; }
    [JsonPropertyName("user_note")] public string? UserNote { get; set; }
    [JsonPropertyName("timestamp")] public DateTimeOffset? Timestamp { get; set; }
    [JsonPropertyName("context")] public Dictionary<string, string>? Context { get; set; }

    private static readonly JsonSerializerOptions Compact = new()
    {
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
        Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping,
    };

    private static readonly JsonSerializerOptions Indented = new(Compact) { WriteIndented = true };

    private static readonly Regex IdPattern = new("^[A-Za-z0-9_-]{8,64}$", RegexOptions.CultureInvariant);

    public static string NewReportId() => Convert.ToHexString(RandomNumberGenerator.GetBytes(16)).ToLowerInvariant();

    public static bool ValidId(string? id) => id is not null && IdPattern.IsMatch(id);

    /// A crash or error report for an exception. message is
    /// "{ExceptionType}: {Message}" and stack is exception.ToString(),
    /// which includes inner exceptions.
    public static ProblemReport FromException(string kind, Exception ex, string via, IEnumerable<string>? logs,
        string appVersion, string os, IDictionary<string, string>? context = null)
    {
        var report = new ProblemReport
        {
            Kind = kind,
            AppVersion = appVersion,
            Os = os,
            Message = $"{ex.GetType().FullName}: {ex.Message}",
            Stack = ex.ToString(),
            Logs = logs?.ToList(),
            Timestamp = DateTimeOffset.UtcNow,
            Context = new Dictionary<string, string>(StringComparer.Ordinal) { ["via"] = via, ["desktop_version"] = appVersion },
        };
        if (context is not null)
            foreach (var (key, value) in context) report.Context[key] = value;
        return report.Sanitize();
    }

    /// Redacts every free-text field and applies the server's caps, in
    /// place. Idempotent, so a report can be sanitised again before sending.
    public ProblemReport Sanitize()
    {
        AppVersion = Cap(OneLine(Redactor.Redact(AppVersion)), 64);
        Os = Cap(OneLine(Redactor.Redact(Os)), 128);
        Message = Cap(Redactor.Redact(Message).Trim(), MaxMessageChars);
        Stack = Stack is null ? null : NullIfEmpty(Cap(Redactor.Redact(Stack).Trim(), MaxStackChars));
        UserNote = UserNote is null ? null : NullIfEmpty(Cap(Redactor.Redact(UserNote).Trim(), MaxUserNoteChars));
        if (Logs is not null)
        {
            Logs = Logs.Skip(Math.Max(0, Logs.Count - MaxLogLines))
                .Select(line => Cap(Redactor.Redact(line).TrimEnd(), MaxLogLineChars))
                .Where(line => line.Length > 0)
                .ToList();
            if (Logs.Count == 0) Logs = null;
        }
        if (Context is not null)
        {
            Context = Context.OrderBy(pair => pair.Key, StringComparer.Ordinal)
                .Where(pair => pair.Key.Trim().Length > 0)
                .Take(MaxContextKeys)
                .ToDictionary(pair => Cap(OneLine(pair.Key), 64), pair => Cap(OneLine(Redactor.Redact(pair.Value)), MaxContextValueChars), StringComparer.Ordinal);
            if (Context.Count == 0) Context = null;
        }
        if (Message.Length == 0)
        {
            Message = FirstLine(Stack) is { Length: > 0 } first ? first : Cap(FirstLine(UserNote), 200);
        }
        return this;
    }

    public string ToJson(bool indented = false) => JsonSerializer.Serialize(this, indented ? Indented : Compact);

    public static ProblemReport? FromJson(string json)
    {
        try { return JsonSerializer.Deserialize<ProblemReport>(json, Compact); }
        catch (JsonException) { return null; }
    }

    private static string OneLine(string text) => text.Replace('\r', ' ').Replace('\n', ' ').Replace('\t', ' ').Trim();

    private static string FirstLine(string? text)
    {
        var t = (text ?? "").Trim();
        var cut = t.IndexOfAny(['\r', '\n']);
        return Cap((cut >= 0 ? t[..cut] : t).Trim(), 300);
    }

    private static string? NullIfEmpty(string text) => text.Length == 0 ? null : text;

    private static string Cap(string text, int max)
    {
        text = text.Replace("\0", "");
        if (text.Length <= max) return text;
        var cut = max;
        if (char.IsHighSurrogate(text[cut - 1])) cut--;
        return text[..cut] + "…";
    }
}

/// <summary>
/// Report files under &lt;base&gt;\reports, shared with the Go core
/// (internal/diagnostics/capture.go):
/// <list type="bullet">
/// <item>held\&lt;id&gt;.json — something crashed; waits for Send / Don't send.</item>
/// <item>pending\&lt;id&gt;.json (anonymous) or &lt;id&gt;.signed.json (with the
/// saved sign-in) — agreed to send; the core uploads with retry.</item>
/// </list>
/// Writes are synchronous and atomic (temp file + rename), because a crash
/// handler may be the last code the process runs.
/// </summary>
internal static class ReportStore
{
    public static string HeldDir(string baseDir) => Path.Combine(baseDir, "reports", "held");
    public static string PendingDir(string baseDir) => Path.Combine(baseDir, "reports", "pending");

    public static string WriteHeld(string baseDir, ProblemReport report) =>
        Write(HeldDir(baseDir), report.ReportId + ".json", report);

    public static string WritePending(string baseDir, ProblemReport report, bool signed)
    {
        var dir = PendingDir(baseDir);
        TryDelete(Path.Combine(dir, report.ReportId + ".json"));
        TryDelete(Path.Combine(dir, report.ReportId + ".signed.json"));
        return Write(dir, report.ReportId + (signed ? ".signed.json" : ".json"), report);
    }

    private static string Write(string dir, string name, ProblemReport report)
    {
        if (!ProblemReport.ValidId(report.ReportId)) throw new ArgumentException("invalid report id");
        Directory.CreateDirectory(dir);
        var path = Path.Combine(dir, name);
        var tmp = path + ".tmp";
        File.WriteAllText(tmp, report.ToJson());
        File.Move(tmp, path, overwrite: true);
        return path;
    }

    /// Held reports, newest first. Unreadable files are removed.
    public static IReadOnlyList<ProblemReport> ReadHeld(string baseDir)
    {
        var dir = HeldDir(baseDir);
        if (!Directory.Exists(dir)) return [];
        var reports = new List<(ProblemReport Report, DateTime At)>();
        foreach (var file in new DirectoryInfo(dir).EnumerateFiles("*.json"))
        {
            ProblemReport? report = null;
            try { report = ProblemReport.FromJson(File.ReadAllText(file.FullName)); }
            catch (IOException) { continue; }
            if (report is null || !ProblemReport.ValidId(report.ReportId))
            {
                TryDelete(file.FullName);
                continue;
            }
            if (DateTime.UtcNow - file.LastWriteTimeUtc > TimeSpan.FromDays(7))
            {
                TryDelete(file.FullName);
                continue;
            }
            reports.Add((report, file.LastWriteTimeUtc));
        }
        return reports.OrderByDescending(r => r.At).Select(r => r.Report).ToList();
    }

    /// Moves a held report to the upload queue.
    public static void Release(string baseDir, ProblemReport report, bool signed)
    {
        WritePending(baseDir, report.Sanitize(), signed);
        Discard(baseDir, report.ReportId);
    }

    public static void Discard(string baseDir, string reportId)
    {
        if (!ProblemReport.ValidId(reportId)) return;
        TryDelete(Path.Combine(HeldDir(baseDir), reportId + ".json"));
    }

    public static int PendingCount(string baseDir)
    {
        var dir = PendingDir(baseDir);
        return Directory.Exists(dir) ? Directory.EnumerateFiles(dir, "*.json").Count() : 0;
    }

    private static void TryDelete(string path)
    {
        try { File.Delete(path); } catch (IOException) { } catch (UnauthorizedAccessException) { }
    }
}

/// <summary>
/// The "logs" part of a report: the newest lines of the desktop's and the
/// core's log files, merged by timestamp (both start with an RFC 3339 UTC
/// time), already redacted by their writers and redacted again here.
/// </summary>
internal static class LogBundle
{
    private const int TailBytes = 256 * 1024;

    public static List<string> Recent(string logsDir, int count = ProblemReport.MaxLogLines)
    {
        var lines = new List<string>();
        foreach (var prefix in new[] { "nexus", "core" })
        {
            var files = RollingLogFile.Files(logsDir, prefix);
            var taken = new List<string>();
            foreach (var file in files.Take(3))
            {
                var tail = ReadTail(file);
                taken.InsertRange(0, tail);
                if (taken.Count >= count) break;
            }
            lines.AddRange(taken.Skip(Math.Max(0, taken.Count - count)));
        }
        return Merge(lines, count);
    }

    /// Orders lines by their leading timestamp (stable for equal times) and
    /// keeps the newest count. Lines without a timestamp keep their place
    /// after the line before them.
    public static List<string> Merge(IEnumerable<string> lines, int count)
    {
        var keyed = new List<(string Key, int Order, string Line)>();
        var lastKey = "";
        var order = 0;
        foreach (var line in lines)
        {
            if (line.Trim().Length == 0) continue;
            var key = line.Length >= 24 && line[4] == '-' && line[10] == 'T' && line[23] == 'Z' ? line[..24] : lastKey;
            lastKey = key;
            keyed.Add((key, order++, Redactor.Redact(line)));
        }
        return keyed.OrderBy(k => k.Key, StringComparer.Ordinal).ThenBy(k => k.Order)
            .Select(k => k.Line).TakeLast(count).ToList();
    }

    private static List<string> ReadTail(string path)
    {
        try
        {
            using var stream = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete);
            var skip = Math.Max(0, stream.Length - TailBytes);
            stream.Seek(skip, SeekOrigin.Begin);
            using var reader = new StreamReader(stream);
            var text = reader.ReadToEnd();
            var lines = text.Split('\n').Select(l => l.TrimEnd('\r')).Where(l => l.Length > 0).ToList();
            if (skip > 0 && lines.Count > 0) lines.RemoveAt(0); // partial first line
            return lines;
        }
        catch (IOException) { return []; }
        catch (UnauthorizedAccessException) { return []; }
    }
}
