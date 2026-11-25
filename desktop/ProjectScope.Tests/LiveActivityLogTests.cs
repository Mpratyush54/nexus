using Xunit;

namespace Nexus.Core.Tests;

public class LiveActivityLogTests
{
    [Fact]
    public void CursorFileDiffIsRetainedThroughRunningAndCompletedEvents()
    {
        var log = new LiveActivityLog();
        Assert.True(log.Apply("""{"id":"cursor-edit","tool":"edit","status":"running","files":["src/app.cs"]}"""));
        Assert.True(log.Apply("""{"id":"cursor-edit","tool":"edit","status":"completed","files":["src/app.cs"],"diff":"@@ -1 +1 @@\n-old\n+new"}"""));
        Assert.Contains("+new", Assert.Single(log.Recent).Diff);
        Assert.Equal(1, log.Count);
        Assert.True(Assert.Single(log.Recent).HasDetails);
    }

    [Fact]
    public void ToolUpdatesReplaceStateWithoutDuplicatingRows()
    {
        var log = new LiveActivityLog();
        Assert.True(log.Apply("""{"id":"p1","tool":"edit","status":"running","files":["app.cs"]}"""));
        Assert.Contains("1 running", log.Summary);
        Assert.False(log.Apply("""{"id":"p1","tool":"edit","status":"running","files":["app.cs"]}"""));
        Assert.True(log.Apply("""{"id":"p1","tool":"edit","status":"completed","files":["app.cs"]}"""));
        Assert.Equal(1, log.Count);
        Assert.Equal("Completed", Assert.Single(log.Recent).Label);
        Assert.DoesNotContain("running", log.Summary);
    }

    [Theory]
    [InlineData("garbage")]
    [InlineData("[]")]
    [InlineData("null")]
    [InlineData("""{"id":"p1","tool":"edit","status":"invented"}""")]
    [InlineData("""{"id":"p1","status":"running"}""")]
    public void InvalidEventsDoNotBecomeChatText(string json)
    {
        var log = new LiveActivityLog();
        Assert.False(log.Apply(json));
        Assert.Equal(0, log.Count);
    }

    [Fact]
    public void ToolDetailsAndSameStatusOutputUpdatesAreRetained()
    {
        var log = new LiveActivityLog();
        Assert.True(log.Apply("""{"id":"p1","tool":"bash","status":"completed","command":"go test ./...","output":"first\nsecond","exit_code":0,"duration_ms":1250}"""));
        var item = Assert.Single(log.Recent);
        Assert.Equal("go test ./...", item.Preview);
        Assert.Equal("first\nsecond", item.Output);
        Assert.Equal(0, item.ExitCode);
        Assert.Equal(1250, item.DurationMS);
        Assert.True(item.HasDetails);
        Assert.True(log.Apply("""{"id":"p1","tool":"bash","status":"completed","command":"go test ./...","output":"new result","exit_code":1}"""));
        Assert.Equal("new result", Assert.Single(log.Recent).Output);
        Assert.Equal(1, log.Count);
    }

    [Fact]
    public void MalformedOptionalDetailsDoNotCrashProgress()
    {
        var log = new LiveActivityLog();
        Assert.True(log.Apply("""{"id":"p1","tool":"bash","status":"running","exit_code":null,"duration_ms":"unknown","files":[null,123,"app.cs"]}"""));
        var item = Assert.Single(log.Recent);
        Assert.Null(item.ExitCode);
        Assert.Equal(0, item.DurationMS);
        Assert.Equal("app.cs", Assert.Single(item.Files));
    }
}
