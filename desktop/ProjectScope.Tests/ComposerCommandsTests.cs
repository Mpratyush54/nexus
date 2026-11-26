#nullable enable
using Xunit;

namespace Nexus.Core.Tests;

public class FuzzyMatchTests
{
    [Theory]
    [InlineData("mod", "model")]
    [InlineData("MOD", "model")]
    [InlineData("cmp", "compact")]
    [InlineData("", "anything")]
    public void MatchesLettersInOrder(string query, string text) => Assert.NotNull(FuzzyMatch.Match(query, text));

    [Theory]
    [InlineData("xyz", "model")]
    [InlineData("ledom", "model")]
    [InlineData("toolong", "short")]
    public void RejectsWhatIsNotThere(string query, string text) => Assert.Null(FuzzyMatch.Match(query, text));

    [Fact]
    public void ReturnsThePositionsToHighlight()
    {
        var m = FuzzyMatch.Match("cmp", "compact")!.Value;
        Assert.Equal(new[] { 0, 2, 3 }, m.Positions);
        var sub = FuzzyMatch.Match("act", "compact")!.Value;
        Assert.Equal(new[] { 4, 5, 6 }, sub.Positions);
    }

    [Fact]
    public void PrefersWordStartsAndUnbrokenRuns()
    {
        var start = FuzzyMatch.Match("new", "new")!.Value.Score;
        var inside = FuzzyMatch.Match("new", "renew")!.Value.Score;
        var scattered = FuzzyMatch.Match("new", "n_e_w")!.Value.Score;
        Assert.True(start > inside);
        Assert.True(inside > scattered);
    }

    [Fact]
    public void AFileNameMatchBeatsOneSpreadOverFolders()
    {
        var name = FuzzyMatch.MatchPath("main", "src/app/main.cs")!.Value.Score;
        var folders = FuzzyMatch.MatchPath("main", "m/a/i/n.cs")!.Value.Score;
        Assert.True(name > folders);
        // Positions index into the whole path.
        Assert.Equal(new[] { 8, 9, 10, 11 }, FuzzyMatch.MatchPath("main", "src/app/main.cs")!.Value.Positions);
    }

    [Fact]
    public void ASlashInTheQuerySearchesTheWholePath()
    {
        Assert.NotNull(FuzzyMatch.MatchPath("app/ma", "src/app/main.cs"));
        Assert.Null(FuzzyMatch.MatchPath("lib/ma", "src/app/main.cs"));
    }

    [Fact]
    public void HighlightsOnlyTightMatches()
    {
        Assert.Equal(new[] { 0, 1, 2 }, FuzzyMatch.Highlights("ord", "Order creation is idempotent"));
        Assert.Empty(FuzzyMatch.Highlights("ord", "Project brief Key decisions"));
        Assert.Empty(FuzzyMatch.Highlights("", "anything"));
    }

    [Fact]
    public void LettersScatteredOverFoldersDoNotMatch()
    {
        Assert.Null(FuzzyMatch.MatchPath("ret", "src/checkout/order_key.go"));
        Assert.NotNull(FuzzyMatch.MatchPath("ret", "src/checkout/retry_policy.go"));
    }

    [Fact]
    public void SegmentsSplitTextIntoHighlightedRuns()
    {
        var parts = FuzzyMatch.Segments("compact", new[] { 0, 2, 3 });
        Assert.Equal(new (string, bool)[] { ("c", true), ("o", false), ("mp", true), ("act", false) }, parts);
        Assert.Equal(new (string, bool)[] { ("plain", false) }, FuzzyMatch.Segments("plain", Array.Empty<int>()));
    }
}

public class ComposerCommandsTests
{
    [Theory]
    [InlineData("/", 1, '/', "")]
    [InlineData("/mo", 3, '/', "mo")]
    [InlineData("@", 1, '@', "")]
    [InlineData("look at @src/ma", 15, '@', "src/ma")]
    [InlineData("first line\n@rea", 15, '@', "rea")]
    public void FindsTheTriggerAtTheCaret(string text, int caret, char symbol, string query)
    {
        var t = ComposerCommands.Find(text, caret);
        Assert.NotNull(t);
        Assert.Equal(symbol, t!.Value.Symbol);
        Assert.Equal(query, t.Value.Query);
        Assert.Equal(caret - query.Length - 1, t.Value.Start);
    }

