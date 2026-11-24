using Xunit;

namespace Nexus.Core.Tests;

public class BoundedShutdownTests
{
    [Fact]
    public async Task StopsCoreBeforeExitingAndIgnoresRepeatedQuit()
    {
        var events = new List<string>();
        var shutdown = new BoundedShutdown();
        await shutdown.RunAsync(() => { events.Add("stop"); return Task.CompletedTask; }, () => events.Add("exit"), TimeSpan.FromSeconds(1));
        await shutdown.RunAsync(() => throw new Exception("must not run twice"), () => events.Add("again"), TimeSpan.Zero);
        Assert.Equal(new[] { "stop", "exit" }, events);
    }

    [Fact]
    public async Task NativeFailureCannotSwallowQuit()
    {
        var exited = false;
        await new BoundedShutdown().RunAsync(() => throw new InvalidOperationException("native failure"), () => exited = true, TimeSpan.FromSeconds(1));
        Assert.True(exited);
    }

    [Fact]
    public async Task HungNativeShutdownCannotKeepAppAlive()
    {
        var never = new TaskCompletionSource();
        var exited = false;
        await new BoundedShutdown().RunAsync(() => never.Task, () => exited = true, TimeSpan.FromMilliseconds(25)).WaitAsync(TimeSpan.FromSeconds(2));
        Assert.True(exited);
    }
}
