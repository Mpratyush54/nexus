using Nexus.Core;
using Xunit;

public class TagTextTests
{
    [Theory]
    [InlineData("  #Bug Fix ", "bug-fix")]
    [InlineData("release/1.2", "release/1.2")]
    [InlineData("###", "")]
    [InlineData("a  --  b", "a-b")]
    public void NormalizesTypedTags(string raw, string expected) => Assert.Equal(expected, TagText.Normalize(raw));

    [Fact]
    public void ParseSplitsAndDedupes() =>
        Assert.Equal(new[] { "ui", "perf" }, TagText.Parse("UI, perf;ui,, "));

    [Fact]
    public void LongTagsAreCut() => Assert.Equal(TagText.MaxLength, TagText.Normalize(new string('x', 80)).Length);

    [Fact]
    public void SavingUserTagsKeepsProvenanceTags()
    {
        var current = new[] { "session:abc", "source:claude", "old", "pinned" };
        var saved = TagText.WithUserTags(current, new[] { "New Tag", "session:evil", "pinned" });
        Assert.Equal(new[] { "session:abc", "source:claude", "pinned", "new-tag" }, saved);
    }
}
