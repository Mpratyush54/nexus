using Nexus.Core;
using Xunit;

public class SubagentTranscriptTests
{
    // Shapes copied from a real Claude Code sub-agent transcript (texts shortened).
    private static readonly string[] Lines =
    [
        """{"parentUuid":null,"isSidechain":true,"agentId":"a02f","type":"user","message":{"role":"user","content":"Check the new 3D Graph view"},"timestamp":"2026-11-23T09:42:22.493Z"}""",
        """{"parentUuid":"x","isSidechain":true,"type":"attachment","timestamp":"2026-11-23T09:42:22.517Z"}""",
        """{"parentUuid":"x","isSidechain":true,"type":"user","message":{"role":"user","content":"<system-reminder>\nnoise"},"timestamp":"2026-11-23T09:42:22.518Z"}""",
        """{"type":"api-request","body":"{\"type\":\"user\"}"}""",
        """{"parentUuid":"y","isSidechain":true,"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Looking at the graph."},{"type":"tool_use","name":"Bash","input":{}},{"type":"tool_use","name":"Bash","input":{}},{"type":"tool_use","name":"Read","input":{}}]},"timestamp":"2026-11-23T09:43:00Z"}""",
        """{"parentUuid":"z","isSidechain":true,"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]},"timestamp":"2026-11-23T09:43:01Z"}""",
        """{"parentUuid":"w","isSidechain":true,"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"SubagentHandback","input":{}}]},"timestamp":"2026-11-23T13:37:01.298Z"}""",
        "not json",
    ];

    [Fact]
    public void ReadsOnlyMessages()
    {
        var (turns, total) = SubagentTranscript.Read(Lines, 50);
        Assert.Equal(3, total);
        Assert.Equal(new[] { "user", "assistant", "assistant" }, turns.Select(t => t.Role));
        Assert.Equal("Check the new 3D Graph view", turns[0].Text);
        Assert.Equal("Bash ×2, Read", SubagentTranscript.ToolSummary(turns[1].Tools));
        Assert.Equal(new DateTimeOffset(2026, 11, 23, 13, 37, 1, 298, TimeSpan.Zero), turns[2].At);
    }

    [Fact]
    public void CapKeepsTheLastMessage()
    {
        var (turns, total) = SubagentTranscript.Read(Lines, 1);
        Assert.Equal(3, total);
        Assert.Equal(2, turns.Count);
        Assert.Equal("SubagentHandback", turns[^1].Tools.Single());
    }

    [Fact]
    public void StartIsTheFirstTimestamp() =>
        Assert.Equal(new DateTimeOffset(2026, 11, 23, 9, 42, 22, 493, TimeSpan.Zero), SubagentTranscript.FirstTimestamp(Lines));

    [Theory]
    [InlineData(42, "42s")]
    [InlineData(185, "3m 05s")]
    [InlineData(4320, "1h 12m")]
    [InlineData(-5, "0s")]
    public void FormatsDurations(int seconds, string expected) => Assert.Equal(expected, SubagentTranscript.Duration(TimeSpan.FromSeconds(seconds)));
}

public class SubagentTranscriptSpacingTests
{
    [Fact]
    public void SpacedJsonIsReadToo()
    {
        var line = """{"type": "assistant", "message": {"role": "assistant", "content": [{"type": "text", "text": "done"}]}, "timestamp": "2026-11-23T09:43:00Z"}""";
        Assert.Equal("done", SubagentTranscript.ParseLine(line)?.Text);
        Assert.Equal(new DateTimeOffset(2026, 11, 23, 9, 43, 0, TimeSpan.Zero), SubagentTranscript.FirstTimestamp([line]));
    }
}

public class SubagentTranscriptTailTests
{
    [Fact]
    public void LastTimestampIsTheLatestInTheTail() =>
        Assert.Equal(new DateTimeOffset(2026, 11, 23, 13, 37, 1, 476, TimeSpan.Zero),
            SubagentTranscript.LastTimestamp("partial line\",\"timestamp\":\"2026-11-23T13:37:01.298Z\"}\n{\"type\":\"user\",\"timestamp\":\"2026-11-23T13:37:01.476Z\"}\n"));
}
