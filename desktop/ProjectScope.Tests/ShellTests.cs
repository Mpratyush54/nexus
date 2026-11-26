using Nexus.Core;
using Xunit;

namespace Nexus.Core.Tests;

public class PaletteRankingTests
{
    private static PaletteRanking.Entry E(string id, string title, string group = "Actions", string keywords = "") => new(id, group, title, keywords);

    [Fact]
    public void PrefixBeatsWordStartBeatsSubstringBeatsSubsequence()
    {
        var q = "set";
        Assert.True(PaletteRanking.Score(q, "Settings") > PaletteRanking.Score(q, "Open settings"));
        Assert.True(PaletteRanking.Score(q, "Open settings") > PaletteRanking.Score(q, "Reset cache"));
        Assert.True(PaletteRanking.Score(q, "Reset cache") > PaletteRanking.Score(q, "Sweet tea"));
        Assert.True(PaletteRanking.Score(q, "Sweet tea") > 0);
        Assert.Equal(0, PaletteRanking.Score(q, "Home"));
    }

    [Fact]
    public void InitialsAndWordsInAnyOrderMatch()
    {
        Assert.True(PaletteRanking.Score("nc", "New chat") > PaletteRanking.Score("nc", "Open launch config"));
        Assert.True(PaletteRanking.Score("chat new", "New chat") >= 600);
    }

    [Fact]
    public void RankOrdersByScoreAndKeepsOnlyMatches()
    {
        var ranked = PaletteRanking.Rank(new[] { E("a", "Report a problem"), E("b", "New chat"), E("c", "Archive this chat") }, "chat", Array.Empty<string>());
        Assert.Equal(new[] { "b", "c" }, ranked.Select(r => r.Entry.Id));
    }

    [Fact]
    public void KeywordsMatchButCountLessThanTitles()
    {
        var ranked = PaletteRanking.Rank(new[] { E("logs", "Open logs folder", keywords: "diagnostics"), E("diag", "Diagnostics") }, "diag", Array.Empty<string>());
        Assert.Equal("diag", ranked[0].Entry.Id);
        Assert.Contains(ranked, r => r.Entry.Id == "logs");
    }

    [Fact]
    public void EmptyQueryPutsRecentPicksFirstInRecencyOrder()
    {
        var entries = new[] { E("a", "Alpha"), E("b", "Beta"), E("c", "Gamma"), E("d", "Delta") };
        var ranked = PaletteRanking.Rank(entries, "", new[] { "c", "a" });
        Assert.Equal(new[] { "c", "a", "b", "d" }, ranked.Select(r => r.Entry.Id));
    }

    [Fact]
    public void RecentPickWinsATieWhileTyping()
    {
        var entries = new[] { E("one", "Fix the login bug", "Chats"), E("two", "Fix the login bug", "Chats") };
        var ranked = PaletteRanking.Rank(entries, "login", new[] { "two" });
        Assert.Equal("two", ranked[0].Entry.Id);
    }

    [Fact]
    public void DuplicateIdsAreListedOnce()
    {
        var ranked = PaletteRanking.Rank(new[] { E("x", "Chat"), E("x", "Chat again") }, "chat", Array.Empty<string>());
        Assert.Single(ranked);
    }

    [Fact]
    public void GroupsFollowTheirBestResult()
    {
        var ranked = PaletteRanking.Rank(new[] { E("a", "Projects overview", "Actions"), E("p", "Projects", "Projects"), E("c", "my projects chat", "Chats") }, "projects", Array.Empty<string>());
        var groups = PaletteRanking.Grouped(ranked);
        Assert.Equal("Projects", groups[0].Group);
    }

    [Fact]
    public void RememberMovesToFrontAndCaps()
    {
        var list = PaletteRanking.Remember(new[] { "a", "b", "c" }, "b", max: 3);
        Assert.Equal(new[] { "b", "a", "c" }, list);
        Assert.Equal(new[] { "d", "b", "a" }, PaletteRanking.Remember(list, "d", max: 3));
    }

    [Fact]
    public void RankingIsFastEnoughForEveryKeystroke()
    {
        var entries = Enumerable.Range(0, 2000).Select(i => E("chat:" + i, $"Chat number {i} about the parser and release", "Chats")).ToList();
        PaletteRanking.Rank(entries, "warm", Array.Empty<string>());
        var watch = System.Diagnostics.Stopwatch.StartNew();
        for (var i = 0; i < 10; i++) PaletteRanking.Rank(entries, "parsr rel", Array.Empty<string>());
        Assert.True(watch.ElapsedMilliseconds / 10 < 50, $"{watch.ElapsedMilliseconds / 10} ms per rank");
    }
}

