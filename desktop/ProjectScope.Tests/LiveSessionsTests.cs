#nullable enable
using System.Text.Json;
using Xunit;

namespace Nexus.Core.Tests;

public class LiveSessionsTests
{
    private static string Status(string id, string status, bool unread = false, int running = 0, int queued = 0, int approvals = 0, string summary = "", string agent = "claude", string title = "Fix tests") =>
        JsonSerializer.Serialize(new Dictionary<string, object>
        {
            ["type"] = "live.status", ["session_id"] = id, ["status"] = status, ["unread"] = unread, ["agent"] = agent,
            ["title"] = title, ["pending"] = approvals, ["approval_summary"] = summary, ["error"] = "",
            ["running"] = running, ["queued"] = queued, ["approval_count"] = approvals, ["followups"] = 0
        });

    [Fact]
    public void TracksStatusCountsAndIndicators()
    {
        var live = new LiveSessions();
        var seen = new List<(LiveSessions.ChatStatus? Before, LiveSessions.ChatStatus After)>();
        live.Changed += (b, a) => seen.Add((b, a));
        Assert.False(live.Apply("{\"type\":\"live.event\",\"session_id\":\"a\"}"));
        Assert.True(live.Apply(Status("a", "running", running: 1)));
        Assert.True(live.Apply(Status("b", "approval", running: 2, approvals: 1, summary: "Run `npm test`")));
        Assert.True(live.Apply(Status("c", "queued", running: 2, queued: 1, approvals: 1)));
        Assert.Equal(LiveSessions.Indicator.Running, live.IndicatorFor("a"));
        Assert.Equal(LiveSessions.Indicator.Approval, live.IndicatorFor("b"));
        Assert.Equal(LiveSessions.Indicator.Queued, live.IndicatorFor("c"));
        Assert.Equal(LiveSessions.Indicator.None, live.IndicatorFor("zzz"));
        Assert.Equal(3, live.Working);
        Assert.Equal("2 agents working · 1 waiting · 1 needs you", live.Summary());
        live.Apply(Status("a", "done", unread: true, running: 1));
        Assert.Equal(LiveSessions.Indicator.Unread, live.IndicatorFor("a"));
        live.Apply(Status("a", "idle", running: 1));
        Assert.Equal(LiveSessions.Indicator.None, live.IndicatorFor("a"));
        Assert.Equal(5, seen.Count);
        Assert.Equal("running", seen[3].Before?.Status);
    }

    [Fact]
    public void LoadReplacesAndClearsMissing()
    {
        var live = new LiveSessions();
        live.Apply(Status("old", "running", running: 1));
        using var doc = JsonDocument.Parse("{\"sessions\":[{\"session_id\":\"x\",\"status\":\"interrupted\",\"unread\":true,\"agent\":\"codex\"}],\"running\":0,\"queued\":0,\"approval\":0}");
        live.Load(doc.RootElement);
        Assert.Equal(LiveSessions.Indicator.Interrupted, live.IndicatorFor("x"));
        Assert.Null(live.Get("old"));
        Assert.Equal(0, live.Working);
        Assert.Equal("", live.Summary());
    }

    [Fact]
    public void NotifiesOnlyWhenNotFocusedAndOnTransitions()
    {
        var running = new LiveSessions.ChatStatus("a", "running", false, "claude", "Fix tests", 0, "", "", 0);
        var done = running with { Status = "done", Unread = true };
        var approval = running with { Status = "approval", ApprovalSummary = "Run `npm test`" };
        var failed = running with { Status = "error", Unread = true, Error = "Claude Code exited" };

        Assert.Null(LiveSessions.NotificationFor(running, done, windowFocused: true, "all"));
        var finished = LiveSessions.NotificationFor(running, done, windowFocused: false, "all");
        Assert.Equal(("Claude Code finished", "Fix tests"), finished);
        var needs = LiveSessions.NotificationFor(running, approval, false, "all");
        Assert.Equal("Claude Code needs your approval", needs?.Title);
        Assert.Equal("Run `npm test` · Fix tests", needs?.Body);
        // Approval reminders do not repeat while it still waits.
        Assert.Null(LiveSessions.NotificationFor(approval, approval with { Pending = 2 }, false, "all"));
        Assert.Equal("Claude Code stopped", LiveSessions.NotificationFor(running, failed, false, "all")?.Title);
        // Preferences.
        Assert.Null(LiveSessions.NotificationFor(running, done, false, "approvals"));
        Assert.NotNull(LiveSessions.NotificationFor(running, approval, false, "approvals"));
        Assert.Null(LiveSessions.NotificationFor(running, approval, false, "off"));
        // Marking a finished chat seen is not news.
        Assert.Null(LiveSessions.NotificationFor(done, done with { Status = "idle", Unread = false }, false, "all"));
        Assert.Equal("2 agents are still working", LiveSessions.QuitQuestion(2));
        Assert.Equal("An agent is still working", LiveSessions.QuitQuestion(1));
    }
}
