#nullable enable
using System.Text.Json;

namespace Nexus.Core;

/// <summary>
/// How long an agent thought: from its first thinking to the first words
/// of its reply. Times come from the core's events ("at", and "until" on a
/// replayed run of merged thinking deltas), so a reopened chat that replays
/// the turn at once still says "Thought for 7s", not "1s".
/// </summary>
internal sealed class ThinkingSpan
{
    private readonly Func<DateTimeOffset> _now;

    public ThinkingSpan(DateTimeOffset? startedAt = null, Func<DateTimeOffset>? now = null)
    {
        _now = now ?? (() => DateTimeOffset.Now);
        Started = startedAt ?? _now();
    }

    public DateTimeOffset Started { get; private set; }
    public DateTimeOffset? Ended { get; private set; }
    public bool Finished => Ended is not null;

    /// <summary>The thinking started earlier than this view knew (a replay).</summary>
    public void StartedSince(DateTimeOffset at)
    {
        if (!Finished && at < Started && at <= _now()) Started = at;
    }

    /// <summary>The reply's text started at <paramref name="at"/> (now when unknown). Only the first call counts.</summary>
    public bool Finish(DateTimeOffset? at = null)
    {
        if (Finished) return false;
        var end = at ?? _now();
        Ended = end < Started ? Started : end;
        return true;
    }

    /// <summary>How long so far, or in all once finished.</summary>
    public TimeSpan Duration => (Ended ?? _now()) - Started;

    /// <summary>An event's time in Unix milliseconds ("at", "until"), if it has one.</summary>
    public static DateTimeOffset? EventTime(JsonElement e, string key = "at")
    {
        if (e.ValueKind != JsonValueKind.Object || !e.TryGetProperty(key, out var v)) return null;
        if (v.ValueKind == JsonValueKind.Number && v.TryGetInt64(out var ms) && ms > 946684800000) // after 2000
            return DateTimeOffset.FromUnixTimeMilliseconds(ms).ToLocalTime();
        return null;
    }
}
