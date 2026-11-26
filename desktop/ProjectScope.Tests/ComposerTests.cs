#nullable enable
using Xunit;
using static Nexus.Core.ComposerKeys;
using Action = Nexus.Core.ComposerKeys.Action;

namespace Nexus.Core.Tests;

public class ComposerDraftStoreTests : IDisposable
{
    private readonly string _dir = Path.Combine(Path.GetTempPath(), "nexus-drafts-" + Guid.NewGuid().ToString("N"));
    private string FilePath => Path.Combine(_dir, "drafts.json");

    public void Dispose()
    {
        try { Directory.Delete(_dir, recursive: true); } catch (IOException) { }
    }

    [Fact]
    public void KeepsADraftPerChatAcrossARestart()
    {
        var store = new ComposerDraftStore(FilePath);
        Assert.True(store.Set("chat-a", new ComposerDraft { Text = "half a thought", Caret = 4, Files = { @"C:\repo\notes.md" }, Pastes = { "line\nline\nline" } }));
        Assert.True(store.Set("chat-b", new ComposerDraft { Text = "other chat" }));
        store.Save();

        var reopened = new ComposerDraftStore(FilePath);
        var a = reopened.Get("chat-a");
        Assert.NotNull(a);
        Assert.Equal("half a thought", a!.Text);
        Assert.Equal(4, a.Caret);
        Assert.Equal(new[] { @"C:\repo\notes.md" }, a.Files);
        Assert.Equal(new[] { "line\nline\nline" }, a.Pastes);
        Assert.Equal("other chat", reopened.Get("CHAT-B")?.Text);
        Assert.Null(reopened.Get("chat-c"));
    }

    [Fact]
    public void AnEmptyDraftForgetsTheChat()
    {
        var store = new ComposerDraftStore(FilePath);
        store.Set("chat", new ComposerDraft { Text = "draft" });
        Assert.True(store.Set("chat", new ComposerDraft { Text = "   " }));
        Assert.False(store.Has("chat"));
        Assert.False(store.Set("chat", new ComposerDraft()));
    }

    [Fact]
    public void AttachmentsAloneAreADraft()
    {
        var store = new ComposerDraftStore(FilePath);
        Assert.True(store.Set("chat", new ComposerDraft { Images = { @"C:\shot.png" } }));
        Assert.True(store.Has("chat"));
    }

    [Fact]
    public void SameDraftIsNotAChange()
    {
        var store = new ComposerDraftStore(FilePath);
        Assert.True(store.Set("chat", new ComposerDraft { Text = "x", Caret = 1 }));
        Assert.False(store.Set("chat", new ComposerDraft { Text = "x", Caret = 1 }));
        Assert.True(store.Set("chat", new ComposerDraft { Text = "x", Caret = 0 }));
    }

    [Fact]
    public void SendRemovesTheDraft()
    {
        var store = new ComposerDraftStore(FilePath);
        store.Set("chat", new ComposerDraft { Text = "send me" });
        store.Save();
        Assert.True(store.Remove("chat"));
        store.Save();
        Assert.Null(new ComposerDraftStore(FilePath).Get("chat"));
    }

    [Fact]
    public void CaretIsClampedToTheText()
    {
        var store = new ComposerDraftStore(FilePath);
        store.Set("chat", new ComposerDraft { Text = "abc", Caret = 99 });
        Assert.Equal(3, store.Get("chat")!.Caret);
    }

    [Fact]
    public void ADamagedFileLosesOnlyOldDrafts()
    {
        Directory.CreateDirectory(_dir);
        File.WriteAllText(FilePath, "{ not json");
        var store = new ComposerDraftStore(FilePath);
        Assert.Null(store.Get("chat"));
        store.Set("chat", new ComposerDraft { Text = "fresh" });
        store.Save();
        Assert.Equal("fresh", new ComposerDraftStore(FilePath).Get("chat")?.Text);
    }

    [Fact]
    public void OnlyTheNewestDraftsAreKept()
    {
        var clock = new DateTimeOffset(2026, 11, 26, 9, 0, 0, TimeSpan.Zero);
        var store = new ComposerDraftStore(FilePath, () => clock);
        for (var i = 0; i < ComposerDraftStore.MaxDrafts + 5; i++)
        {
            clock = clock.AddSeconds(1);
            store.Set("chat-" + i, new ComposerDraft { Text = "draft " + i });
        }
        store.Save();
        var reopened = new ComposerDraftStore(FilePath);
        Assert.Null(reopened.Get("chat-0"));
        Assert.Null(reopened.Get("chat-4"));
        Assert.NotNull(reopened.Get("chat-5"));
        Assert.NotNull(reopened.Get("chat-" + (ComposerDraftStore.MaxDrafts + 4)));
    }
}

