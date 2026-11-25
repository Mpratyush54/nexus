#nullable enable
namespace Nexus.Core;

/// <summary>
/// One message from the composer, from the moment Enter is pressed until the
/// reply is saved or the send fails. The bubble appears at once (Pending);
/// the agent checks run after that. A failure keeps the message on screen
/// with Retry and Edit; nothing the person typed is ever thrown away.
///
///   Pending ──checks ok──▶ Sending ──agent took it──▶ Replying ──▶ Sent
///      │                     │                          │
///      └──────────── Failed ◀┴──────────────────────────┘
///   Failed ──Retry──▶ Pending        Failed ──Edit──▶ Withdrawn
///   Pending/Sending ──chat closed──▶ Detached (a live agent keeps it)
/// </summary>
internal sealed class OutgoingMessage
{
    public enum State { Pending, Sending, Replying, Sent, Failed, Withdrawn, Detached }

    /// <summary>Where a send failed, which decides what the person is told.</summary>
    public enum FailStage
    {
        None,
        /// <summary>Before the agent got it (agent missing, check failed, live.send refused).</summary>
        NotSent,
        /// <summary>The agent took it and stopped without replying.</summary>
        NoReply,
        /// <summary>The agent replied part of the way, then failed.</summary>
        Interrupted
    }

    public OutgoingMessage(string sessionId, string text, IReadOnlyList<string>? images = null, IReadOnlyList<string>? files = null, IReadOnlyList<string>? pastes = null)
    {
        SessionId = sessionId;
        Text = text;
        Images = images?.ToArray() ?? Array.Empty<string>();
        Files = files?.ToArray() ?? Array.Empty<string>();
        Pastes = pastes?.ToArray() ?? Array.Empty<string>();
    }

    public string SessionId { get; }
    /// <summary>What was typed (the bubble shows this; Edit puts it back).</summary>
    public string Text { get; }
    public IReadOnlyList<string> Images { get; }
    public IReadOnlyList<string> Files { get; }
    /// <summary>Large pasted blocks sent after the typed text.</summary>
    public IReadOnlyList<string> Pastes { get; }

    public State Current { get; private set; } = State.Pending;
    public FailStage Stage { get; private set; }
    public string Reason { get; private set; } = "";
    public int Attempts { get; private set; } = 1;

    public bool IsOpen => Current is State.Pending or State.Sending or State.Replying;

    /// <summary>The prompt the agent gets: the typed text, then each pasted block.</summary>
    public string Prompt => PastedText.Join(Text, Pastes);

    private bool Move(State from, State to)
    {
        if (Current != from) return false;
        Current = to;
        return true;
    }

    /// <summary>The checks passed and the message is going to the agent.</summary>
    public bool ChecksPassed() => Move(State.Pending, State.Sending);

    /// <summary>The agent (or the core's queue) accepted the message.</summary>
    public bool Accepted() => Move(State.Sending, State.Replying);

    /// <summary>The reply was saved.</summary>
    public bool Completed()
    {
        if (Current is not (State.Sending or State.Replying)) return false;
        Current = State.Sent;
        return true;
    }

    /// <summary>
    /// The send failed. Before the agent accepted it the message was not
    /// sent; after, it either got no reply or a partial one.
    /// </summary>
    public bool Fail(string reason, bool hadOutput = false)
    {
        if (!IsOpen) return false;
        Stage = Current == State.Replying ? (hadOutput ? FailStage.Interrupted : FailStage.NoReply) : FailStage.NotSent;
        Reason = (reason ?? "").Trim();
        Current = State.Failed;
        return true;
    }

    /// <summary>Retry: the same message goes again.</summary>
    public bool Retry()
    {
        if (Current != State.Failed) return false;
        Current = State.Pending;
        Stage = FailStage.None;
        Reason = "";
        Attempts++;
        return true;
    }

    /// <summary>Edit: the message goes back to the composer and leaves the chat.</summary>
    public bool Withdraw()
    {
        if (Current != State.Failed) return false;
        Current = State.Withdrawn;
        return true;
    }

    /// <summary>The chat closed while the checks ran; a live agent still gets it.</summary>
    public bool Detach()
    {
        if (Current is not (State.Pending or State.Sending)) return false;
        Current = State.Detached;
        return true;
    }

    /// <summary>"Not sent", "No reply" or "Stopped": the first words of the failure line.</summary>
    public string FailureLabel => Stage switch
    {
        FailStage.NotSent => "Not sent",
        FailStage.NoReply => "No reply",
        FailStage.Interrupted => "Stopped",
        _ => ""
    };

    /// <summary>The failure line's short reason (first line, at most 90 chars) and whether there is more to show.</summary>
    public (string Short, bool HasDetails) ShortReason()
    {
        var reason = Reason.Replace("\r", "");
        if (reason.Length == 0) return ("", false);
        var first = reason.Split('\n', 2)[0].Trim();
        var clipped = first.Length > 90 ? first[..89].TrimEnd() + "…" : first;
        return (clipped, clipped != reason.Trim());
    }
}

/// <summary>Large pasted text: kept as a chip in the composer, sent after the typed text.</summary>
internal static class PastedText
{
    public const int MinLines = 12;
    public const int MinChars = 1200;

    /// <summary>A paste this big becomes a chip instead of flooding the composer.</summary>
    public static bool IsLarge(string? text)
    {
        if (string.IsNullOrEmpty(text)) return false;
        var lines = LineCount(text);
        return text.Length >= 4000 || (lines >= MinLines && text.Length >= 200) || (text.Length >= MinChars && lines >= 4);
    }

    /// <summary>One line ending: a text box hands back "\r" where the clipboard had "\r\n".</summary>
    public static string Normalize(string text) => text.Replace("\r\n", "\n").Replace('\r', '\n');

    public static int LineCount(string text)
    {
        if (text.Length == 0) return 0;
        var lines = 1;
        foreach (var c in Normalize(text).TrimEnd('\n')) if (c == '\n') lines++;
        return lines;
    }

    /// <summary>"Pasted text · 142 lines" (the chip's label).</summary>
    public static string Label(string text)
    {
        var lines = LineCount(text);
        return "Pasted text · " + (lines == 1 ? "1 line" : lines.ToString("N0") + " lines");
    }

    /// <summary>The chip's second line: the first non-empty line, trimmed.</summary>
    public static string Preview(string text, int max = 60)
    {
        var first = Normalize(text).Split('\n').Select(l => l.Trim()).FirstOrDefault(l => l.Length > 0) ?? "";
        return first.Length > max ? first[..(max - 1)] + "…" : first;
    }

    /// <summary>The message as sent: typed text, then each pasted block, separated by blank lines.</summary>
    public static string Join(string typed, IEnumerable<string> pastes)
    {
        var parts = new List<string>();
        if (!string.IsNullOrWhiteSpace(typed)) parts.Add(typed.Trim());
        parts.AddRange(pastes.Where(p => !string.IsNullOrWhiteSpace(p)).Select(p => p.TrimEnd()));
        return string.Join("\n\n", parts);
    }
}
