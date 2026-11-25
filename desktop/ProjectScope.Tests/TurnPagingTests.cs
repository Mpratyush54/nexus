#nullable enable
using Nexus.Core;
using Xunit;

public class TurnPagingTests
{
    // Byte-offset turn indexes, as Claude Code captures store them.
    private static readonly long[] Chat = { 2108, 281496, 584099, 1133907, 1146787, 1190239, 1202754, 1218943, 1220865, 1222696 };

    [Fact]
    public void BackwardAnswerIsAccepted()
    {
        // The newest 3 turns; next is the first idx on the page.
        Assert.False(TurnPaging.LooksForward(new long[] { 1218943, 1220865, 1222696 }, "1218943", null));
        // An older backward page: everything before the cursor.
        Assert.False(TurnPaging.LooksForward(new long[] { 1190239, 1202754 }, "1190239", 1218943));
        // The first page of a short chat has no next cursor either way.
        Assert.False(TurnPaging.LooksForward(new long[] { 2108, 281496 }, "", null));
    }

    [Fact]
    public void ForwardAnswerToABackwardRequestIsDetected()
    {
        // A server that ignores direction=backward returns the OLDEST turns
        // and a forward cursor (the last idx on the page).
        Assert.True(TurnPaging.LooksForward(new long[] { 2108, 281496, 584099 }, "584099", null));
        // Asked for turns before a cursor, it returns turns after it.
        Assert.True(TurnPaging.LooksForward(new long[] { 1133907, 1146787 }, "1146787", 584099));
        Assert.True(TurnPaging.LooksForward(new long[] { 1220865, 1222696 }, "", 1218943));
    }

    [Fact]
    public void BeforeCutsNewestPagesFromAForwardWalk()
    {
        var (latest, next) = TurnPaging.Before(Chat, i => i, null, 4);
        Assert.Equal(new long[] { 1202754, 1218943, 1220865, 1222696 }, latest);
        Assert.Equal("1202754", next);

        var (older, olderNext) = TurnPaging.Before(Chat, i => i, long.Parse(next), 4);
        Assert.Equal(new long[] { 1133907, 1146787, 1190239 }.Prepend(584099).ToArray(), older);
        Assert.Equal("584099", olderNext);

        var (first, firstNext) = TurnPaging.Before(Chat, i => i, long.Parse(olderNext), 4);
        Assert.Equal(new long[] { 2108, 281496 }, first);
        Assert.Equal("", firstNext);

        // Walking all pages back covers every turn exactly once, in order.
        var all = first.Concat(older).Concat(latest).ToArray();
        Assert.Equal(Chat, all);
    }

    [Fact]
    public void BeforeHandlesShortChatsAndHugeIndexes()
    {
        var (page, next) = TurnPaging.Before(new long[] { 5, 9 }, i => i, null, 40);
        Assert.Equal(new long[] { 5, 9 }, page);
        Assert.Equal("", next);
        var huge = new[] { 179533922286017891L, 179533922286017892L, 179533922286017899L };
        var (tail, cursor) = TurnPaging.Before(huge, i => i, null, 2);
        Assert.Equal(new[] { 179533922286017892L, 179533922286017899L }, tail);
        Assert.Equal("179533922286017892", cursor);
        var (head, done) = TurnPaging.Before(huge, i => i, long.Parse(cursor), 2);
        Assert.Equal(new[] { 179533922286017891L }, head);
        Assert.Equal("", done);
    }
}

public class TranscriptDedupeTests
{
    private sealed class Element { }

    // Feeds rows in order; returns the texts shown (a superseded copy is removed).
    private static List<string> Render(TranscriptDedupe dedupe, List<(string Text, Element Element)> screen, params (string Role, string Text, bool Sourced)[] rows)
    {
        foreach (var (role, text, sourced) in rows)
        {
            var signature = TranscriptDedupe.Signature(role, text);
            var last = screen.Count > 0 ? screen[^1].Element : null;
            if (!dedupe.Admit(signature, sourced, last, out var superseded)) continue;
            var element = new Element();
            screen.Add((role + ": " + text, element));
            dedupe.Shown(signature, sourced, element);
            if (superseded is not null) screen.RemoveAll(s => ReferenceEquals(s.Element, superseded));
        }
        return screen.Select(s => s.Text).ToList();
    }

    private const string Prompt = "See you don't get the point the cm-ui is the ui app";
    private const string ReplyA = "I've read the product spec end to end; waiting on the explorers.";
    private const string ReplyB = "Understood, and that changes my recommendation: native stays.";
    private const string Leave = "I leave it to you and get this fixed, use any agent you want";

    [Fact]
    public void AdjacentRepeatOfAPromptIsShownOnce()
    {
        // Captured prompt, then the import's copy (reminder stripped).
        var shown = Render(new TranscriptDedupe(), new(), ("user", Prompt, true), ("user", Prompt + "\n", false), ("assistant", ReplyB, true));
        Assert.Equal(new[] { "user: " + Prompt, "assistant: " + ReplyB }, shown);
    }

