#nullable enable
using Nexus.Core;
using Xunit;

public class DictationTextTests
{
    [Theory]
    [InlineData("", "hello world", "Hello world")]
    [InlineData("Fix the bug", "in the parser", " in the parser")]
    [InlineData("Fix the bug.", "now add tests", " Now add tests")]
    [InlineData("Fix the bug. ", "now add tests", "Now add tests")]
    [InlineData("Line one\n", "second line", "Second line")]
    [InlineData("Is it done", "?", "?")]
    [InlineData("Wait", ", really", ", really")]
    [InlineData("call (", "maybe", "maybe")]
    [InlineData("Ends with space ", "more words", "more words")]
    [InlineData("anything", "   ", "")]
    [InlineData("", "iPhone app", "iPhone app")]
    [InlineData("", "macOS build", "macOS build")]
    [InlineData("", "API keys", "API keys")]
    public void InsertionAddsSpacingAndCapital(string before, string dictated, string expected)
    {
        Assert.Equal(expected, DictationText.Insertion(before, "", dictated));
    }

    [Theory]
    [InlineData("tests", "add more", "add more ")]
    [InlineData(" tests", "add more", "add more")]
    [InlineData(".", "add more", "add more")]
    public void InsertionSpacesBeforeFollowingWord(string after, string dictated, string expected)
    {
        Assert.Equal(expected, DictationText.Insertion("Please ", after, dictated));
    }

    [Fact]
    public void MergeInsertsAtCaretAndReportsCaret()
    {
        var (text, caret) = DictationText.Merge("Refactor  please", 9, 0, "the login page");
        Assert.Equal("Refactor the login page please", text);
        Assert.Equal("Refactor the login page".Length, caret);
    }

    [Fact]
    public void MergeReplacesSelection()
    {
        var (text, caret) = DictationText.Merge("Rename foo to bar", 7, 3, "the helper");
        Assert.Equal("Rename the helper to bar", text);
        Assert.Equal(17, caret);
    }

    [Fact]
    public void MergeAppendsToTypedText()
    {
        var (text, caret) = DictationText.Merge("Hi.", 3, 0, "can you help");
        Assert.Equal("Hi. Can you help", text);
        Assert.Equal(text.Length, caret);
    }

    [Fact]
    public void MergeWithNothingDictatedKeepsText()
    {
        var (text, caret) = DictationText.Merge("Keep me", 4, 0, "  ");
        Assert.Equal("Keep me", text);
        Assert.Equal(4, caret);
    }

    [Fact]
    public void MergeClampsOutOfRangeCaret()
    {
        var (text, _) = DictationText.Merge("abc", 99, 5, "def");
        Assert.Equal("abc def", text);
    }

    [Theory]
    [InlineData("", "Hello", "Hello")]
    [InlineData("Hello", "", "Hello")]
    [InlineData("Hello", "world", "Hello world")]
    [InlineData("Hello", ", world", "Hello, world")]
    [InlineData("Is that right", "?", "Is that right?")]
    [InlineData("First part.", "second part", "First part. Second part")]
    [InlineData("  spaced   out ", " next  one ", "spaced out next one")]
    public void JoinMergesPhrases(string committed, string next, string expected)
    {
        Assert.Equal(expected, DictationText.Join(committed, next));
    }

    [Theory]
    [InlineData("", true)]
    [InlineData("Done.", true)]
    [InlineData("Done! ", true)]
    [InlineData("Really?", true)]
    [InlineData("line\n", true)]
    [InlineData("mid sentence", false)]
    [InlineData("comma, ", false)]
    public void StartsSentence(string before, bool expected)
    {
        Assert.Equal(expected, DictationText.StartsSentence(before));
    }

    [Fact]
    public void StreamingInterimThenFinalReplacesOnlyDictatedWords()
    {
        // What VoiceInput does: each interim replaces the previous insertion.
        const string typed = "Please ";
        var shown = typed + DictationText.Insertion(typed, "", "refactor");
        Assert.Equal("Please refactor", shown);
        shown = typed + DictationText.Insertion(typed, "", DictationText.Join("", "refactor the login"));
        Assert.Equal("Please refactor the login", shown);
        var committed = DictationText.Join("", "refactor the login page.");
        shown = typed + DictationText.Insertion(typed, "", DictationText.Join(committed, "add tests"));
        Assert.Equal("Please refactor the login page. Add tests", shown);
    }
}
