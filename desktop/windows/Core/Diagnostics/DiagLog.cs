#nullable enable
using System.Globalization;
using System.Text;
using System.Text.RegularExpressions;

namespace Nexus.Core.Diagnostics;

/// <summary>
/// Day- and size-rotated log files, the same scheme as the Go core's
/// diagnostics.RollingFile: &lt;prefix&gt;-YYYYMMDD.log, moved aside to
/// &lt;prefix&gt;-YYYYMMDD.N.log when it would pass the size cap, and at most
/// <c>maxFiles</c> files per prefix (newest kept).
/// </summary>
internal sealed class RollingLogFile : IDisposable
{
    public const long DefaultMaxBytes = 2L << 20;
    public const int DefaultMaxFiles = 5;

    private readonly object _gate = new();
    private readonly string _dir;
    private readonly string _prefix;
    private readonly long _maxBytes;
    private readonly int _maxFiles;
    private readonly Func<DateTime> _utcNow;
    private readonly Func<string>? _header;
    private FileStream? _stream;
    private string _day = "";
    private long _size;

    public RollingLogFile(string dir, string prefix, long maxBytes = DefaultMaxBytes, int maxFiles = DefaultMaxFiles,
        Func<DateTime>? utcNow = null, Func<string>? header = null)
    {
        _dir = dir;
        _prefix = prefix;
        _maxBytes = maxBytes > 0 ? maxBytes : DefaultMaxBytes;
        _maxFiles = maxFiles > 0 ? maxFiles : DefaultMaxFiles;
        _utcNow = utcNow ?? (() => DateTime.UtcNow);
        _header = header;
    }

    public string Directory => _dir;

    public void Append(string text)
    {
        var bytes = Encoding.UTF8.GetBytes(text);
        lock (_gate)
        {
            var day = _utcNow().ToString("yyyyMMdd", CultureInfo.InvariantCulture);
            if (_stream is not null && (day != _day || _size + bytes.Length > _maxBytes))
            {
                Close(newDay: day != _day);
            }
            if (_stream is null) Open(day);
            _stream!.Write(bytes, 0, bytes.Length);
            _stream.Flush();
            _size += bytes.Length;
        }
    }

    private string CurrentPath(string day) => Path.Combine(_dir, $"{_prefix}-{day}.log");

    private void Open(string day)
    {
        System.IO.Directory.CreateDirectory(_dir);
        var path = CurrentPath(day);
        _stream = new FileStream(path, FileMode.Append, FileAccess.Write, FileShare.ReadWrite | FileShare.Delete);
        _day = day;
        _size = _stream.Length;
        if (_size >= _maxBytes)
        {
            // A full file from an earlier run rolls before anything is added.
            Close(newDay: false);
            _stream = new FileStream(path, FileMode.Append, FileAccess.Write, FileShare.ReadWrite | FileShare.Delete);
            _size = 0;
        }
        Prune(path);
        var header = _header?.Invoke();
        if (!string.IsNullOrEmpty(header))
        {
            var bytes = Encoding.UTF8.GetBytes(header.TrimEnd('\r', '\n') + "\n");
            _stream.Write(bytes, 0, bytes.Length);
            _size += bytes.Length;
        }
    }

    // A full file moves aside to <prefix>-<day>.<n>.log; a new day just
    // closes the old one.
    private void Close(bool newDay)
    {
        _stream?.Dispose();
        _stream = null;
        if (newDay) return;
        var current = CurrentPath(_day);
        for (var n = 1; n < 1000; n++)
        {
            var target = Path.Combine(_dir, $"{_prefix}-{_day}.{n}.log");
            if (File.Exists(target)) continue;
            try { File.Move(current, target); } catch (IOException) { }
            return;
        }
    }

    private void Prune(string current)
    {
        var others = 0;
        foreach (var path in Files(_dir, _prefix))
        {
            if (string.Equals(path, current, StringComparison.OrdinalIgnoreCase)) continue;
            if (++others > _maxFiles - 1)
            {
                try { File.Delete(path); } catch (IOException) { } catch (UnauthorizedAccessException) { }
            }
        }
    }

