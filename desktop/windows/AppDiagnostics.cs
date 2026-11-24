using System.Diagnostics;
using System.Reflection;
using System.Text.Json;
using System.Text.RegularExpressions;
using Nexus.Core;
using Nexus.Core.Diagnostics;

namespace Nexus;

/// <summary>
/// The desktop side of problem reports (spec: PR #238, phase 2).
/// <list type="bullet">
/// <item>Logging: DiagLog writes %LOCALAPPDATA%\Nexus\logs\nexus-*.log; the
/// core writes core-*.log in the same folder.</item>
/// <item>Crashes: the three .NET/WinUI hooks write a held report synchronously;
/// the next launch asks "Send a report?" unless the person chose to send
/// crash reports automatically.</item>
/// <item>Sending: a report is written to reports\pending and the core uploads
/// it (POST /v1/diagnostics), retrying with backoff while offline.</item>
/// </list>
/// </summary>
internal static partial class AppDiagnostics
{
    private static int s_unobservedRecorded;

    public static string BaseDir { get; private set; } = "";
    public static string Version { get; private set; } = "";
    public static string Os { get; } = DiagLog.OsDescription();

    /// The screen the person was on, for report context.
    public static string CurrentView { get; set; } = "";

    /// The X-Request-ID of the last failed API call ("ref ABC123").
    public static string LastRequestId { get; private set; } = "";

    public static string LogsDir => DiagLog.LogsDir;

    public static void Init()
    {
        if (BaseDir.Length > 0) return;
        BaseDir = Path.Combine(ProfilePaths.Folder(Environment.SpecialFolder.LocalApplicationData), "Nexus");
        Version = AppVersion();
        DiagLog.Init(BaseDir, Version);
        AppDomain.CurrentDomain.UnhandledException += (_, e) =>
        {
            if (e.ExceptionObject is Exception ex) RecordCrash(ProblemReport.KindCrash, ex, "AppDomain.UnhandledException");
            DiagLog.Shutdown();
        };
        TaskScheduler.UnobservedTaskException += (_, e) =>
        {
            DiagLog.Error("task", "unobserved task exception", e.Exception);
            // One report per run: a failing poll would otherwise queue dozens.
            if (Interlocked.Exchange(ref s_unobservedRecorded, 1) == 0)
                RecordCrash(ProblemReport.KindError, e.Exception, "TaskScheduler.UnobservedTaskException", logged: true);
            e.SetObserved();
        };
        DiagLog.Info("startup", $"Nexus {Version} starting{(ProfilePaths.Active ? " (profile " + ProfilePaths.Current!.Name + ")" : "")}");
    }

    public static string AppVersion()
    {
        var info = typeof(App).Assembly.GetCustomAttribute<AssemblyInformationalVersionAttribute>()?.InformationalVersion
            ?? typeof(App).Assembly.GetName().Version?.ToString() ?? "unknown";
        var plus = info.IndexOf('+');
        return plus > 0 ? info[..plus] : info;
    }

    public static Dictionary<string, string> Context()
    {
        var context = new Dictionary<string, string>(StringComparer.Ordinal) { ["desktop_version"] = Version, ["channel"] = "desktop" };
        if (CurrentView.Length > 0) context["view"] = CurrentView;
        if (LastRequestId.Length > 0) context["last_request_id"] = LastRequestId;
        return context;
    }

    /// Writes a held crash/error report. Synchronous on purpose: for
    /// AppDomain.UnhandledException this is the last code that runs.
    public static string RecordCrash(string kind, Exception ex, string via, bool logged = false)
    {
        try
        {
            if (!logged) DiagLog.Error("crash", $"{via}: {kind}", ex);
            // Desktop and core lines in time order, newest 200.
            var logs = LogBundle.Merge(DiagLog.Recent().Concat(CoreTail()), ProblemReport.MaxLogLines);
            var report = ProblemReport.FromException(kind, ex, via, logs, Version, Os, Context());
            ReportStore.WriteHeld(BaseDir, report);
            return report.ReportId;
        }
        catch (Exception writeError)
        {
            try { DiagLog.Error("crash", "could not write the crash report", writeError); } catch { }
            return "";
        }
    }

    // The core's recent lines (its own file), so a desktop crash report also
    // shows what the engine was doing.
    private static IEnumerable<string> CoreTail()
    {
        try { return LogBundle.Recent(LogsDir).Where(l => l.Contains(" [core] ", StringComparison.Ordinal)); }
        catch { return []; }
    }

    // ---- nx_call results

    private static readonly Dictionary<string, DateTime> s_recentFailures = new(StringComparer.Ordinal);

