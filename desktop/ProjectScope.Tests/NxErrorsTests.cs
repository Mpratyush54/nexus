using Nexus.Core;
using Xunit;

public class NxErrorsTests
{
    [Theory]
    [InlineData("Get \"http://127.0.0.1:47921/v1/timeline\": dial tcp 127.0.0.1:47921: connectex: No connection could be made because the target machine actively refused it.")]
    [InlineData("Post \"https://api.example/v1/x\": context deadline exceeded (Client.Timeout exceeded while awaiting headers)")]
    [InlineData("dial tcp: lookup api.example: no such host")]
    public void UnreachableErrorsBecomeOnePlainSentence(string raw)
    {
        Assert.True(NxErrors.IsUnreachable(raw));
        var (message, reference) = NxErrors.Friendly(raw);
        Assert.StartsWith(NxErrors.UnreachableMessage, message);
        Assert.DoesNotContain("tcp", message);
        Assert.DoesNotContain("http", message);
        Assert.Equal(8, reference.Length);
        Assert.Contains("(ref " + reference + ")", message);
    }

    [Fact]
    public void TechnicalTextIsHiddenBehindARef()
    {
        var (message, reference) = NxErrors.Friendly("json: cannot unmarshal string into Go value of type int");
        Assert.StartsWith("Something went wrong.", message);
        Assert.NotEmpty(reference);
    }

    [Fact]
    public void ServerErrorsSayTheServerHadAProblem()
    {
        var (message, _) = NxErrors.Friendly("500 Internal Server Error");
        Assert.StartsWith("The Nexus server ran into a problem", message);
    }

    [Fact]
    public void UnsupportedRoutesSaySo()
    {
        Assert.StartsWith(NxErrors.UnsupportedMessage, NxErrors.Friendly("404 page not found").Message);
        Assert.StartsWith(NxErrors.UnsupportedMessage, NxErrors.Friendly("unknown nx_call method").Message);
    }

    [Fact]
    public void PlainSentencesPassThroughAndKeepTheServerRef()
    {
        Assert.Equal("Plan limit reached: 3 projects (ref ABC123XYZ)", NxErrors.Friendly("Plan limit reached: 3 projects (ref ABC123XYZ)").Message);
        Assert.Equal("select a local workspace before sending", NxErrors.Friendly("select a local workspace before sending").Message);
    }

    [Fact]
    public void SignedOutWinsAndIsRecognisedAfterMapping()
    {
        var (message, reference) = NxErrors.Friendly("401 Unauthorized: invalid bearer token");
        Assert.Equal(NxErrors.SignedOutMessage, message);
        Assert.Equal("", reference);
        Assert.True(NxErrors.IsSignedOut(message));
    }

    [Theory]
    [InlineData("Not signed in to Nexus on this computer")]
    [InlineData("Nexus is offline; memory unavailable")]
    public void NoSignInIsNotAnOutage(string engine)
    {
        // No saved sign-in must ask the person to sign in, never wait for a
        // connection that is fine.
        Assert.False(NxErrors.IsUnreachable(engine));
        Assert.Equal(NxErrors.SignedOutMessage, NxErrors.Friendly(engine).Message);
    }

    [Fact]
    public void EngineTransportTextIsNotShown()
    {
        Assert.DoesNotContain("core response", NxErrors.Friendly("empty core response").Message);
        Assert.DoesNotContain("adapter", NxErrors.Friendly("This harness has no supported account-wide model-list adapter here").Message);
    }
}

public class FriendlyTimeTests
{
    private static readonly DateTimeOffset Now = new(2026, 11, 25, 15, 0, 0, TimeSpan.Zero);

    [Fact]
    public void CountsArePlural()
    {
        Assert.Equal("1 device", FriendlyTime.Count(1, "device"));
        Assert.Equal("2 devices", FriendlyTime.Count(2, "device"));
        Assert.Equal("3 memories", FriendlyTime.Count(3, "memory", "memories"));
    }

    [Fact]
    public void RelativeTimesAreWords()
    {
        Assert.Equal("just now", FriendlyTime.Relative(Now.AddSeconds(-20), Now));
        Assert.Equal("1 minute ago", FriendlyTime.Relative(Now.AddMinutes(-1), Now));
        Assert.Equal("5 minutes ago", FriendlyTime.Relative(Now.AddMinutes(-5), Now));
        Assert.DoesNotContain(" d ago", FriendlyTime.Relative(Now.AddDays(-3), Now));
        Assert.EndsWith("days ago", FriendlyTime.Relative(Now.AddDays(-3), Now));
    }
}
