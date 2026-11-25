using Nexus.Core;
using Xunit;

namespace Nexus.Core.Tests;

public class MarkdownBlocksTests
{
    private static List<MdBlock> P(string s) => MarkdownBlocks.Parse(s);

    [Fact]
    public void ParagraphsKeepLineBreaksAndSplitOnBlankLines()
    {
        var blocks = P("First line\nsecond line\n\nNext paragraph");
        Assert.Equal(2, blocks.Count);
        Assert.Equal("First line\nsecond line", blocks[0].Text);
        Assert.Equal("Next paragraph", blocks[1].Text);
    }

    [Fact]
    public void FenceKeepsLanguageAndCode()
    {
        var blocks = P("Intro\n```Python\ndef f():\n    return 1\n```\nAfter");
        Assert.Equal(new[] { MdKind.Paragraph, MdKind.Code, MdKind.Paragraph }, blocks.Select(b => b.Kind));
        Assert.Equal("python", blocks[1].Language);
        Assert.Equal("def f():\n    return 1", blocks[1].Text);
        Assert.True(blocks[1].Closed);
    }

    [Fact]
    public void UnclosedFenceRunsToTheEnd()
    {
        var blocks = P("```js\nconst a = 1;\n");
        Assert.Single(blocks);
        Assert.False(blocks[0].Closed);
        Assert.Equal("const a = 1;", blocks[0].Text.TrimEnd());
    }

    [Fact]
    public void FenceInsideAListItemIsStillAFence()
    {
        var blocks = P("1. Run:\n   ```sh\n   npm test\n   ```\n2. Done");
        Assert.Equal(new[] { MdKind.ListItem, MdKind.Code, MdKind.ListItem }, blocks.Select(b => b.Kind));
        Assert.Equal("npm test", blocks[1].Text);
    }

    [Fact]
    public void OrderedListsKeepTheirNumbers()
    {
        var blocks = P("1. one\n2. two\n10) ten");
        Assert.Equal(new[] { "1.", "2.", "10." }, blocks.Select(b => b.Number));
        Assert.All(blocks, b => Assert.Equal(MdKind.ListItem, b.Kind));
    }

    [Fact]
    public void OrderedListsCountFromTheFirstItem()
    {
        // CommonMark: only the first number matters.
        var blocks = P("1. a\n2. b\n   - nested\n3. c\n10. d");
        Assert.Equal(new[] { "1.", "2.", "", "3.", "4." }, blocks.Select(b => b.Ordinal));
        Assert.Equal(new[] { "5.", "6." }, P("5. a\n1. b").Select(b => b.Ordinal));
        // A paragraph ends the list; the next one starts again.
        Assert.Equal(new[] { "1.", "", "1." }, P("1. a\n\nText\n\n1. b").Select(b => b.Ordinal));
    }

    [Fact]
    public void StreamedListsAreNumberedLikeAFullParse()
    {
        var stream = new MarkdownStream();
        stream.Update("1. a\n2. b\n");
        var blocks = stream.Update("1. a\n2. b\n7. c");
        Assert.Equal(new[] { "1.", "2.", "3." }, blocks.Select(b => b.Ordinal));
    }

    [Fact]
    public void NestedItemsIndentPerTwoSpaces()
    {
        var blocks = P("- a\n  - b\n    - c\n- d");
        Assert.Equal(new[] { 0, 1, 2, 0 }, blocks.Select(b => b.Indent));
        Assert.Equal("", blocks[0].Number);
    }

    [Fact]
    public void TaskItems()
    {
        var blocks = P("- [ ] todo\n- [x] done\n- plain");
        Assert.Equal(new bool?[] { false, true, null }, blocks.Select(b => b.Task));
        Assert.Equal("todo", blocks[0].Text);
    }

    [Fact]
    public void ListItemContinuationLines()
    {
        var blocks = P("- first\n  still first\n- second");
        Assert.Equal(2, blocks.Count);
        Assert.Equal("first\nstill first", blocks[0].Text);
    }

