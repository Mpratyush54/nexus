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
