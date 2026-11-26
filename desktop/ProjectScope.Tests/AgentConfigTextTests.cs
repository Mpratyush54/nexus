using Nexus.Core;
using Xunit;

namespace Nexus.Core.Tests;

public class AgentConfigTextTests
{
    [Fact]
    public void NewlinesNormalizesTextBoxBreaks()
    {
        Assert.Equal("a\nb\nc", AgentConfigText.Newlines("a\rb\r\nc"));
        Assert.False(AgentConfigText.Differs("a\nb", "a\rb"));
        Assert.True(AgentConfigText.Differs("a\nb", "a\rc"));
    }

    [Fact]
    public void LinesDropsBlanksAndTrims()
    {
        Assert.Equal(new[] { "-y", "@pkg/server", "C:/my folder" }, AgentConfigText.Lines("-y\r\r  @pkg/server \rC:/my folder\r"));
    }

    [Fact]
    public void PairsReadsEnvAndHeadersAndFlagsBadLines()
    {
        var env = AgentConfigText.Pairs("API_KEY=abc=def\r# comment\r\rDEBUG = 1", '=', out var bad);
        Assert.Equal(0, bad);
        Assert.Equal("abc=def", env["API_KEY"]);
        Assert.Equal("1", env["DEBUG"]);

        var headers = AgentConfigText.Pairs("Authorization: Bearer x:y\rbroken line", ':', out bad);
        Assert.Equal(2, bad);
        Assert.Equal("Bearer x:y", headers["Authorization"]);
    }

    [Fact]
    public void FormatPairsRoundTrips()
    {
        var map = new Dictionary<string, string> { ["b"] = "2", ["A"] = AgentConfigText.Mask };
        var text = AgentConfigText.FormatPairs(map, '=');
        Assert.Equal("A=" + AgentConfigText.Mask + "\nb=2", text);
        Assert.Equal(map, AgentConfigText.Pairs(text, '=', out _));
        Assert.Equal("Authorization: x", AgentConfigText.FormatPairs(new Dictionary<string, string> { ["Authorization"] = "x" }, ':'));
    }

    [Fact]
    public void ManagedNoteIsPlain()
    {
        Assert.Equal("", AgentConfigText.ManagedNote(Array.Empty<(int, int, string)>()));
        Assert.Equal("Lines 3–5 (Project memory). Nexus updates this section by itself, so edit outside it.",
            AgentConfigText.ManagedNote(new[] { (3, 5, "Project memory") }));
        Assert.Contains("these sections", AgentConfigText.ManagedNote(new[] { (1, 1, "a"), (4, 6, "b") }));
        Assert.StartsWith("Line 1 (a)", AgentConfigText.ManagedNote(new[] { (1, 1, "a"), (4, 6, "b") }));
    }

    [Fact]
    public void AgentListReadsNaturally()
    {
        Assert.Equal("Codex", AgentConfigText.AgentList(new[] { "Codex" }));
        Assert.Equal("Codex and Cursor", AgentConfigText.AgentList(new[] { "Codex", "Cursor" }));
        Assert.Equal("Claude Code, Codex and Cursor", AgentConfigText.AgentList(new[] { "Claude Code", "Codex", "Cursor" }));
    }
}
