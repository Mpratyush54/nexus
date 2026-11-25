using Nexus.Core;
using Xunit;

// One slow or failed call must never put the app in its offline state: only
// the reachability probe (Connectivity.Offline) decides that. A call that
// times out while the server is up makes its own card wait and retry.
public class SlowCallTests
{
    // The v0.4.2 report: Home's dashboard.get timed out on a big project while
    // the chat list and the server were fine.
    private const string DashboardTimeout =
        "Get \"https://api-nexus.example/projects/6f1c/dashboard?utc_offset_minutes=330\": context deadline exceeded (Client.Timeout exceeded while awaiting headers)";

    [Theory]
    [InlineData(DashboardTimeout)]
    [InlineData("Get \"https://api-nexus.example/projects/6f1c/dashboard\": unexpected EOF")]
    [InlineData("read tcp 10.0.0.2:51234->1.2.3.4:443: i/o timeout")]
    [InlineData("Get \"https://api-nexus.example/v1/memory\": 504 Gateway Timeout")]
    public void ATimedOutCallIsSlowNotOffline(string raw)
    {
        Assert.True(NxErrors.IsSlow(raw));
        Assert.Equal(CallTrouble.Slow, NxErrors.Classify(raw, offline: false));
        // Only when the probe has also failed is it the outage.
        Assert.Equal(CallTrouble.Offline, NxErrors.Classify(raw, offline: true));
    }

    [Theory]
    [InlineData("Get \"http://127.0.0.1:47921/v1/timeline\": dial tcp 127.0.0.1:47921: connectex: No connection could be made because the target machine actively refused it.")]
    [InlineData("dial tcp: lookup api.example: no such host")]
    public void FailingToConnectIsNotSlowButStillWaitsForTheProbe(string raw)
    {
        Assert.False(NxErrors.IsSlow(raw));
        Assert.True(NxErrors.IsUnreachable(raw));
        // The probe checks right away; until it fails, the card just waits.
        Assert.Equal(CallTrouble.Slow, NxErrors.Classify(raw, offline: false));
        Assert.Equal(CallTrouble.Offline, NxErrors.Classify(raw, offline: true));
    }

    [Fact]
    public void ShownMessagesKeepTheirMeaning()
    {
        // What NxClient shows for a slow call, with its reference id.
        var shown = NxErrors.SlowMessage + " (ref ABCD2345)";
        Assert.True(NxErrors.IsSlow(shown));
        Assert.Equal(CallTrouble.Slow, NxErrors.Classify(shown, offline: false));
        Assert.Equal(CallTrouble.Slow, NxErrors.Classify(NxErrors.UnreachableMessage + " (ref ABCD2345)", offline: false));
        Assert.Equal(CallTrouble.SignedOut, NxErrors.Classify("401 Unauthorized: token expired", offline: true));
        Assert.Equal(CallTrouble.Failed, NxErrors.Classify("500 Internal Server Error: could not load events: boom", offline: false));
        Assert.Equal(CallTrouble.None, NxErrors.Classify("", offline: true));
        // The slow sentence is plain language, with no transport jargon.
        Assert.False(NxErrors.UsesJargon(NxErrors.SlowMessage));
        Assert.DoesNotContain("deadline", NxErrors.Friendly(DashboardTimeout).Message);
    }

    [Fact]
    public void ASlowCardRetriesAFewTimesThenWaitsForTheButton()
    {
        var delays = Enumerable.Range(0, 10).Select(CardRetry.NextDelay).TakeWhile(d => d is not null).Select(d => d!.Value).ToList();
        Assert.InRange(delays.Count, 2, 6);
        Assert.True(delays.SequenceEqual(delays.OrderBy(d => d)), "retries back off");
        Assert.True(delays[0] >= TimeSpan.FromSeconds(2), "no retry storm against a slow server");
        Assert.Null(CardRetry.NextDelay(-1));
    }

    private static System.Text.Json.JsonElement Json(string json) => System.Text.Json.JsonDocument.Parse(json).RootElement.Clone();

    [Fact]
    public void ADashboardWithAMissingSectionStillShowsTheRest()
    {
        var answer = Json("""{"partial":true,"unavailable":["memories"],"metrics":{"members":2,"online":1},"agent_activity":{"timestamp_basis":"agent_source_time","total":4,"active_days":1,"days":[{"date":"2026-11-24","count":4}]}}""");
        Assert.True(ActivitySummaries.Unavailable(answer, "memories"));
        Assert.False(ActivitySummaries.Unavailable(answer, "agent_activity"));
        Assert.Equal(4, ActivitySummaries.Parse(answer)!.Total);
        // Older servers send neither field.
        Assert.False(ActivitySummaries.Unavailable(Json("""{"metrics":{}}"""), "agent_activity"));
        Assert.False(ActivitySummaries.Unavailable(null, "agent_activity"));
    }

    [Fact]
    public void ArchivedHistoryStillBeingReadIsPendingNotFinal()
    {
        var pending = ActivitySummaries.Parse(Json("""{"agent_activity":{"timestamp_basis":"agent_source_time","total":3,"active_days":1,"partial":true,"archive_pending":true,"days":[{"date":"2026-11-24","count":3}]}}"""))!;
        var done = ActivitySummaries.Parse(Json("""{"agent_activity":{"timestamp_basis":"agent_source_time","total":5,"active_days":1,"days":[{"date":"2026-11-24","count":5}]}}"""))!;
        Assert.True(pending.Pending);
        Assert.False(done.Pending);
        Assert.True(ActivitySummaries.Merge([pending, done]).Pending);
        Assert.False(ActivitySummaries.Merge([done]).Pending);
    }
}
