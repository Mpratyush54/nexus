using Nexus.Core;
using Xunit;

public class ActivityCalendarTests
{
    [Fact]
    public void CalendarHasExactlyOneYearAndSundayAlignedWeeks()
    {
        var today = new DateOnly(2026, 10, 22);
        var cells = ActivityCalendar.Build(today, []);
        Assert.Equal(DayOfWeek.Sunday, cells[0].Date.DayOfWeek);
        Assert.Equal(0, cells.Count % 7);
        Assert.Equal(365, cells.Count(c => c.InRange));
        Assert.All(cells, c => Assert.Equal(0, c.Count));
    }

    [Fact]
    public void DuplicateBackfillsDoNotInflateDatesOrPaintFutureCells()
    {
        var today = new DateOnly(2024, 3, 1);
        var cells = ActivityCalendar.Build(today, [(today.AddDays(-1), 5), (today.AddDays(-1), 5), (today.AddDays(1), 90)]);
        Assert.Equal(5, cells.Sum(c => c.Count));
        Assert.Equal(5, cells.Single(c => c.Date == new DateOnly(2024, 2, 29)).Count);
    }
}