    [Theory]
    [InlineData("hello /model", 12)] // "/" only at the very start
    [InlineData("/model sonnet", 13)] // past the command name
    [InlineData("mail@example.com", 16)] // an address, not a mention
    [InlineData("/src/app", 8)] // a path, not a command
    [InlineData("@file then more", 15)] // the caret left the mention
    [InlineData("", 0)]
    public void IgnoresWhatIsNotATrigger(string text, int caret) => Assert.Null(ComposerCommands.Find(text, caret));

    [Fact]
    public void FindsAMentionWhereTheCaretIsNotAtTheEnd()
    {
        var t = ComposerCommands.Find("see @rea and more", 8);
        Assert.Equal(new ComposerCommands.Trigger('@', 4, "rea"), t);
    }

    [Theory]
    [InlineData("/model", "model", "")]
    [InlineData("  /Model  claude-sonnet-4 ", "model", "claude-sonnet-4")]
    [InlineData("/mode full", "mode", "full")]
    [InlineData("/frontend:review the diff", "frontend:review", "the diff")]
    public void ParsesACommandAndItsArgument(string text, string name, string argument)
    {
        Assert.Equal((name, argument), ComposerCommands.Parse(text));
    }

    [Theory]
    [InlineData("model")]
    [InlineData("/")]
    [InlineData("/ model")]
    [InlineData("/a$b")]
    public void RejectsWhatIsNotACommand(string text) => Assert.Null(ComposerCommands.Parse(text));

    [Fact]
    public void BuiltinsDependOnTheAgent()
    {
        var claude = ComposerCommands.Builtins("claude", live: true, canRewind: false).Select(c => c.Name).ToList();
        Assert.Equal(new[] { "model", "mode", "new", "clear", "compact", "help" }, claude);
        var gemini = ComposerCommands.Builtins("gemini", live: false, canRewind: false).Select(c => c.Name).ToList();
        Assert.DoesNotContain("compact", gemini);
        Assert.DoesNotContain("mode", gemini);
        Assert.Contains("rewind", ComposerCommands.Builtins("claude", true, canRewind: true).Select(c => c.Name));
    }

    [Theory]
    [InlineData("full", "full")]
    [InlineData("Full access", "full")]
    [InlineData("ask", "ask")]
    [InlineData("edits", "edits")]
    [InlineData("whatever", "")]
    public void ModeArgumentsMapToPermissionModes(string argument, string mode) => Assert.Equal(mode, ComposerCommands.ModeFrom(argument));

    [Fact]
    public void RanksByMatchAndKeepsMenuOrderWhenEmpty()
    {
        var all = ComposerCommands.Builtins("claude", true, false);
        Assert.Equal(all.Select(c => c.Name), ComposerCommands.Rank(all, "").Select(r => r.Command.Name));
        var ranked = ComposerCommands.Rank(all, "mo");
        Assert.Equal("model", ranked[0].Command.Name);
        Assert.Contains(ranked, r => r.Command.Name == "mode");
        Assert.DoesNotContain(ranked, r => r.Command.Name == "help");
        Assert.Equal("compact", ComposerCommands.Rank(all, "cpt")[0].Command.Name);
    }