    [Fact]
    public void HeadingsRulesQuotes()
    {
        var blocks = P("## Title ##\n---\n> quoted\n> more\n***");
        Assert.Equal(new[] { MdKind.Heading, MdKind.Rule, MdKind.Quote, MdKind.Rule }, blocks.Select(b => b.Kind));
        Assert.Equal(2, blocks[0].Level);
        Assert.Equal("Title", blocks[0].Text);
        Assert.Equal("quoted\nmore", blocks[2].Text);
    }

    [Fact]
    public void HashtagIsNotAHeading()
    {
        Assert.Equal(MdKind.Paragraph, P("#hashtag here")[0].Kind);
    }

    [Fact]
    public void Tables()
    {
        var blocks = P("| a | b |\n|---|:-:|\n| 1 | 2 \\| 3 |\n\ntext");
        Assert.Equal(MdKind.Table, blocks[0].Kind);
        Assert.Equal(2, blocks[0].Rows.Count);
        Assert.Equal(new[] { "1", "2 | 3" }, MarkdownBlocks.TableCells(blocks[0].Rows[1]));
        Assert.Equal(MdKind.Paragraph, blocks[1].Kind);
    }

    [Fact]
    public void PipeRowWithoutRuleIsItsOwnParagraph()
    {
        var blocks = P("Intro\n| a | b |");
        Assert.Equal(2, blocks.Count);
        Assert.Equal("Intro", blocks[0].Text);
    }

    [Fact]
    public void SourceSlicesAreExact()
    {
        const string text = "a\r\n\r\n```\nx\n```\n- y";
        foreach (var b in P(text)) Assert.Equal(text[b.Start..b.End], b.Source);
    }

    [Fact]
    public void EmptyAndBlank()
    {
        Assert.Empty(P(""));
        Assert.Empty(P("\n\n   \n"));
    }
}

public class MarkdownInlineTests
{
    private static List<MdInline> P(string s) => MarkdownInline.Parse(s);

    [Fact]
    public void BoldItalicStrikeCode()
    {
        var items = P("a **b** *c* ~~d~~ `e` _f_");
        Assert.Equal(new[] { MdSpan.Text, MdSpan.Bold, MdSpan.Text, MdSpan.Italic, MdSpan.Text, MdSpan.Strike, MdSpan.Text, MdSpan.Code, MdSpan.Text, MdSpan.Italic },
            items.Select(i => i.Kind));
        Assert.Equal("e", items[7].Text);
    }

    [Fact]
    public void SnakeCaseAndMathStayPlain()
    {
        var items = P("use snake_case_name and 2*3*4");
        Assert.Single(items);
        Assert.Equal(MdSpan.Text, items[0].Kind);
    }

    [Fact]
    public void BoldContainsCodeAndLinks()
    {
        var bold = P("**see `x` and [docs](https://a.dev)**").Single();
        Assert.Equal(MdSpan.Bold, bold.Kind);
        Assert.Contains(bold.Items, i => i.Kind == MdSpan.Code && i.Text == "x");
        Assert.Contains(bold.Items, i => i.Kind == MdSpan.Link && i.Url == "https://a.dev");
    }

    [Fact]
    public void CodeIsLiteral()
    {
        var items = P("`**not bold**` and ``a ` b``");
        Assert.Equal("**not bold**", items[0].Text);
        Assert.Equal(MdSpan.Code, items[2].Kind);
        Assert.Equal("a ` b", items[2].Text);
    }

    [Fact]
    public void LinksImagesAndBareUrls()
    {
        var items = P("![shot](C:/tmp/a.png) see https://example.com/x, or <https://b.io>.");
        Assert.Equal(MdSpan.Image, items[0].Kind);
        Assert.Equal("C:/tmp/a.png", items[0].Url);
        var links = items.Where(i => i.Kind == MdSpan.Link).ToList();
        Assert.Equal(new[] { "https://example.com/x", "https://b.io" }, links.Select(l => l.Url));
    }