public class ComposerKeysTests
{
    private static Press Enter(bool shift = false, bool primary = false, bool alt = false, bool composing = false) =>
        new(Key.Enter, shift, primary, alt, composing);

    [Fact]
    public void EnterSendsAndShiftEnterIsANewLine()
    {
        Assert.Equal(Action.Send, Decide(Enter(), Run.Idle, hasContent: true));
        Assert.Equal(Action.None, Decide(Enter(shift: true), Run.Idle, hasContent: true));
        Assert.Equal(Action.None, Decide(Enter(alt: true), Run.Idle, hasContent: true));
        // Ctrl/Cmd+Enter sends too (Cursor, Copilot muscle memory).
        Assert.Equal(Action.Send, Decide(Enter(primary: true), Run.Idle, hasContent: true));
    }

    [Fact]
    public void EnterOnAnEmptyComposerDoesNothingVisible()
    {
        Assert.Equal(Action.Swallow, Decide(Enter(), Run.Idle, hasContent: false));
        Assert.Equal(Action.None, Decide(Enter(shift: true), Run.Idle, hasContent: false));
    }

    [Fact]
    public void WhileTheAgentWorksEnterQueuesAndPrimaryEnterSendsNow()
    {
        Assert.Equal(Action.Queue, Decide(Enter(), Run.Live, hasContent: true));
        Assert.Equal(Action.SendNow, Decide(Enter(primary: true), Run.Live, hasContent: true));
        Assert.Equal(Action.None, Decide(Enter(shift: true), Run.Live, hasContent: true));
        Assert.Equal(Action.Swallow, Decide(Enter(), Run.Live, hasContent: false));
    }

    [Fact]
    public void WhileASendIsStartingEnterKeepsTheText()
    {
        Assert.Equal(Action.Swallow, Decide(Enter(), Run.Busy, hasContent: true));
        Assert.Equal(Action.Swallow, Decide(Enter(primary: true), Run.Busy, hasContent: true));
    }

    [Fact]
    public void AnInputMethodComposingOwnsEveryKey()
    {
        Assert.Equal(Action.None, Decide(Enter(composing: true), Run.Idle, hasContent: true));
        Assert.Equal(Action.None, Decide(Enter(composing: true), Run.Live, hasContent: true));
        Assert.Equal(Action.None, Decide(new Press(Key.Escape, Composing: true), Run.Live, hasContent: true));
    }

    [Fact]
    public void EscStopsARunningReply()
    {
        Assert.Equal(Action.Stop, Decide(new Press(Key.Escape), Run.Live, hasContent: false));
        Assert.Equal(Action.Stop, Decide(new Press(Key.Escape), Run.Busy, hasContent: true));
        Assert.Equal(Action.None, Decide(new Press(Key.Escape), Run.Idle, hasContent: true));
        // An open flyout takes Esc first.
        Assert.Equal(Action.None, Decide(new Press(Key.Escape), Run.Live, hasContent: false, flyoutOpen: true));
    }

    [Fact]
    public void PrimaryShiftBackspaceStopsAndPrimaryDownJumps()
    {
        Assert.Equal(Action.Stop, Decide(new Press(Key.Backspace, Shift: true, Primary: true), Run.Live, false));
        Assert.Equal(Action.None, Decide(new Press(Key.Backspace, Shift: true, Primary: true), Run.Idle, false));
        Assert.Equal(Action.None, Decide(new Press(Key.Backspace), Run.Live, false));
        Assert.Equal(Action.JumpToLatest, Decide(new Press(Key.Down, Primary: true), Run.Idle, false));
        Assert.Equal(Action.None, Decide(new Press(Key.Down), Run.Idle, false));
    }

    [Fact]
    public void TooltipsNameTheShortcuts()
    {
        Assert.Equal("Send · Enter (Shift+Enter for a new line)", SendTip(Run.Idle, k => "Ctrl+" + k));
        Assert.Contains("⌘Enter sends now", SendTip(Run.Live, k => "⌘" + k));
        Assert.Equal("Stop · Esc", StopTip);
    }
}

public class OutgoingMessageTests
{
    [Fact]
    public void AMessageGoesPendingSendingReplyingSent()
    {
        var m = new OutgoingMessage("chat", "fix the test");
        Assert.Equal(OutgoingMessage.State.Pending, m.Current);
        Assert.True(m.IsOpen);
        Assert.True(m.ChecksPassed());
        Assert.Equal(OutgoingMessage.State.Sending, m.Current);
        Assert.True(m.Accepted());
        Assert.Equal(OutgoingMessage.State.Replying, m.Current);
        Assert.True(m.Completed());
        Assert.Equal(OutgoingMessage.State.Sent, m.Current);
        Assert.False(m.IsOpen);
        Assert.False(m.Fail("late"));
    }

