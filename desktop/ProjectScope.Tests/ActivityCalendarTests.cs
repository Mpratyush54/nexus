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

    private static System.Text.Json.JsonElement Dashboard(string json) => System.Text.Json.JsonDocument.Parse(json).RootElement.Clone();

    [Fact]
    public void AllProjectsActivityAddsEveryProject()
    {
        // The real failure: the shell project (restored from the last
        // session) had only July activity while the work happened in two
        // other projects. "All projects" must show all three.
        var old = ActivitySummaries.Parse(Dashboard("""{"agent_activity":{"timestamp_basis":"agent_source_time","total":209,"active_days":1,"undated_messages":3505,"days":[{"date":"2026-07-03","count":209},{"date":"2026-11-24","count":0}]}}"""));
        var main = ActivitySummaries.Parse(Dashboard("""{"agent_activity":{"timestamp_basis":"agent_source_time","total":990,"active_days":2,"undated_messages":5049,"days":[{"date":"2026-11-23","count":483},{"date":"2026-11-24","count":507}]}}"""));
        var lab = ActivitySummaries.Parse(Dashboard("""{"agent_activity":{"timestamp_basis":"agent_source_time","total":2,"active_days":1,"partial":false,"days":[{"date":"2026-11-23","count":2}]}}"""));
        Assert.NotNull(old);
        var merged = ActivitySummaries.Merge([old, main, lab]);
        Assert.Equal(1201, merged.Total);
        Assert.Equal(3, merged.ActiveDays);
        Assert.Equal(3, merged.Projects);
        Assert.Equal(8554, merged.Undated);
        Assert.Equal(485, merged.Days.Single(d => d.Date == new DateOnly(2026, 11, 23)).Count);
        Assert.False(merged.Partial);
        var cells = ActivityCalendar.Build(new DateOnly(2026, 11, 25), merged.Days);
        Assert.Equal(1201, cells.Sum(c => c.Count));
    }

    [Fact]
    public void FailedProjectMarksMergedActivityIncomplete()
    {
        var main = ActivitySummaries.Parse(Dashboard("""{"agent_activity":{"timestamp_basis":"agent_source_time","total":5,"active_days":1,"days":[{"date":"2026-11-23","count":5}]}}"""));
        var merged = ActivitySummaries.Merge([main, null]);
        Assert.Equal(1, merged.FailedProjects);
        Assert.True(merged.Partial);
        Assert.Equal(5, merged.Total);
    }

    [Fact]
    public void OldServerWithoutSourceTimeActivityIsNotParsed()
    {
        Assert.Null(ActivitySummaries.Parse(Dashboard("""{"heatmap":[{"date":"2026-11-23","count":5}]}""")));
        Assert.Null(ActivitySummaries.Parse(null));
    }

    [Theory]
    [InlineData("Missing or invalid bearer token", true)]
    [InlineData("401 Unauthorized", true)]
    [InlineData("Nexus is offline; memory unavailable", true)]
    [InlineData("Not signed in to Nexus on this computer", true)]
    [InlineData("dial tcp 127.0.0.1:443: connectex: refused", false)]
    [InlineData("", false)]
    [InlineData(null, false)]
    public void SignedOutErrorsAreRecognised(string? error, bool signedOut) => Assert.Equal(signedOut, NxErrors.IsSignedOut(error));
}