    [Fact]
    public void EscapesAndLineBreaks()
    {
        var items = P("\\*literal\\*\nnext");
        Assert.Equal("*literal*", items[0].Text);
        Assert.Equal(MdSpan.LineBreak, items[1].Kind);
        Assert.Equal("*literal*\nnext", MarkdownInline.Plain(items));
    }

    [Theory]
    [InlineData("https://a.b", true)]
    [InlineData("mailto:x@y.z", true)]
    [InlineData("javascript:alert(1)", false)]
    [InlineData("file:///c:/x", false)]
    [InlineData("src/a.ts", false)]
    public void OnlyWebLinksOpen(string url, bool web) => Assert.Equal(web, MarkdownInline.IsWebUrl(url));
}

public class MarkdownStreamTests
{
    [Fact]
    public void GrowingTextMatchesAFullParseAtEveryStep()
    {
        const string reply = "Here is the plan:\n\n1. Read **the** file\n2. Fix `retry`\n\n```ts\nconst a = 1;\nconst b = 2;\n```\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n> note\n\nDone — see [docs](https://x.dev).";
        var stream = new MarkdownStream();
        for (var n = 0; n <= reply.Length; n++)
        {
            var text = reply[..n];
            var streamed = stream.Update(text);
            var full = MarkdownBlocks.Parse(text);
            Assert.Equal(full.Count, streamed.Count);
            for (var i = 0; i < full.Count; i++)
                Assert.True(full[i].SameAs(streamed[i]), $"at {n}: block {i} {streamed[i]} != {full[i]}");
        }
    }

    [Fact]
    public void CommittedBlocksAreReusedNotReparsed()
    {
        var stream = new MarkdownStream();
        var first = stream.Update("Para one\n\nPara two\n\nPara th");
        Assert.Equal(2, stream.CommittedCount);
        var second = stream.Update("Para one\n\nPara two\n\nPara three is longer");
        Assert.Same(first[0], second[0]);
        Assert.Same(first[1], second[1]);
        Assert.Equal("Para three is longer", second[2].Text);
    }

    [Fact]
    public void PartialLastLineDoesNotCommit()
    {
        var stream = new MarkdownStream();
        stream.Update("Para\n---");
        // "---" could still become "---x" (a paragraph line), so nothing is final.
        Assert.Equal(0, stream.CommittedCount);
        var blocks = stream.Update("Para\n---x");
        Assert.Single(blocks);
        Assert.Equal("Para\n---x", blocks[0].Text);
    }

    [Fact]
    public void OpenFenceGrowsAsOneBlock()
    {
        var stream = new MarkdownStream();
        var a = stream.Update("Text\n\n```py\nprint(1)\n");
        Assert.False(a[^1].Closed);
        var b = stream.Update("Text\n\n```py\nprint(1)\nprint(2)\n```\n\nAfter");
        Assert.True(b[1].Closed);
        Assert.Equal("print(1)\nprint(2)", b[1].Text);
        Assert.Equal(MdKind.Paragraph, b[2].Kind);
    }

    [Fact]
    public void ReplacedTextStartsOver()
    {
        var stream = new MarkdownStream();
        stream.Update("Alpha\n\nBeta\n\nGam");
        var blocks = stream.Update("Different\n\nreply");
        Assert.Equal(new[] { "Different", "reply" }, blocks.Select(b => b.Text));
    }

    [Theory]
    [InlineData("plain", "plain")]
    [InlineData("a **bol", "a **bol**")]
    [InlineData("a **", "a")]
    [InlineData("run `npm te", "run `npm te`")]
    [InlineData("run `", "run")]
    [InlineData("**a `b", "**a `b`**")]
    [InlineData("~~gone", "~~gone~~")]
    [InlineData("see [docs](https://exa", "see docs")]
    [InlineData("img ![alt](pa", "img ")]
    [InlineData("**done** and `ok`", "**done** and `ok`")]
    [InlineData("snake_case__", "snake_case__")]
    public void PartialInlineIsSafe(string input, string expected) => Assert.Equal(expected, MarkdownStream.Partial(input));
}

