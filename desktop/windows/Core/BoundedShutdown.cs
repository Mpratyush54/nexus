namespace Nexus.Core;

/// <summary>A stuck native worker must never prevent an explicit Quit.</summary>
internal sealed class BoundedShutdown
{
    private int _started;
    public async Task RunAsync(Func<Task> stop, Action exit, TimeSpan grace)
    {
        if (Interlocked.Exchange(ref _started, 1) != 0) return;
        try
        {
            var work = Task.Run(stop);
            _ = work.ContinueWith(task => { _ = task.Exception; }, TaskContinuationOptions.OnlyOnFaulted);
            var finished = await Task.WhenAny(work, Task.Delay(grace)).ConfigureAwait(false);
            if (finished == work) await work.ConfigureAwait(false);
        }
        catch { /* Shutdown is best-effort; process exit below is authoritative. */ }
        finally { exit(); }
    }
}