    [Fact]
    public void DiscoversCustomCommandsByName()
    {
        var root = Path.Combine(Path.GetTempPath(), "nexus-cmds-" + Guid.NewGuid().ToString("N"));
        var project = Path.Combine(root, "project");
        var home = Path.Combine(root, "home");
        try
        {
            Directory.CreateDirectory(Path.Combine(project, ".claude", "commands", "frontend"));
            Directory.CreateDirectory(Path.Combine(home, ".claude", "commands"));
            Directory.CreateDirectory(Path.Combine(home, ".codex", "prompts"));
            File.WriteAllText(Path.Combine(project, ".claude", "commands", "review.md"), "Review the diff");
            File.WriteAllText(Path.Combine(project, ".claude", "commands", "frontend", "lint.md"), "Lint");
            File.WriteAllText(Path.Combine(project, ".claude", "commands", "notes.txt"), "not a command");
            File.WriteAllText(Path.Combine(home, ".claude", "commands", "review.md"), "the user's own review");
            File.WriteAllText(Path.Combine(home, ".claude", "commands", "standup.md"), "Standup");
            File.WriteAllText(Path.Combine(home, ".claude", "commands", "model.md"), "shadows a built-in");
            File.WriteAllText(Path.Combine(home, ".codex", "prompts", "fix.md"), "Fix");

            var claude = ComposerCommands.DiscoverCustom("claude", project, home);
            Assert.Equal(new[] { "frontend:lint", "review", "standup" }, claude.Select(c => c.Name));
            Assert.Equal("Project command", claude.Single(c => c.Name == "review").Source);
            Assert.All(claude, c => Assert.Equal(ComposerCommands.Kind.Agent, c.Kind));

            Assert.Equal(new[] { "prompts:fix" }, ComposerCommands.DiscoverCustom("codex", project, home).Select(c => c.Name));
            Assert.Empty(ComposerCommands.DiscoverCustom("gemini", project, home));
            Assert.Empty(ComposerCommands.DiscoverCustom("claude", Path.Combine(root, "missing"), ""));
        }
        finally { try { Directory.Delete(root, true); } catch (IOException) { } }
    }
}

public class PromptRecallTests
{
    private static readonly IReadOnlyList<string> History = PromptRecall.Build(new[] { "third" }, new[] { "first", "second", "first", " " });

    [Fact]
    public void BuildsNewestFirstWithoutRepeats()
    {
        Assert.Equal(new[] { "third", "first", "second" }, History);
        Assert.Equal(new[] { "b", "a" }, PromptRecall.Build(Array.Empty<string>(), new[] { "a", "b" }));
    }

    [Fact]
    public void UpWalksBackAndDownComesForwardToEmpty()
    {
        var recall = new PromptRecall();
        Assert.Equal("third", recall.Step(true, "", 0, History));
        Assert.Equal("first", recall.Step(true, "third", 0, History));
        Assert.Equal("second", recall.Step(true, "first", 0, History));
        Assert.Null(recall.Step(true, "second", 0, History)); // the oldest
        Assert.Equal("first", recall.Step(false, "second", 6, History));
        Assert.Equal("third", recall.Step(false, "first", 5, History));
        Assert.Equal("", recall.Step(false, "third", 5, History));
        Assert.Null(recall.Step(false, "", 0, History));
        Assert.Equal(-1, recall.Index);
    }

    [Fact]
    public void TypedTextIsNeverReplaced()
    {
        var recall = new PromptRecall();
        Assert.Null(recall.Step(true, "my own words", 0, History));
        // A recalled prompt that was edited is the person's text now.
        Assert.Equal("third", recall.Step(true, "", 0, History));
        Assert.Null(recall.Step(true, "third, edited", 0, History));
        Assert.Equal(-1, recall.Index);
    }

    [Fact]
    public void MultiLinePromptsMoveTheCaretFirst()
    {
        var history = new[] { "line one\nline two" };
        var recall = new PromptRecall();
        Assert.Equal("line one\nline two", recall.Step(true, "", 0, history));
        // Caret on the second line: ↑ moves up a line, it doesn't recall.
        Assert.Null(recall.Step(true, "line one\nline two", 12, history));
        Assert.Null(recall.Step(false, "line one\nline two", 3, history));
        Assert.Equal("", recall.Step(false, "line one\nline two", 17, history));
    }

    [Fact]
    public void NothingToRecallInANewChat()
    {
        Assert.Null(new PromptRecall().Step(true, "", 0, Array.Empty<string>()));
    }
}

