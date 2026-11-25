#nullable enable
using Nexus.Core.Diagnostics;
using Xunit;

namespace Nexus.Core.Tests;

// Shared vectors with internal/diagnostics/diagnostics_test.go: the C#
// port must redact exactly what the server would.
public class RedactorTests
{
    private const string Jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2lnbmF0dXJlLXZhbHVl";

    public static TheoryData<string, string[], string[]> Vectors => new()
    {
        { "Authorization: Bearer abcdef1234567890", ["abcdef1234567890"], ["[redacted]"] },
        { "token nxs_AbC123xyz_-987 leaked", ["AbC123xyz"], ["nxs_[redacted]", "leaked"] },
        { "OPENAI sk-proj-abcdefghijklmnop1234 and sk-ant-api03-zzzzzzzzzzzzzzzz", ["abcdefghijklmnop", "zzzzzzzzzzzz"], ["sk-[redacted]"] },
        { "jwt=" + Jwt, ["eyJzdWIiOiJhbGljZSJ9"], [] },
        { "session " + Jwt + " expired", ["c2lnbmF0dXJl"], ["session [redacted] expired"] },
        { "mail alice.smith+x@example.co.uk now", ["alice", "example.co.uk"], ["[email]"] },
        { @"open C:\Users\pratyush\AppData\Local\nexus\log.txt failed", ["pratyush"], [@"~\AppData\Local\nexus\log.txt failed"] },
        { @"""path"":""C:\\Users\\pratyush\\.nexus\\config.json""", ["pratyush"], [@"~\\.nexus"] },
        { "at /Users/alice/src/app.go:12", ["alice"], ["~/src/app.go:12"] },
        { "/home/bob/.config/nexus", ["bob"], ["~/.config/nexus"] },
        { "file:///C:/Users/carol/x.ts", ["carol"], ["~/x.ts"] },
        { "GET https://x.dev/cb?code=abc123&state=ok", ["abc123"], ["state=ok"] },
        { @"{""api_key"": ""verysecretvalue""}", ["verysecretvalue"], [@"""api_key"": ""[redacted]"] },
        { "password=hunter22 user=x", ["hunter22"], ["user=x"] },
        { "ghp_abcdefghijklmnopqrstuvwxyz0123 AKIAABCDEFGHIJKLMNOP", ["ghp_abc", "AKIAABCD"], [] },
        { "https://bob:pa55w0rd@host/repo.git", ["pa55w0rd", "bob:"], ["https://[redacted]@host"] },
        { "route /app/home/settings stays", [], ["/app/home/settings"] },
    };

    [Theory]
    [MemberData(nameof(Vectors))]
    public void RedactsLikeTheServer(string input, string[] gone, string[] mustSee)
    {
        var got = Redactor.Redact(input);
        foreach (var g in gone) Assert.DoesNotContain(g, got);
        foreach (var m in mustSee) Assert.Contains(m, got);
        Assert.Equal(got, Redactor.Redact(got));
    }

    [Fact]
    public void WindowsUserNameWithSpaceLeaksNothing()
    {
        Assert.Equal(@"~\x", Redactor.Redact(@"C:\Users\Pratyush Mishra\x"));
    }

    [Fact]
    public void LogLinesAreRedactedAndFormatted()
    {
        var line = DiagLog.Format(new DateTime(2026, 11, 25, 10, 41, 5, 7, DateTimeKind.Utc), "WARN", "sync",
            "upload failed token=nxs_abcdefghijkl for bob@example.com\nsecond line");
        Assert.StartsWith("2026-11-25T10:41:05.007Z WARN [desktop] sync: upload failed", line);
        Assert.DoesNotContain("abcdefghijkl", line);
        Assert.DoesNotContain("bob@", line);
        Assert.DoesNotContain("\n", line);
        Assert.Matches(@"\(t\d+\)$", line);
    }
}

public class RollingLogFileTests : IDisposable
{
    private readonly string _dir = Path.Combine(Path.GetTempPath(), "nexus-log-test-" + Guid.NewGuid().ToString("N"));

    public void Dispose()
    {
        try { Directory.Delete(_dir, recursive: true); } catch (IOException) { }
    }

    [Fact]
    public void RotatesBySizeKeepsFiveAndStartsEachFileWithTheHeader()
    {
        var now = new DateTime(2026, 11, 25, 10, 0, 0, DateTimeKind.Utc);
        using var log = new RollingLogFile(_dir, "nexus", maxBytes: 100, maxFiles: 5, utcNow: () => now, header: () => "header v1");
        var line = new string('x', 39) + "\n";
        for (var i = 0; i < 20; i++)
        {
            log.Append(line);
            Thread.Sleep(2); // distinct write times so "newest" is well defined
        }
        var files = RollingLogFile.Files(_dir, "nexus");
        Assert.Equal(5, files.Count);
        Assert.Equal("nexus-20261125.log", Path.GetFileName(files[0]));
        foreach (var file in files)
        {
            Assert.True(new FileInfo(file).Length <= 100, file + " is over the cap");
            Assert.StartsWith("header v1\n", ReadShared(file));
        }

        // A new UTC day starts a new file; the core's files are left alone.
        File.WriteAllText(Path.Combine(_dir, "core-20261120.log"), "core\n");
        now = now.AddDays(1);
        log.Append(line);
        Assert.True(File.Exists(Path.Combine(_dir, "nexus-20261126.log")));
        Assert.Equal(5, RollingLogFile.Files(_dir, "nexus").Count);
        Assert.True(File.Exists(Path.Combine(_dir, "core-20261120.log")));
    }

    [Fact]
    public void LogBundleMergesDesktopAndCoreByTime()
    {
        Directory.CreateDirectory(_dir);
        File.WriteAllLines(Path.Combine(_dir, "nexus-20261125.log"),
        [
            "2026-11-25T10:00:00.000Z INFO [desktop] startup: one (t1)",
            "2026-11-25T10:00:02.000Z WARN [desktop] nx: sessions.list failed ref=AAAAAAAA1111 (t5)",
        ]);
        File.WriteAllLines(Path.Combine(_dir, "core-20261125.log"),
        [
            "2026-11-25T10:00:01.000Z INFO [core] nexuscore: started",
            "2026-11-25T10:00:03.000Z WARN [core] cloud: GET /v1/x -> 500 ref=AAAAAAAA1111: boom for eve@example.com",
        ]);
        var lines = LogBundle.Recent(_dir, 3);
        Assert.Equal(3, lines.Count);
        Assert.Contains("[core] nexuscore: started", lines[0]);
        Assert.Contains("[desktop] nx:", lines[1]);
        Assert.Contains("[core] cloud:", lines[2]);
        Assert.DoesNotContain("eve@", lines[2]);
    }

    private static string ReadShared(string path)
    {
        using var stream = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete);
        using var reader = new StreamReader(stream);
        return reader.ReadToEnd();
    }
}

public class ProblemReportTests : IDisposable
{
    private readonly string _base = Path.Combine(Path.GetTempPath(), "nexus-report-test-" + Guid.NewGuid().ToString("N"));

    public void Dispose()
    {
        try { Directory.Delete(_base, recursive: true); } catch (IOException) { }
    }

    private static Exception Thrown()
    {
        try
        {
            try { throw new InvalidOperationException(@"inner at C:\Users\Pratyush Mishra\repo with Bearer abcdefghijklmnop"); }
            catch (Exception inner) { throw new ApplicationException("outer for bob@example.com", inner); }
        }
        catch (Exception ex) { return ex; }
    }

    [Fact]
    public void CrashFileRoundTripsRedactedAndCapped()
    {
        var logs = Enumerable.Range(0, 250).Select(i => $"2026-11-25T10:00:00.000Z INFO [desktop] x: line {i} sk-abcdefghijklmnopqrst").ToList();
        var report = ProblemReport.FromException(ProblemReport.KindCrash, Thrown(), "AppDomain.UnhandledException", logs,
            "0.4.0-beta.3", "Windows 11 10.0.26300 x64", new Dictionary<string, string> { ["view"] = "memory" });

        var path = ReportStore.WriteHeld(_base, report);
        Assert.Equal(report.ReportId + ".json", Path.GetFileName(path));
        var held = ReportStore.ReadHeld(_base);
        var back = Assert.Single(held);

        Assert.Equal(report.ReportId, back.ReportId);
        Assert.Equal("crash", back.Kind);
        Assert.Equal("desktop", back.Source);
        Assert.StartsWith("System.ApplicationException: outer for [email]", back.Message);
        Assert.Contains("InvalidOperationException", back.Stack); // inner exception kept
        Assert.DoesNotContain("Pratyush", back.Stack);
        Assert.DoesNotContain("abcdefghijklmnop", back.Stack);
        Assert.Equal(ProblemReport.MaxLogLines, back.Logs!.Count);
        Assert.EndsWith("line 249 sk-[redacted]", back.Logs[^1]); // newest kept
        Assert.Equal("AppDomain.UnhandledException", back.Context!["via"]);
        Assert.Equal("memory", back.Context["view"]);
        Assert.Equal(back.ToJson(), back.Sanitize().ToJson()); // idempotent

        // Send moves it to the anonymous or signed upload queue; dedupe by id.
        ReportStore.Release(_base, back, signed: true);
        Assert.Empty(ReportStore.ReadHeld(_base));
        ReportStore.WritePending(_base, back, signed: false);
        var pending = Directory.GetFiles(ReportStore.PendingDir(_base));
        Assert.Equal(back.ReportId + ".json", Path.GetFileName(Assert.Single(pending)));
    }

    [Fact]
    public void WireFieldNamesMatchTheServer()
    {
        var json = new ProblemReport { ReportId = "abcdefgh1234", Kind = "user_report", UserNote = "hi", AppVersion = "1" }.Sanitize().ToJson();
        foreach (var key in new[] { "\"report_id\"", "\"kind\"", "\"source\":\"desktop\"", "\"app_version\"", "\"user_note\"" })
            Assert.Contains(key, json);
        Assert.DoesNotContain("\"stack\"", json); // empty fields are omitted
    }

    [Fact]
    public void UnreadableHeldFilesAreDropped()
    {
        Directory.CreateDirectory(ReportStore.HeldDir(_base));
        File.WriteAllText(Path.Combine(ReportStore.HeldDir(_base), "broken.json"), "{not json");
        Assert.Empty(ReportStore.ReadHeld(_base));
        Assert.Empty(Directory.GetFiles(ReportStore.HeldDir(_base)));
    }
}

public class MarkdownTextTests
{
    [Fact]
    public void BriefBecomesOnePlainLine()
    {
        var plain = MarkdownText.Plain("# Project brief\n\n## Key decisions\n- Use **Postgres** for `sessions`\n- See [ADR 7](https://x/adr/7)\n---\n| a | b |\n|---|---|\n| 1 | 2 |");
        Assert.Equal("Project brief · Key decisions · Use Postgres for sessions · See ADR 7 · a b · 1 2", plain);
    }

    [Fact]
    public void SnakeCaseAndPlainTextSurvive()
    {
        Assert.Equal("call memory_search_tool now", MarkdownText.Plain("call memory_search_tool now"));
        Assert.False(MarkdownText.LooksLikeMarkdown("Prefer tabs over spaces."));
        Assert.True(MarkdownText.LooksLikeMarkdown("# Project brief\n- x"));
    }
}