public class CodeSyntaxTests
{
    private static string Of(string code, CodeSpan s) => code.Substring(s.Start, s.Length);

    [Fact]
    public void CSharpKeywordsStringsCommentsNumbers()
    {
        const string code = "var x = \"hi\"; // note\nreturn 42;";
        var spans = CodeSyntax.Tokenize(code, "csharp");
        Assert.Contains(spans, s => s.Kind == CodeToken.Keyword && Of(code, s) == "var");
        Assert.Contains(spans, s => s.Kind == CodeToken.String && Of(code, s) == "\"hi\"");
        Assert.Contains(spans, s => s.Kind == CodeToken.Comment && Of(code, s) == "// note");
        Assert.Contains(spans, s => s.Kind == CodeToken.Keyword && Of(code, s) == "return");
        Assert.Contains(spans, s => s.Kind == CodeToken.Number && Of(code, s) == "42");
    }

    [Fact]
    public void PythonTripleQuotesAndHashComments()
    {
        const string code = "def f():\n    \"\"\"doc\nmore\"\"\"\n    return None  # done";
        var spans = CodeSyntax.Tokenize(code, "py");
        Assert.Contains(spans, s => s.Kind == CodeToken.String && Of(code, s) == "\"\"\"doc\nmore\"\"\"");
        Assert.Contains(spans, s => s.Kind == CodeToken.Comment && Of(code, s) == "# done");
        Assert.Contains(spans, s => s.Kind == CodeToken.Keyword && Of(code, s) == "None");
    }

    [Fact]
    public void DiffLines()
    {
        const string code = "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n same";
        var spans = CodeSyntax.Tokenize(code, "diff");
        Assert.Equal(CodeToken.Removed, spans.Single(s => Of(code, s) == "-old").Kind);
        Assert.Equal(CodeToken.Added, spans.Single(s => Of(code, s) == "+new").Kind);
        Assert.Equal(CodeToken.Meta, spans.Single(s => Of(code, s) == "@@ -1 +1 @@").Kind);
        Assert.DoesNotContain(spans, s => Of(code, s) == " same");
    }

    [Fact]
    public void JsonKeysAndValues()
    {
        const string code = "{\"name\": \"nexus\", \"ok\": true, \"n\": 3}";
        var spans = CodeSyntax.Tokenize(code, "json");
        Assert.Equal(CodeToken.Property, spans.First(s => Of(code, s) == "\"name\"").Kind);
        Assert.Equal(CodeToken.String, spans.First(s => Of(code, s) == "\"nexus\"").Kind);
        Assert.Contains(spans, s => s.Kind == CodeToken.Keyword && Of(code, s) == "true");
    }

    [Fact]
    public void ShellFlagsVariablesAndApostrophes()
    {
        const string code = "git commit -m \"msg\" # it's fine\necho $HOME";
        var spans = CodeSyntax.Tokenize(code, "bash");
        Assert.Contains(spans, s => s.Kind == CodeToken.Keyword && Of(code, s) == "git");
        Assert.Contains(spans, s => s.Kind == CodeToken.Meta && Of(code, s) == "-m");
        Assert.Contains(spans, s => s.Kind == CodeToken.Comment && Of(code, s) == "# it's fine");
        Assert.Contains(spans, s => s.Kind == CodeToken.Type && Of(code, s) == "$HOME");
    }

    [Fact]
    public void SpansAreOrderedAndInside()
    {
        const string code = "const s = `a\nb`; /* c */ let n = 0x1F;";
        var spans = CodeSyntax.Tokenize(code, "ts");
        var at = 0;
        foreach (var s in spans)
        {
            Assert.True(s.Start >= at && s.Start + s.Length <= code.Length);
            at = s.Start + s.Length;
        }
    }

    [Fact]
    public void UnknownLanguageAndHugeCodeArePlain()
    {
        Assert.Empty(CodeSyntax.Tokenize("if x then y", "brainfudge"));
        Assert.Empty(CodeSyntax.Tokenize(new string('a', CodeSyntax.MaxHighlightChars + 1), "js"));
    }
}