    /// Logs a failed nx_call (method and the error, never the arguments) and
    /// remembers its request ID. The same failure is logged once a minute.
    /// <summary>
    /// A message safe to show for raw engine/agent error text: plain words
    /// with a reference id; the raw text is logged under that id.
    /// </summary>
    public static string Friendly(string component, string? raw)
    {
        if (string.IsNullOrWhiteSpace(raw)) return "";
        if (NxErrors.IsUnreachable(raw)) return NxErrors.UnreachableMessage + " (ref " + Connectivity.NoteUnreachable(component, raw) + ")";
        var (message, reference) = NxErrors.Friendly(raw);
        if (reference.Length > 0) DiagLog.Warn(component, $"{raw} [ref {reference}]");
        return message;
    }

    public static void NoteCallFailure(string method, string? error)
    {
        var text = error ?? "unknown error";
        if (RequestId(text) is { Length: > 0 } id) LastRequestId = id;
        var key = method + "\n" + text;
        lock (s_recentFailures)
        {
            var now = DateTime.UtcNow;
            if (s_recentFailures.TryGetValue(key, out var at) && now - at < TimeSpan.FromMinutes(1)) return;
            s_recentFailures[key] = now;
            if (s_recentFailures.Count > 200) s_recentFailures.Clear();
        }
        var level = NxErrors.IsSignedOut(text) ? "auth" : "nx";
        DiagLog.Warn(level, $"{method} failed: {text}");
    }

    /// "… (ref 7K3M9QW2HX4D)" → "7K3M9QW2HX4D".
    public static string RequestId(string? message)
    {
        var match = RefPattern().Match(message ?? "");
        return match.Success ? match.Groups[1].Value : "";
    }

    [GeneratedRegex(@"\(ref ([A-Za-z0-9_-]{6,64})\)")]
    private static partial Regex RefPattern();

    // ---- Sending

    public sealed record SendOutcome(bool Sent, bool Queued, string ReportId, string RequestId, string Error);

    /// Queues a report and asks the core to send it now. signed sends it with
    /// the saved sign-in (when there is one).
    public static async Task<SendOutcome> SendAsync(ProblemReport report, bool signed)
    {
        report.Sanitize();
        try
        {
            ReportStore.WritePending(BaseDir, report, signed);
        }
        catch (Exception ex)
        {
            DiagLog.Error("report", "could not queue a report", ex);
            return new SendOutcome(false, false, report.ReportId, "", ex.Message);
        }
        DiagLog.Info("report", $"queued {report.Kind} report {report.ReportId}{(signed ? " (signed)" : "")}");
        var response = await App.Core.CallAsync("diagnostics.reports.flush", new { report_id = report.ReportId, force = true });
        if (response.Ok && response.Result is { } result && result.TryGetProperty("results", out var list) && list.ValueKind == JsonValueKind.Array)
        {
            foreach (var item in list.EnumerateArray())
            {
                if (NxJson.Text(item, "report_id") != report.ReportId) continue;
                var sent = NxJson.Text(item, "sent") == "true";
                var dropped = NxJson.Text(item, "dropped") == "true";
                return new SendOutcome(sent, !sent && !dropped, report.ReportId, NxJson.Text(item, "request_id"), NxJson.Text(item, "error"));
            }
        }
        // An older core without the uploader: the file stays queued.
        return new SendOutcome(false, true, report.ReportId, "", response.Error ?? "");
    }

    /// Sends what an earlier run queued (the core also does this by itself).
    public static void WakeUploader() => _ = App.Core.CallAsync("diagnostics.reports.wake");

    public static IReadOnlyList<ProblemReport> HeldReports() => BaseDir.Length == 0 ? [] : ReportStore.ReadHeld(BaseDir);

    public static void Release(IEnumerable<ProblemReport> reports, bool signed)
    {
        foreach (var report in reports)
        {
            try { ReportStore.Release(BaseDir, report, signed); }
            catch (Exception ex) { DiagLog.Warn("report", "could not queue a held report", ex); }
        }
        WakeUploader();
    }

    public static void Discard(IEnumerable<ProblemReport> reports)
    {
        foreach (var report in reports) ReportStore.Discard(BaseDir, report.ReportId);
    }

    /// A "Report a problem" report: the note, and optionally the newest 200
    /// log lines from the desktop and the core.
    public static ProblemReport UserReport(string note, bool includeLogs) => new ProblemReport
    {
        Kind = ProblemReport.KindUserReport,
        AppVersion = Version,
        Os = Os,
        Message = "",
        UserNote = note,
        Logs = includeLogs ? LogBundle.Recent(LogsDir) : null,
        Timestamp = DateTimeOffset.UtcNow,
        Context = Context(),
    }.Sanitize();

    public static void OpenLogsFolder()
    {
        try
        {
            Directory.CreateDirectory(LogsDir);
            Process.Start(new ProcessStartInfo { FileName = LogsDir, UseShellExecute = true });
        }
        catch (Exception ex)
        {
            DiagLog.Warn("report", "could not open the logs folder", ex);
        }
    }
}