    [Fact]
    public void ReplayedBlockOfEarlierRepliesIsHidden()
    {
        // As stored for "Memory redesign": four earlier replies stored again
        // (no source time) after a later prompt.
        var shown = Render(new TranscriptDedupe(), new(),
            ("assistant", ReplyA, true), ("user", Prompt, true), ("assistant", ReplyB, true), ("user", Leave, true),
            ("assistant", ReplyA, false), ("assistant", ReplyB, false), ("user", Leave, false),
            ("assistant", "I'll start with capture.", true));
        Assert.Equal(new[] { "assistant: " + ReplyA, "user: " + Prompt, "assistant: " + ReplyB, "user: " + Leave, "assistant: I'll start with capture." }, shown);
    }

    [Fact]
    public void CapturedRepeatsAndShortRepliesStay()
    {
        // A person may send the same long message twice (both captured) and
        // short answers like "yes" many times; only adjacent repeats collapse.
        var shown = Render(new TranscriptDedupe(), new(),
            ("user", Prompt, true), ("assistant", ReplyA, true), ("user", Prompt, true),
            ("user", "yes", true), ("assistant", ReplyB, true), ("user", "yes", false));
        Assert.Equal(6, shown.Count);
    }

    [Fact]
    public void OriginalOnAnOlderPageSupersedesACopyShownFirst()
    {
        var dedupe = new TranscriptDedupe();
        var screen = new List<(string Text, Element Element)>();
        // Newest page first: it holds only the copy.
        Render(dedupe, screen, ("user", Leave, false), ("assistant", "Next step.", true));
        // The older page (prepended later) holds the captured original.
        var older = new List<(string Text, Element Element)>();
        foreach (var (role, text, sourced) in new[] { ("user", Leave, true) })
        {
            var signature = TranscriptDedupe.Signature(role, text);
            Assert.True(dedupe.Admit(signature, sourced, null, out var superseded));
            var element = new Element();
            older.Add((role + ": " + text, element));
            dedupe.Shown(signature, sourced, element);
            Assert.NotNull(superseded);
            screen.RemoveAll(s => ReferenceEquals(s.Element, superseded));
        }
        var all = older.Concat(screen).Select(s => s.Text).ToList();
        Assert.Equal(new[] { "user: " + Leave, "assistant: Next step." }, all);
    }

    [Fact]
    public void ConversationIndexKeepsOneEntryPerStoredTwiceMessage()
    {
        var index = new ConversationIndex();
        // Newest page indexed first: the copy.
        index.Add(new("turn:1684734:user", "user", Leave, "", -1, 1684734), copy: true);
        index.Add(new("turn:1683464", "assistant", ReplyB, "", -1, 1683464), copy: true);
        // The history walk brings the captured originals.
        index.Add(new("turn:1650130:user", "user", Leave, "", 0, 1650130));
        index.Add(new("turn:1480926", "assistant", ReplyB, "", 0, 1480926));
        // A later copy of an indexed message is left out.
        index.Add(new("turn:1700000", "assistant", ReplyB, "", 0, 1700000), copy: true);
        Assert.Equal(new[] { "turn:1480926", "turn:1650130:user" }, index.Entries.Select(e => e.Key).ToArray());
        Assert.Single(index.Prompts);
    }

    // As stored for "Memory redesign" (idx 8629809, 8630583, 8652810): two
    // queued prompts captured on their own, then the import's joined turn.
    private const string QueuedA = "You are not looking typography and also the issue is the ui and the way ui behave it feels to heavy is my issue";
    private const string QueuedB = "See the ui u shoed and that of t3 the typography and fonts spacing not looks good I gave you ref multiple time of the t3 code then also you aren't getting that";

    [Fact]
    public void JoinedQueuedPromptsAreHiddenAndThePartsStay()
    {
        var shown = Render(new TranscriptDedupe(), new(),
            ("assistant", ReplyA, true), ("user", QueuedA, true), ("user", QueuedB, true),
            ("user", QueuedA + " \n\n" + QueuedB, false), ("assistant", ReplyB, true));
        Assert.Equal(new[] { "assistant: " + ReplyA, "user: " + QueuedA, "user: " + QueuedB, "assistant: " + ReplyB }, shown);
    }

    [Fact]
    public void JoinedInterruptNoteAndPromptIsHidden()
    {
        const string Interrupt = "[Request interrupted by user]";
        Assert.True(TranscriptDedupe.IsInterruptNote(Interrupt));
        Assert.False(TranscriptDedupe.IsInterruptNote(Interrupt + "\n\ncontinue afer agetns finish"));
        var shown = Render(new TranscriptDedupe(), new(),
            ("assistant", ReplyA, true), ("user", Interrupt, true), ("user", "continue afer agetns finish", true),
            ("user", Interrupt + "\n\ncontinue afer agetns finish", false));
        Assert.Equal(3, shown.Count);
    }

