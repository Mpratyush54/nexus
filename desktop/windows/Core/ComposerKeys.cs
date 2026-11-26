#nullable enable
namespace Nexus.Core;

/// <summary>
/// What a key press in the chat composer does. Pure, so the rules are tested
/// without a window; the page maps its KeyDown to <see cref="Press"/>.
///
/// Enter sends and Shift+Enter starts a new line, as on Home. While the
/// agent works, Enter queues the message behind the running reply and
/// Ctrl/Cmd+Enter sends it now (stopping the reply). Esc stops the reply.
/// Nothing fires while an input method is composing.
/// </summary>
internal static class ComposerKeys
{
    public enum Key { Other, Enter, Escape, Backspace, Down }

    /// <summary>What the chat is doing when the key is pressed.</summary>
    public enum Run
    {
        /// <summary>Nothing running: Enter sends.</summary>
        Idle,
        /// <summary>A live agent is replying: Enter queues, Ctrl/Cmd+Enter sends now.</summary>
        Live,
        /// <summary>A reply that can't take follow-ups (one-shot runs, or checks before a send).</summary>
        Busy
    }

    public enum Action
    {
        /// <summary>Let the text box handle it (type, new line).</summary>
        None,
        Send,
        Queue,
        SendNow,
        Stop,
        /// <summary>Swallow the key: a send is starting and Enter must not add a new line.</summary>
        Swallow,
        JumpToLatest
    }

    public readonly record struct Press(Key Key, bool Shift = false, bool Primary = false, bool Alt = false, bool Composing = false);

    public static Action Decide(Press p, Run run, bool hasContent, bool flyoutOpen = false)
    {
        if (p.Composing) return Action.None;
        switch (p.Key)
        {
            case Key.Enter:
                if (p.Shift || p.Alt) return Action.None; // a new line
                if (run == Run.Busy) return Action.Swallow;
                if (!hasContent) return Action.Swallow;
                if (run == Run.Live) return p.Primary ? Action.SendNow : Action.Queue;
                return Action.Send;
            case Key.Escape:
                return run != Run.Idle && !flyoutOpen && !p.Shift && !p.Primary ? Action.Stop : Action.None;
            case Key.Backspace:
                // Ctrl/Cmd+Shift+Backspace stops from anywhere in the chat (Cursor).
                return p.Primary && p.Shift && run != Run.Idle ? Action.Stop : Action.None;
            case Key.Down:
                return p.Primary && !p.Shift && !p.Alt ? Action.JumpToLatest : Action.None;
            default:
                return Action.None;
        }
    }

    /// <summary>The send button's tooltip for the state it is in.</summary>
    public static string SendTip(Run run, Func<string, string> label) => run == Run.Live
        ? "Queue · Enter sends it when this reply finishes (" + label("Enter") + " sends now)"
        : "Send · Enter (Shift+Enter for a new line)";

    public const string StopTip = "Stop · Esc";
}