public class ComposerMentionTests
{
    [Fact]
    public void PathsGoAsAtReferencesAndChatsAsContext()
    {
        var mentions = new List<ComposerMention>
        {
            new() { Kind = ComposerMention.File, Value = "src/app/main.cs", Label = "main.cs" },
            new() { Kind = ComposerMention.Folder, Value = "docs/", Label = "docs/" },
            new() { Kind = ComposerMention.File, Value = "My Notes/todo.md", Label = "todo.md" },
            new() { Kind = ComposerMention.Chat, Value = "c1", Label = "Fix \"login\" bug", Context = "We moved the token check into middleware." },
            new() { Kind = ComposerMention.Memory, Value = "m1", Label = "Deploys", Context = "Deploys go out on Tuesdays." },
        };
        var prompt = MentionText.Apply("Please refactor", mentions, "claude");
        Assert.Equal(
            "Please refactor\n\n@src/app/main.cs @docs/ @\"My Notes/todo.md\"\n\n" +
            "<context source=\"Earlier Nexus chat: Fix 'login' bug\">\nWe moved the token check into middleware.\n</context>\n\n" +
            "<context source=\"Nexus memory: Deploys\">\nDeploys go out on Tuesdays.\n</context>",
            prompt);
    }

    [Fact]
    public void NoMentionsLeavesThePromptAlone()
    {
        Assert.Equal("hi ", MentionText.Apply("hi ", Array.Empty<ComposerMention>(), "claude"));
    }

    [Fact]
    public void AReferenceAlreadyTypedIsNotRepeated()
    {
        var mentions = new[] { new ComposerMention { Kind = ComposerMention.File, Value = "a.cs" } };
        Assert.Equal("look at @a.cs please", MentionText.Apply("look at @a.cs please", mentions, "claude"));
    }

    [Fact]
    public void LongContextIsClipped()
    {
        var mentions = new[] { new ComposerMention { Kind = ComposerMention.Memory, Value = "m", Label = "big", Context = new string('x', MentionText.MaxContextChars + 500) } };
        var prompt = MentionText.Apply("q", mentions, "codex");
        Assert.Contains("…\n</context>", prompt);
        Assert.True(prompt.Length < MentionText.MaxContextChars + 200);
    }

    [Fact]
    public void ChipLabelsAreShort()
    {
        Assert.Equal("main.cs", MentionText.ChipLabel(new ComposerMention { Kind = ComposerMention.File, Value = "src/app/main.cs" }));
        Assert.Equal("app/", MentionText.ChipLabel(new ComposerMention { Kind = ComposerMention.Folder, Value = "src/app/" }));
        Assert.EndsWith("…", MentionText.ChipLabel(new ComposerMention { Kind = ComposerMention.Chat, Value = "c", Label = new string('t', 60) }));
    }

    [Fact]
    public void DraftsKeepMentions()
    {
        var path = Path.Combine(Path.GetTempPath(), "nexus-drafts-" + Guid.NewGuid().ToString("N"), "drafts.json");
        try
        {
            var store = new ComposerDraftStore(path);
            var changed = new List<string>();
            store.Changed += changed.Add;
            Assert.True(store.Set("chat", new ComposerDraft { Mentions = { new ComposerMention { Kind = ComposerMention.File, Value = "a.cs", Label = "a.cs" } } }));
            store.Save();
            var again = new ComposerDraftStore(path).Get("chat");
            Assert.Equal("a.cs", Assert.Single(again!.Mentions).Value);
            Assert.Equal(new[] { "chat" }, changed);
            Assert.True(store.ShowsDraftMarker("chat"));
            store.ActiveSession = "chat";
            Assert.False(store.ShowsDraftMarker("chat"));
            Assert.True(store.Remove("chat"));
            Assert.Equal(new[] { "chat", "chat", "chat" }, changed);
        }
        finally { try { Directory.Delete(Path.GetDirectoryName(path)!, true); } catch (IOException) { } }
    }
}

