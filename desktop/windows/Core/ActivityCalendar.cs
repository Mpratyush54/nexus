#nullable enable
namespace Nexus.Core;

internal sealed record ActivityDay(DateOnly Date, int Count, bool InRange);

internal static class ActivityCalendar
{
    internal static IReadOnlyList<ActivityDay> Build(DateOnly today, IEnumerable<(DateOnly Date, int Count)> days)
    {
        var counts = days.GroupBy(d => d.Date).ToDictionary(g => g.Key, g => g.Max(d => Math.Max(0, d.Count)));
        var start = today.AddDays(-364);
        var aligned = start.AddDays(-(int)start.DayOfWeek);
        var end = today.AddDays(6 - (int)today.DayOfWeek);
        var result = new List<ActivityDay>();
        for (var date = aligned; date <= end; date = date.AddDays(1))
        {
            var inRange = date >= start && date <= today;
            result.Add(new(date, inRange ? counts.GetValueOrDefault(date) : 0, inRange));
        }
        return result;
    }

    internal static int Level(int count) => count <= 0 ? 0 : count <= 2 ? 1 : count <= 8 ? 2 : count <= 20 ? 3 : 4;
}

/// One dashboard's agent_activity block, or several projects added together.
internal sealed record ActivitySummary(IReadOnlyList<(DateOnly Date, int Count)> Days, long Total, int ActiveDays, long Undated, bool Partial, int Projects, int FailedProjects);

internal static class ActivitySummaries
{
    /// The agent_activity block of a dashboard.get answer; null when the
    /// server does not report activity by original agent date.
    internal static ActivitySummary? Parse(System.Text.Json.JsonElement? response)
    {
        if (response is not { ValueKind: System.Text.Json.JsonValueKind.Object } data ||
            !data.TryGetProperty("agent_activity", out var activity) ||
            NxJson.Text(activity, "timestamp_basis") != "agent_source_time")
            return null;
        var days = new List<(DateOnly, int)>();
        if (activity.TryGetProperty("days", out var values) && values.ValueKind == System.Text.Json.JsonValueKind.Array)
            foreach (var value in values.EnumerateArray())
                if (DateOnly.TryParseExact(NxJson.Text(value, "date"), "yyyy-MM-dd", out var date) && int.TryParse(NxJson.Text(value, "count"), out var count))
                    days.Add((date, count));
        long.TryParse(NxJson.Text(activity, "total"), out var total);
        int.TryParse(NxJson.Text(activity, "active_days"), out var active);
        long.TryParse(NxJson.Text(activity, "undated_messages"), out var undated);
        return new ActivitySummary(days, total, active, undated, NxJson.Text(activity, "partial") == "true", 1, 0);
    }

    /// Activity across projects: message counts per day are added (each
    /// project counts only its own messages), active days are the days with
    /// any message in any project.
    internal static ActivitySummary Merge(IEnumerable<ActivitySummary?> parts)
    {
        var byDay = new Dictionary<DateOnly, int>();
        long total = 0, undated = 0;
        bool partial = false;
        int projects = 0, failed = 0;
        foreach (var part in parts)
        {
            if (part is null) { failed++; continue; }
            projects += part.Projects;
            failed += part.FailedProjects;
            total += part.Total;
            undated += part.Undated;
            partial |= part.Partial;
            // A dashboard lists every day once; guard against repeats anyway.
            foreach (var group in part.Days.GroupBy(d => d.Date))
                byDay[group.Key] = byDay.GetValueOrDefault(group.Key) + Math.Max(0, group.Max(d => d.Count));
        }
        var days = byDay.OrderBy(d => d.Key).Select(d => (d.Key, d.Value)).ToList();
        return new ActivitySummary(days, total, days.Count(d => d.Value > 0), undated, partial || failed > 0, projects, failed);
    }
}
