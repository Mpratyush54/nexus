using Nexus.Core;
using Xunit;


public class DefaultProjectTests
{
    private static readonly HashSet<string> Known = new(StringComparer.OrdinalIgnoreCase) { "central", "pathlab", "stale" };

    [Fact]
    public void WorkspaceFolderWins()
    {
        var folders = new Dictionary<string, string> { ["central"] = @"D:\cm-ui\", ["pathlab"] = @"D:\pathlab" };
        Assert.Equal("central", DefaultProject.Pick(new[] { "pathlab" }, folders, "d:/cm-ui", Known));
    }

    [Fact]
    public void NewestChatProjectBeatsStalePreference()
    {
        Assert.Equal("pathlab", DefaultProject.Pick(new[] { "", "pathlab", "central" }, new Dictionary<string, string>(), "", Known));
    }

    [Fact]
    public void UnknownProjectsAreSkipped()
    {
        Assert.Equal("central", DefaultProject.Pick(new[] { "deleted", "central" }, new Dictionary<string, string>(), "", Known));
    }

    [Fact]
    public void NothingToGoOnKeepsCurrentChoice()
    {
        Assert.Equal("", DefaultProject.Pick(Array.Empty<string>(), new Dictionary<string, string> { ["gone"] = @"D:\x" }, @"D:\x", Known));
    }
}