    [Fact]
    public void JoinedTextAfterAReplyOrOfOnePromptStays()
    {
        // Not adjacent to the parts (a reply between) or only one part: shown.
        var shown = Render(new TranscriptDedupe(), new(),
            ("user", QueuedA, true), ("user", QueuedB, true), ("assistant", ReplyA, true),
            ("user", QueuedA + "\n\n" + QueuedB, false));
        Assert.Equal(4, shown.Count);
        Assert.False(TranscriptDedupe.JoinsPrompts(new[] { QueuedA }, QueuedA));
    }

    [Fact]
    public void OfTwoAdjacentCompactionSummariesOnlyTheLastStays()
    {
        const string Lead = "This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation.\n\nSummary:\n1. Primary Request";
        Assert.True(TranscriptDedupe.IsCompactionSummary(Lead));
        var shown = Render(new TranscriptDedupe(), new(),
            ("assistant", ReplyA, true), ("user", Lead + " and Intent: cut here…", true), ("user", Lead + " and Intent: cut later…", false),
            ("assistant", ReplyB, false));
        Assert.Equal(new[] { "assistant: " + ReplyA, "user: " + Lead + " and Intent: cut later…", "assistant: " + ReplyB }, shown);
    }

    [Fact]
    public void ConversationIndexSkipsJoinedQueuedPromptsAndSummaries()
    {
        var index = new ConversationIndex();
        index.Add(new("turn:8628671", "assistant", ReplyA, "", 0, 8628671));
        index.Add(new("turn:8629809:user", "user", QueuedA, "", 0, 8629809));
        index.Add(new("turn:8630583:user", "user", QueuedB, "", 0, 8630583));
        index.Add(new("turn:8652810:user", "user", QueuedA + " \n\n" + QueuedB, "", 0, 8652810), copy: true);
        index.Add(new("turn:23589245:user", "user", "This session is being continued from a previous conversation that ran out of context.", "", 0, 23589245));
        index.Add(new("turn:41635122:user", "user", "[Request interrupted by user]", "", 0, 41635122));
        Assert.Equal(new[] { "turn:8629809:user", "turn:8630583:user" }, index.Prompts.Select(e => e.Key).ToArray());
        Assert.Equal(4, index.Entries.Count);
    }
}

public class TailProbeTests
{
    [Fact]
    public void ProbesDoubleUntilTheEndIsBracketed()
    {
        Assert.Equal(new long[] { 400, 800, 1600 }, TurnPaging.TailProbes(200, null, 3));
        Assert.All(TurnPaging.TailProbes(1_000_000_000, null, 8), c => Assert.True(c <= TurnPaging.MaxCursor));
    }

    [Fact]
    public void ProbesSplitTheBracket()
    {
        var probes = TurnPaging.TailProbes(100, 1000, 8);
        Assert.Equal(8, probes.Count);
        Assert.All(probes, c => Assert.InRange(c, 101, 999));
        Assert.Equal(probes.OrderBy(c => c), probes);
        Assert.Empty(TurnPaging.TailProbes(5, 6, 8));
    }

    // Replays the desktop's search (SessionPage.ProbeTailAsync) on a chat
    // shaped like the real 2,284-turn one: sparse byte-offset indexes up to
    // ~57.6M. A probe answers with up to 200 turns after its cursor.
    [Theory]
    [InlineData(2284, 1)]
    [InlineData(2284, 2)]
    [InlineData(900, 3)]
    [InlineData(20000, 4)]
    public void SearchFindsTheTailInFewRounds(int turns, int seed)
    {
        var random = new Random(seed);
        var idx = new List<long> { 2108 };
        for (var i = 1; i < turns; i++) idx.Add(idx[^1] + (long)Math.Exp(random.NextDouble() * 12) + 1);
        (List<long> Items, bool More) Fetch(long cursor)
        {
            if (cursor > TurnPaging.MaxCursor) return (new(), false);
            var after = idx.Where(i => i > cursor).ToList();
            return (after.Take(200).ToList(), after.Count > 200);
        }
        long lo = Fetch(-1).Items[^1];
        long? hi = null;
        for (var round = 1; round <= 5; round++)
        {
            foreach (var cursor in TurnPaging.TailProbes(lo, hi, 8))
            {
                var (items, more) = Fetch(cursor);
                if (!more && items.Count >= 40)
                {
                    Assert.Equal(idx[^1], items[^1]);
                    return;
                }
                if (more) lo = Math.Max(lo, items[^1]);
                else hi = hi is { } h ? Math.Min(h, cursor) : cursor;
            }
        }
        Assert.Fail("tail not found in 5 rounds");
    }
}