public class ProjectFileIndexTests
{
    [Fact]
    public void AddsFoldersBeforeFiles()
    {
        var all = ProjectFileIndex.WithFolders(new[] { "README.md", "src/app/main.cs", "src/lib.cs" });
        Assert.Equal(new[] { "src/", "src/app/", "README.md", "src/app/main.cs", "src/lib.cs" }, all);
    }

    [Fact]
    public void SearchRanksFileNamesAndRecentPicks()
    {
        var entries = ProjectFileIndex.WithFolders(new[] { "README.md", "src/app/main.cs", "src/domain/manager.cs", "docs/maintenance.md" });
        var hits = ProjectFileIndex.Search(entries, "main", Array.Empty<string>());
        Assert.Equal("src/app/main.cs", hits[0].Path);
        Assert.Equal(new[] { 8, 9, 10, 11 }, hits[0].Positions);
        var recent = ProjectFileIndex.Search(entries, "ma", new[] { "docs/maintenance.md" });
        Assert.Equal("docs/maintenance.md", recent[0].Path);
        Assert.Empty(ProjectFileIndex.Search(entries, "zzz", Array.Empty<string>()));
    }

    [Fact]
    public void EmptyQueryShowsRecentThenTopLevel()
    {
        var entries = ProjectFileIndex.WithFolders(new[] { "README.md", "src/app/main.cs" });
        var hits = ProjectFileIndex.Search(entries, "", new[] { "src/app/main.cs" });
        Assert.Equal(new[] { "src/app/main.cs", "src/", "README.md" }, hits.Select(h => h.Path));
        Assert.True(hits[1].IsFolder);
    }

    [Fact]
    public void SearchIsFastOnABigProject()
    {
        var files = Enumerable.Range(0, ProjectFileIndex.MaxEntries).Select(i => $"src/module{i % 200}/sub{i % 7}/file{i}.cs").ToList();
        var entries = ProjectFileIndex.WithFolders(files);
        var watch = System.Diagnostics.Stopwatch.StartNew();
        var hits = ProjectFileIndex.Search(entries, "file1999", Array.Empty<string>());
        watch.Stop();
        Assert.Equal("src/module199/sub4/file1999.cs", hits[0].Path);
        Assert.True(watch.ElapsedMilliseconds < 1000, $"took {watch.ElapsedMilliseconds} ms");
    }

    [Fact]
    public void WalkRespectsGitignoreAndSkipsDependencyFolders()
    {
        var root = Path.Combine(Path.GetTempPath(), "nexus-walk-" + Guid.NewGuid().ToString("N"));
        try
        {
            foreach (var dir in new[] { "src", "node_modules/pkg", "logs", "build" }) Directory.CreateDirectory(Path.Combine(root, dir));
            File.WriteAllText(Path.Combine(root, ".gitignore"), "# comment\nlogs/\n*.secret\n/top.txt\n");
            foreach (var file in new[] { "src/a.cs", "src/key.secret", "node_modules/pkg/index.js", "logs/x.log", "build/out.dll", "top.txt", "keep.txt" })
                File.WriteAllText(Path.Combine(root, file), "x");
            var files = ProjectFileIndex.Walk(root, IgnoreRules.Load(root));
            Assert.Equal(new[] { ".gitignore", "keep.txt", "src/a.cs" }, files.OrderBy(f => f, StringComparer.Ordinal));
        }
        finally { try { Directory.Delete(root, true); } catch (IOException) { } }
    }

    [Theory]
    [InlineData("*.log", "x.log", true)]
    [InlineData("*.log", "dir/x.log", false)]
    [InlineData("**/x.log", "a/b/x.log", true)]
    [InlineData("a?c", "abc", true)]
    [InlineData("a?c", "a/c", false)]
    [InlineData("Secrets", "secrets", true)]
    public void GlobsMatchLikeGit(string pattern, string text, bool expected) => Assert.Equal(expected, IgnoreRules.Glob(pattern, text));
}