    [Fact]
    public void ACheckThatFailsLeavesItNotSent()
    {
        var m = new OutgoingMessage("chat", "hello");
        Assert.True(m.Fail("Claude Code isn't signed in."));
        Assert.Equal(OutgoingMessage.FailStage.NotSent, m.Stage);
        Assert.Equal("Not sent", m.FailureLabel);
        Assert.Equal("Claude Code isn't signed in.", m.Reason);
    }

    [Fact]
    public void FailingAfterTheAgentTookItIsNoReplyOrStopped()
    {
        var silent = new OutgoingMessage("chat", "hello");
        silent.ChecksPassed(); silent.Accepted();
        silent.Fail("The agent stopped.");
        Assert.Equal("No reply", silent.FailureLabel);

        var partial = new OutgoingMessage("chat", "hello");
        partial.ChecksPassed(); partial.Accepted();
        partial.Fail("Connection lost", hadOutput: true);
        Assert.Equal(OutgoingMessage.FailStage.Interrupted, partial.Stage);
        Assert.Equal("Stopped", partial.FailureLabel);
    }

    [Fact]
    public void RetrySendsTheSameMessageAgain()
    {
        var m = new OutgoingMessage("chat", "hello", images: new[] { "a.png" }, files: new[] { "b.txt" });
        m.Fail("offline");
        Assert.True(m.Retry());
        Assert.Equal(OutgoingMessage.State.Pending, m.Current);
        Assert.Equal(2, m.Attempts);
        Assert.Equal("", m.Reason);
        Assert.Equal(OutgoingMessage.FailStage.None, m.Stage);
        Assert.Equal(new[] { "a.png" }, m.Images);
        Assert.Equal(new[] { "b.txt" }, m.Files);
        Assert.False(m.Retry());
    }

    [Fact]
    public void EditWithdrawsOnlyAFailedMessage()
    {
        var m = new OutgoingMessage("chat", "hello");
        Assert.False(m.Withdraw());
        m.Fail("x");
        Assert.True(m.Withdraw());
        Assert.Equal(OutgoingMessage.State.Withdrawn, m.Current);
        Assert.False(m.Retry());
    }

    [Fact]
    public void ClosingTheChatDuringTheChecksDetachesIt()
    {
        var m = new OutgoingMessage("chat", "hello");
        Assert.True(m.Detach());
        Assert.False(m.ChecksPassed());
        var replying = new OutgoingMessage("chat", "hello");
        replying.ChecksPassed(); replying.Accepted();
        Assert.False(replying.Detach());
    }

    [Fact]
    public void TypingAfterSendDoesNotChangeTheMessage()
    {
        var typed = "first";
        var m = new OutgoingMessage("chat", typed);
        typed = "first, and more";
        Assert.Equal("first", m.Text);
        Assert.Equal("first", m.Prompt);
    }

    [Fact]
    public void PastedBlocksFollowTheTypedText()
    {
        var m = new OutgoingMessage("chat", "Why does this fail?", pastes: new[] { "stack line 1\nstack line 2\n" });
        Assert.Equal("Why does this fail?\n\nstack line 1\nstack line 2", m.Prompt);
        Assert.Equal("only paste", new OutgoingMessage("chat", "", pastes: new[] { "only paste" }).Prompt);
    }

    [Fact]
    public void ALongReasonIsShortenedWithDetails()
    {
        var m = new OutgoingMessage("chat", "x");
        m.Fail("exit status 1\nTraceback (most recent call last):\n  File \"x.py\"");
        var (shortReason, details) = m.ShortReason();
        Assert.Equal("exit status 1", shortReason);
        Assert.True(details);
        var one = new OutgoingMessage("chat", "x");
        one.Fail("Offline");
        Assert.Equal(("Offline", false), one.ShortReason());
    }
}

public class PastedTextTests
{
    [Fact]
    public void BigPastesBecomeChips()
    {
        Assert.False(PastedText.IsLarge("a short sentence"));
        Assert.False(PastedText.IsLarge(string.Join("\n", Enumerable.Repeat("x", 5))));
        Assert.True(PastedText.IsLarge(string.Join("\n", Enumerable.Repeat("a log line that is long enough", 14))));
        Assert.True(PastedText.IsLarge(new string('x', 4000)));
    }