    /// The prefix's log files, newest first.
    public static IReadOnlyList<string> Files(string dir, string prefix)
    {
        if (!System.IO.Directory.Exists(dir)) return [];
        var pattern = new Regex("^" + Regex.Escape(prefix) + @"-\d{8}(\.\d+)?\.log$", RegexOptions.CultureInvariant);
        return new DirectoryInfo(dir).EnumerateFiles(prefix + "-*.log")
            .Where(f => pattern.IsMatch(f.Name))
            .OrderByDescending(f => f.LastWriteTimeUtc)
            .ThenByDescending(f => f.Name, StringComparer.Ordinal)
            .Select(f => f.FullName)
            .ToList();
    }

    public void Dispose()
    {
        lock (_gate)
        {
            _stream?.Dispose();
            _stream = null;
        }
    }
}

/// <summary>
/// The desktop's log: %LOCALAPPDATA%\Nexus\logs\nexus-YYYYMMDD.log (inside
/// the NEXUS_PROFILE root when one is set), beside the core's core-*.log.
/// Lines match the core's format so a report can merge both by time:
/// <c>2026-11-25T10:41:05.007Z WARN [desktop] sync: upload failed ref=… (t12)</c>.
/// Every line is redacted; callers must still never pass chat or memory text.
/// </summary>
internal static class DiagLog
{
    public const int RecentLines = 200;
    private const int MaxLineChars = 1000;

    private static readonly object Gate = new();
    private static readonly LinkedList<string> Ring = new();
    private static RollingLogFile? s_file;

    public static string BaseDir { get; private set; } = "";
    public static string LogsDir => Path.Combine(BaseDir, "logs");
    public static string AppVersion { get; private set; } = "";

    public static void Init(string baseDir, string appVersion)
    {
        lock (Gate)
        {
            if (s_file is not null) return;
            BaseDir = baseDir;
            AppVersion = appVersion;
            s_file = new RollingLogFile(LogsDir, "nexus", header: () =>
                Format(DateTime.UtcNow, "INFO", "log", $"Nexus {appVersion}, {OsDescription()}, pid {Environment.ProcessId}"));
        }
    }

    public static void Info(string component, string message) => Write("INFO", component, message, null);
    public static void Warn(string component, string message, Exception? ex = null) => Write("WARN", component, message, ex);
    public static void Error(string component, string message, Exception? ex = null) => Write("ERROR", component, message, ex);

    /// The newest lines written by this process, oldest first.
    public static string[] Recent()
    {
        lock (Gate) return Ring.ToArray();
    }

    public static void Write(string level, string component, string message, Exception? ex)
    {
        var text = ex is null ? message : $"{message}: {ex.GetType().Name}: {ex.Message}";
        var lines = new List<string> { Format(DateTime.UtcNow, level, component, text) };
        if (ex is not null && level == "ERROR")
        {
            // The stack goes on its own lines (same prefix) so the file stays
            // one-record-per-line for merging and for the 1000-char cap.
            foreach (var frame in ex.ToString().Split('\n').Skip(1).Take(40))
            {
                var trimmed = frame.TrimEnd('\r');
                if (trimmed.Trim().Length > 0) lines.Add(Format(DateTime.UtcNow, level, component, "  " + trimmed.Trim()));
            }
        }
        lock (Gate)
        {
            foreach (var line in lines)
            {
                Ring.AddLast(line);
                while (Ring.Count > RecentLines) Ring.RemoveFirst();
            }
            try
            {
                s_file?.Append(string.Join("\n", lines) + "\n");
            }
            catch (IOException) { }
            catch (UnauthorizedAccessException) { }
        }
    }

    public static string Format(DateTime utc, string level, string component, string message)
    {
        var line = utc.ToUniversalTime().ToString("yyyy-MM-dd'T'HH:mm:ss.fff'Z'", CultureInfo.InvariantCulture) +
            " " + level + " [desktop] " + component + ": " + Redactor.Redact(message.Replace("\r", " ").Replace("\n", " ")) +
            " (t" + Environment.CurrentManagedThreadId + ")";
        return line.Length <= MaxLineChars ? line : line[..MaxLineChars] + "…";
    }

    public static string OsDescription()
    {
        var v = Environment.OSVersion.Version;
        var name = v.Major == 10 && v.Build >= 22000 ? "Windows 11" : v.Major == 10 ? "Windows 10" : "Windows";
        return $"{name} {v.Major}.{v.Minor}.{v.Build} {System.Runtime.InteropServices.RuntimeInformation.OSArchitecture.ToString().ToLowerInvariant()}";
    }

    public static void Shutdown()
    {
        lock (Gate)
        {
            s_file?.Dispose();
            s_file = null;
        }
    }
}