public class ShortcutRegistryTests
{
    [Fact]
    public void RegistryHasNoConflictsOnEitherPlatform()
    {
        Assert.Empty(ShortcutRegistry.Conflicts(ShortcutRegistry.All("Alt+Space", mac: false)));
        Assert.Empty(ShortcutRegistry.Conflicts(ShortcutRegistry.All("Option+Space", mac: true)));
    }

    [Fact]
    public void IdsAreUnique()
    {
        var duplicates = ShortcutRegistry.All().GroupBy(s => s.Id).Where(g => g.Count() > 1).Select(g => g.Key);
        Assert.Empty(duplicates);
    }

    [Fact]
    public void LabelsUseCtrlOnWindowsAndSymbolsOnMac()
    {
        var palette = ShortcutRegistry.Find(ShortcutRegistry.Palette)!;
        Assert.Equal("Ctrl+K", ShortcutRegistry.Label(palette, mac: false));
        Assert.Equal("⌘K", ShortcutRegistry.Label(palette, mac: true));
        var archive = ShortcutRegistry.Find(ShortcutRegistry.ArchiveChat)!;
        Assert.Equal("Ctrl+Shift+A", ShortcutRegistry.Label(archive, mac: false));
        Assert.Equal("⇧⌘A", ShortcutRegistry.Label(archive, mac: true));
        Assert.Equal(new[] { "⇧", "⌘", "A" }, ShortcutRegistry.Keycaps(archive, mac: true));
    }

    [Fact]
    public void ControlTabStaysControlOnMac()
    {
        var next = ShortcutRegistry.Find(ShortcutRegistry.NextChat)!;
        Assert.Equal("Ctrl+Tab", ShortcutRegistry.Label(next, mac: false));
        Assert.Equal("⌃⇥", ShortcutRegistry.Label(next, mac: true));
    }

    [Fact]
    public void QuickAskFollowsTheConfiguredKeys()
    {
        var win = ShortcutRegistry.Find(ShortcutRegistry.QuickAsk, ShortcutRegistry.All("Ctrl+Shift+Space"))!;
        Assert.True(win.Global);
        Assert.Equal("Ctrl+Shift+Space", ShortcutRegistry.Label(win, mac: false));
        var mac = ShortcutRegistry.Find(ShortcutRegistry.QuickAsk, ShortcutRegistry.All("Option+Space", mac: true))!;
        Assert.Equal("⌥Space", ShortcutRegistry.Label(mac, mac: true));
        // An unusable setting falls back to the platform default.
        var fallback = ShortcutRegistry.Find(ShortcutRegistry.QuickAsk, ShortcutRegistry.All("Space"))!;
        Assert.Equal("Alt+Space", ShortcutRegistry.Label(fallback, mac: false));
    }

    [Fact]
    public void SheetGroupsAlternateKeysOnOneRow()
    {
        var sheet = ShortcutRegistry.Sheet(ShortcutRegistry.All());
        var view = sheet.Single(g => g.Group == ShortcutRegistry.GroupView);
        var sidebar = view.Rows.Single(r => r.Title == "Show or hide the sidebar");
        Assert.Equal(2, sidebar.Keys.Count);
        Assert.Equal(new[] { ShortcutRegistry.GroupGeneral, ShortcutRegistry.GroupChats, ShortcutRegistry.GroupChat, ShortcutRegistry.GroupView }, sheet.Select(g => g.Group));
    }

    [Theory]
    [InlineData("Alt+Space", "Space", false, false, true)]
    [InlineData("ctrl+shift+space", "Space", true, true, false)]
    [InlineData("Option+Space", "Space", false, false, true)]
    [InlineData("Cmd+Option+n", "N", true, false, true)]
    public void HotkeysParse(string text, string key, bool primary, bool shift, bool alt)
    {
        Assert.Equal(new HotkeyText(key, primary, shift, alt), HotkeyText.Parse(text));
    }

    [Theory]
    [InlineData("")]
    [InlineData("Space")]
    [InlineData("Shift+Space")]
    [InlineData("Alt+Space+K")]
    public void BadHotkeysAreRejected(string text) => Assert.Null(HotkeyText.Parse(text));

