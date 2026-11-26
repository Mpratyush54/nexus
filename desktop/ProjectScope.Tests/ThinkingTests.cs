#nullable enable
using System.Text.Json;
using Nexus.Core;
using Xunit;

namespace Nexus.Core.Tests;

public class ThinkingSpanTests
{
    private static readonly DateTimeOffset T0 = new(2026, 11, 26, 9, 0, 0, TimeSpan.Zero);

    [Fact]
    public void AReplayedTurnKeepsTheRealThinkingTime()
    {
        // Reopening a chat replays the whole turn at once, 3 minutes later:
        // thinking ran from T0 to T0+7s, when the reply's text started.
        var now = T0.AddMinutes(3);
        var span = new ThinkingSpan(T0, () => now);
        Assert.True(span.Finish(T0.AddSeconds(7)));
        Assert.Equal(TimeSpan.FromSeconds(7), span.Duration);
        Assert.False(span.Finish(T0.AddSeconds(30))); // only the first finish counts
        Assert.Equal(TimeSpan.FromSeconds(7), span.Duration);
    }

    [Fact]
    public void ALiveRowTicksFromTheFirstThinkingEvent()
    {
        var now = T0.AddSeconds(5);
        var span = new ThinkingSpan(null, () => now);
        span.StartedSince(T0); // the replay says it began earlier
        Assert.Equal(TimeSpan.FromSeconds(5), span.Duration);
        now = now.AddSeconds(2);
        span.Finish();
        Assert.Equal(TimeSpan.FromSeconds(7), span.Duration);
        span.StartedSince(T0.AddSeconds(-30)); // finished: unchanged
        Assert.Equal(TimeSpan.FromSeconds(7), span.Duration);
    }

    [Fact]
    public void AnEndBeforeTheStartIsZero()
    {
        var span = new ThinkingSpan(T0, () => T0);
        span.Finish(T0.AddSeconds(-4));
        Assert.Equal(TimeSpan.Zero, span.Duration);
    }

    [Fact]
    public void EventTimesAreUnixMilliseconds()
    {
        using var doc = JsonDocument.Parse("{\"at\":1795683600000,\"until\":1795683607000,\"bad\":\"x\",\"old\":5}");
        var at = ThinkingSpan.EventTime(doc.RootElement);
        var until = ThinkingSpan.EventTime(doc.RootElement, "until");
        Assert.NotNull(at);
        Assert.Equal(TimeSpan.FromSeconds(7), until!.Value - at!.Value);
        Assert.Null(ThinkingSpan.EventTime(doc.RootElement, "bad"));
        Assert.Null(ThinkingSpan.EventTime(doc.RootElement, "old"));
        Assert.Null(ThinkingSpan.EventTime(doc.RootElement, "missing"));
    }
}

public class TurnStatusThinkingRowTests
{
    [Fact]
    public void TheLineLeavesThinkingToTheRow()
    {
        var now = new DateTimeOffset(2026, 11, 26, 9, 0, 0, TimeSpan.Zero);
        var s = new TurnStatus("Claude Code", now, () => now);
        s.Apply("thinking_delta", "Looking");
        Assert.Equal("Thinking… · 0s · Esc to stop", s.Line());
        s.ThinkingRowShown = true;
        Assert.False(s.ShowsVerb);
        Assert.Equal("Esc to stop", s.Line());
        s.Apply("tool_start", tool: "Bash", toolInput: "{\"command\":\"npm test\"}");
        Assert.True(s.ShowsVerb);
        Assert.StartsWith("Running npm test…", s.Line());
        s.ThinkingRowShown = false;
        s.Apply("tool_end");
        Assert.Equal("Thinking", s.Verb);
        Assert.True(s.ShowsVerb);
    }
}
