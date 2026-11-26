namespace Nexus.Core;

/// <summary>
/// The state behind the toast host (Pages/Toast.cs), without any UI: which
/// notices are showing, when each one expires, and what Undo or Ctrl/Cmd+Z
/// does. At most <see cref="MaxVisible"/> show at once; a fourth pushes the
/// oldest out, which commits it (its expire action runs) exactly as if its
/// time had run out. Each notice ends exactly once: undone, expired,
/// dismissed or flushed.
/// </summary>
internal sealed class ToastQueue
{
    public const int MaxVisible = 3;
    public static readonly TimeSpan PlainDuration = TimeSpan.FromSeconds(5);
    public static readonly TimeSpan UndoDuration = TimeSpan.FromSeconds(8);

    internal enum Outcome { Expired, Undone, Dismissed, Flushed }

    internal sealed class Item
    {
        public long Id { get; init; }
        public string Message { get; init; } = "";
        public string? ActionLabel { get; init; }
        public Action? OnAction { get; init; }
        public Action? OnExpire { get; init; }
        public TimeSpan Duration { get; init; }
        public DateTime ShownAt { get; set; }
        /// <summary>Time left while paused (pointer over the toast).</summary>
        public TimeSpan? PausedRemaining { get; set; }
        public bool HasUndo => OnAction is not null;
    }

    private readonly List<Item> _visible = new();
    private long _next;

    /// <summary>The toasts on screen, oldest first.</summary>
    public IReadOnlyList<Item> Visible => _visible;

    /// <summary>Raised after a toast leaves (for the UI to animate it out).</summary>
    public event Action<Item, Outcome>? Ended;

    /// <summary>
    /// Shows a notice. <paramref name="onAction"/> is the Undo (or other
    /// action) and makes it stay 8 s instead of 5 s. <paramref name="onExpire"/>
    /// runs when it leaves without the action (time up, dismissed, pushed out
    /// or the app closing): that is where a soft delete becomes real.
    /// </summary>
    public Item Show(string message, DateTime now, string? actionLabel = null, Action? onAction = null, Action? onExpire = null, TimeSpan? duration = null)
    {
        var item = new Item
        {
            Id = ++_next,
            Message = message,
            ActionLabel = onAction is null ? null : actionLabel ?? "Undo",
            OnAction = onAction,
            OnExpire = onExpire,
            Duration = duration ?? (onAction is null ? PlainDuration : UndoDuration),
            ShownAt = now
        };
        _visible.Add(item);
        while (_visible.Count > MaxVisible) End(_visible[0], Outcome.Expired);
        return item;
    }

    /// <summary>Ends every toast whose time is up. Call it a few times a second.</summary>
    public void Tick(DateTime now)
    {
        foreach (var item in _visible.ToList())
            if (item.PausedRemaining is null && now - item.ShownAt >= item.Duration) End(item, Outcome.Expired);
    }

    /// <summary>Runs the toast's action (Undo). False when it already left or has none.</summary>
    public bool Act(long id)
    {
        var item = _visible.FirstOrDefault(i => i.Id == id);
        if (item?.OnAction is null) return false;
        End(item, Outcome.Undone);
        return true;
    }

    /// <summary>Ctrl/Cmd+Z: undoes the newest toast that has an Undo. False when none is showing.</summary>
    public bool UndoLatest()
    {
        var item = _visible.LastOrDefault(i => i.HasUndo);
        return item is not null && Act(item.Id);
    }

    /// <summary>The close button: leaves without undoing (a pending delete goes ahead).</summary>
    public void Dismiss(long id)
    {
        if (_visible.FirstOrDefault(i => i.Id == id) is { } item) End(item, Outcome.Dismissed);
    }

    /// <summary>The pointer is over the toast: its time stops until it leaves.</summary>
    public void Pause(long id, DateTime now)
    {
        if (_visible.FirstOrDefault(i => i.Id == id) is { PausedRemaining: null } item)
            item.PausedRemaining = item.Duration - (now - item.ShownAt);
    }

    public void Resume(long id, DateTime now)
    {
        if (_visible.FirstOrDefault(i => i.Id == id) is { PausedRemaining: { } left } item)
        {
            item.PausedRemaining = null;
            // At least a second and a half more after the pointer leaves.
            var remaining = left < TimeSpan.FromSeconds(1.5) ? TimeSpan.FromSeconds(1.5) : left;
            item.ShownAt = now - (item.Duration - remaining);
        }
    }

    /// <summary>The app is closing: everything pending is committed now.</summary>
    public void Flush()
    {
        foreach (var item in _visible.ToList()) End(item, Outcome.Flushed);
    }

    private void End(Item item, Outcome outcome)
    {
        if (!_visible.Remove(item)) return;
        try
        {
            if (outcome == Outcome.Undone) item.OnAction?.Invoke();
            else item.OnExpire?.Invoke();
        }
        finally
        {
            Ended?.Invoke(item, outcome);
        }
    }
}