    [Fact]
    public void HotkeyFormatsPerPlatform()
    {
        var keys = HotkeyText.Parse("Alt+Shift+Space")!;
        Assert.Equal("Alt+Shift+Space", keys.Format(mac: false));
        Assert.Equal("Option+Shift+Space", keys.Format(mac: true));
        Assert.All(HotkeyText.Choices(false).Concat(HotkeyText.Choices(true)), c => Assert.NotNull(HotkeyText.Parse(c)));
    }
}

public class ToastQueueTests
{
    private static readonly DateTime T0 = new(2026, 11, 26, 12, 0, 0, DateTimeKind.Utc);

    [Fact]
    public void PlainNoticesLastFiveSecondsAndUndoOnesEight()
    {
        var q = new ToastQueue();
        var expired = new List<string>();
        q.Show("plain", T0, onExpire: () => expired.Add("plain"));
        q.Show("undo", T0, onAction: () => { }, onExpire: () => expired.Add("undo"));
        q.Tick(T0.AddSeconds(4.9));
        Assert.Empty(expired);
        q.Tick(T0.AddSeconds(5));
        Assert.Equal(new[] { "plain" }, expired);
        q.Tick(T0.AddSeconds(7.9));
        Assert.Single(q.Visible);
        q.Tick(T0.AddSeconds(8));
        Assert.Equal(new[] { "plain", "undo" }, expired);
        Assert.Empty(q.Visible);
    }

    [Fact]
    public void UndoRunsTheActionAndNeverTheExpire()
    {
        var q = new ToastQueue();
        var log = new List<string>();
        var item = q.Show("Deleted", T0, onAction: () => log.Add("undo"), onExpire: () => log.Add("delete"));
        Assert.Equal("Undo", item.ActionLabel);
        Assert.True(q.Act(item.Id));
        q.Tick(T0.AddMinutes(1));
        q.Flush();
        Assert.Equal(new[] { "undo" }, log);
        Assert.False(q.Act(item.Id));
    }

    [Fact]
    public void AtMostThreeShowAndAFourthCommitsTheOldest()
    {
        var q = new ToastQueue();
        var log = new List<string>();
        for (var i = 1; i <= 4; i++)
        {
            var n = i;
            q.Show("t" + n, T0.AddSeconds(i), onAction: () => log.Add("undo" + n), onExpire: () => log.Add("commit" + n));
        }
        Assert.Equal(ToastQueue.MaxVisible, q.Visible.Count);
        Assert.Equal(new[] { "commit1" }, log);
        Assert.Equal(new[] { "t2", "t3", "t4" }, q.Visible.Select(v => v.Message));
    }

    [Fact]
    public void CtrlZUndoesTheNewestUndoableNotice()
    {
        var q = new ToastQueue();
        var log = new List<string>();
        q.Show("archived", T0, onAction: () => log.Add("unarchive"));
        q.Show("text size", T0);
        Assert.True(q.UndoLatest());
        Assert.Equal(new[] { "unarchive" }, log);
        Assert.False(q.UndoLatest());
        Assert.Single(q.Visible);
    }

    [Fact]
    public void HoverPausesTheClock()
    {
        var q = new ToastQueue();
        var expired = 0;
        var item = q.Show("x", T0, onExpire: () => expired++);
        q.Pause(item.Id, T0.AddSeconds(4));
        q.Tick(T0.AddSeconds(30));
        Assert.Equal(0, expired);
        q.Resume(item.Id, T0.AddSeconds(30));
        q.Tick(T0.AddSeconds(31));
        Assert.Equal(0, expired);
        q.Tick(T0.AddSeconds(31.6));
        Assert.Equal(1, expired);
    }

    [Fact]
    public void DismissAndFlushCommitOnceEach()
    {
        var q = new ToastQueue();
        var commits = 0;
        var outcomes = new List<ToastQueue.Outcome>();
        q.Ended += (_, outcome) => outcomes.Add(outcome);
        var a = q.Show("a", T0, onAction: () => { }, onExpire: () => commits++);
        q.Show("b", T0, onAction: () => { }, onExpire: () => commits++);
        q.Dismiss(a.Id);
        q.Dismiss(a.Id);
        q.Flush();
        q.Flush();
        Assert.Equal(2, commits);
        Assert.Equal(new[] { ToastQueue.Outcome.Dismissed, ToastQueue.Outcome.Flushed }, outcomes);
    }
}
