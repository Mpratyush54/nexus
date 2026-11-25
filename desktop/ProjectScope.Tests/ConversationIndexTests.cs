using Nexus.Core;
using Xunit;

public class ConversationIndexTests
{
    [Fact]
    public void SearchFindsBothSidesAcrossOldAndNewPages()
    {
        var index = new ConversationIndex();
        index.Add(new("turn:1", "user", "Original question about shipping", "", 0, 1));
        index.Add(new("turn:2", "assistant", "The deployment uses a blue harbor", "", 0, 2));
        index.Add(new("turn:500", "user", "Later question", "", 8, 500));
        index.Add(new("tool:3", "tool", "blue harbor", "", 0, 3));
        var found = index.Search("BLUE HARBOR");
        Assert.Single(found);
        Assert.Equal(0, found[0].Page);
        Assert.Equal("assistant", found[0].Role);
        Assert.Equal(2, index.Prompts.Count);
        Assert.Equal("turn:1", index.Prompts[0].Key);
    }

    [Fact]
    public void ReindexingSavedTurnsDoesNotDuplicatePrompts()
    {
        var index = new ConversationIndex();
        index.Add(new("turn:22", "user", "same prompt", "", -1, 22));
        index.Add(new("turn:22", "user", "same prompt", "", 4, 22));
        Assert.Single(index.Prompts);
        Assert.Equal(4, index.Prompts[0].Page);
    }

    [Fact]
    public void SnippetCentersOnLiteralMatchInsteadOfTruncatingIt()
    {
        var text = new string('x', 600) + " unique 100% phrase " + new string('z', 600);
        Assert.Contains("unique 100% phrase", ConversationIndex.Preview(text, "unique 100% phrase"));
    }
}

public class TurnIndexTests
{
    // A saved Nexus reply finds its rendered turn even when an older core
    // rounded the cloud index through a double (…891 shown as …900).
    [Fact]
    public void FindsReplyByExactOrDoubleRoundedIndex()
    {
        var keys = new[] { "turn:179533922286017900:user", "turn:179533922286017900", "turn:17294237" };
        Assert.Equal("turn:179533922286017900", TurnIndex.FindReplyKey(keys, "179533922286017891"));
        Assert.Equal("turn:17294237", TurnIndex.FindReplyKey(keys, "17294237"));
        Assert.Null(TurnIndex.FindReplyKey(keys, "179533922286019000"));
        Assert.Null(TurnIndex.FindReplyKey(new[] { "turn:179533922286017900:user" }, "179533922286017891"));
        Assert.Null(TurnIndex.FindReplyKey(keys, "0"));
    }
}