    [Fact]
    public void ChipLabelCountsLines()
    {
        Assert.Equal("Pasted text · 3 lines", PastedText.Label("a\nb\nc\n"));
        Assert.Equal("Pasted text · 1 line", PastedText.Label("abc"));
        Assert.Equal("Pasted text · 1,200 lines", PastedText.Label(string.Join("\n", Enumerable.Repeat("x", 1200))));
        Assert.Equal("func main() {", PastedText.Preview("\n\n  func main() {\n}"));
        // A text box hands pasted text back with "\r" line breaks.
        var fromTextBox = string.Join("\r", Enumerable.Repeat("2026-11-26 ERROR checkout: charge retry failed order_key=ok_1", 40));
        Assert.True(PastedText.IsLarge(fromTextBox));
        Assert.Equal("Pasted text · 40 lines", PastedText.Label(fromTextBox));
        Assert.Equal("a\nb\nc", PastedText.Normalize("a\r\nb\rc"));
    }
}

public class TurnStatusTests
{
    private static (TurnStatus Status, System.Action<int> Advance) Make()
    {
        var now = new DateTimeOffset(2026, 11, 26, 9, 0, 0, TimeSpan.Zero);
        var status = new TurnStatus("Claude Code", now, () => now);
        return (status, seconds => now = now.AddSeconds(seconds));
    }

    [Fact]
    public void FollowsTheTurnFromStartToWriting()
    {
        var (s, advance) = Make();
        Assert.Equal("Starting Claude Code… · 0s · Esc to stop", s.Line());
        Assert.True(s.Apply("status", "Claude Code is ready"));
        Assert.Equal("Thinking", s.Verb);
        Assert.Equal("", s.Notice);
        advance(12);
        Assert.True(s.Apply("tool_start", tool: "Bash", toolInput: "{\"command\":\"npm test\"}"));
        Assert.Equal("Running npm test… · 12s · Esc to stop", s.Line());
        s.Apply("tool_end");
        Assert.Equal("Thinking", s.Verb);
        s.Apply("text_delta", "Hi");
        Assert.Equal("Writing", s.Verb);
        Assert.True(s.HasText);
    }

    [Fact]
    public void ApprovalAndStoppingDropTheEscHint()
    {
        var (s, _) = Make();
        s.Apply("approval");
        Assert.Equal("Waiting for your approval… · 0s", s.Line());
        s.Apply("approval_resolved");
        Assert.Equal("Thinking", s.Verb);
        s.Stopping();
        Assert.False(s.Apply("text_delta"));
        Assert.Equal("Stopping… · 0s", s.Line());
    }

    [Fact]
    public void NoticesStayReadyNoticesDoNot()
    {
        var (s, _) = Make();
        s.Apply("status", "Model \"x\" isn't available in Gemini's chat mode; using its default model.");
        Assert.StartsWith("Model", s.Notice);
        Assert.True(TurnStatus.IsReadyNotice("Connected"));
        Assert.True(TurnStatus.IsReadyNotice("Codex is ready"));
        Assert.False(TurnStatus.IsReadyNotice("Conversation compacted"));
    }

    [Fact]
    public void ElapsedReadsLikeAClock()
    {
        Assert.Equal("4s", TurnStatus.FormatElapsed(TimeSpan.FromSeconds(4.7)));
        Assert.Equal("1m 05s", TurnStatus.FormatElapsed(TimeSpan.FromSeconds(65)));
        Assert.Equal("1h 02m", TurnStatus.FormatElapsed(TimeSpan.FromMinutes(62)));
        Assert.Equal("0s", TurnStatus.FormatElapsed(TimeSpan.FromSeconds(-3)));
    }

    [Fact]
    public void AReopenedTurnCountsFromWhenItStarted()
    {
        var (s, _) = Make();
        s.StartedSince(s.StartedAt.AddSeconds(-42));
        Assert.Equal("42s", TurnStatus.FormatElapsed(s.Elapsed));
    }

    [Fact]
    public void ToolsReadAsPlainWords()
    {
        Assert.Equal("Editing retry.ts", TurnStatus.DescribeTool("Edit", "{\"file_path\":\"C:\\\\repo\\\\src\\\\retry.ts\"}"));
        Assert.Equal("Reading README.md", TurnStatus.DescribeTool("Read", "{\"file_path\":\"/repo/README.md\"}"));
        Assert.Equal("Searching for TODO", TurnStatus.DescribeTool("Grep", "{\"pattern\":\"TODO\"}"));
        Assert.Equal("Using nexus · memory search", TurnStatus.DescribeTool("mcp__nexus__memory_search", "{}"));
        Assert.Equal("Running WebSearch", TurnStatus.DescribeTool("WebSearch", "not json"));
        Assert.StartsWith("Running npm run build -- --filter", TurnStatus.DescribeTool("Bash", "{\"command\":\"npm run build -- --filter web --filter api --filter docs\"}"));
    }
}
