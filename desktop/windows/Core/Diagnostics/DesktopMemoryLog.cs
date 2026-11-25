#nullable enable
using System.Diagnostics;
using System.Text.Json;
using System.Threading;
using Microsoft.UI.Dispatching;

namespace Nexus.Core.Diagnostics;

/// <summary>
/// Counts of the desktop's big UI caches, kept by their owners on the UI
/// thread and read by <see cref="DesktopMemoryLog"/>. Sizes and counts only.
/// </summary>
internal static class UiStats
{
    /// <summary>Message bodies rendered as rich text since start (cumulative).</summary>
    public static long RenderedMessages;
    /// <summary>Graph node visuals currently built.</summary>
    public static int GraphNodes;
    /// <summary>Graph sprite textures currently held, and their total bytes.</summary>
    public static int GraphTextures;
    public static long GraphTextureBytes;
    /// <summary>Sidebar: chat entries and built row controls (set by MainWindow).</summary>
    public static Func<(int Entries, int Rows)>? Sidebar;
}

/// <summary>
/// Once a minute, one line in the desktop log that attributes the process's
/// memory: the .NET GC heap, process private bytes and working set, the Go
/// core's total (from diagnostics.get), and the big UI caches' counts.
/// </summary>
internal static class DesktopMemoryLog
{
    private static DispatcherQueueTimer? _timer;
    private static int _busy;

    public static void Start(DispatcherQueue queue)
    {
        if (_timer is not null) return;
        _timer = queue.CreateTimer();
        _timer.Interval = TimeSpan.FromSeconds(60);
        _timer.Tick += (_, _) => _ = TickAsync();
        _timer.Start();
    }

    private static async Task TickAsync()
    {
        if (Interlocked.Exchange(ref _busy, 1) == 1) return;
        try
        {
            // UI-owned counts are read here, on the UI thread.
            var pages = Pages.PageCache.CachedSessionPages;
            var chatElements = Pages.PageCache.CachedChatElements;
            var sidebar = UiStats.Sidebar?.Invoke() ?? (0, 0);
            var goTotal = await GoTotalMiB();
            var gc = GC.GetGCMemoryInfo();
            using var proc = Process.GetCurrentProcess();
            proc.Refresh();
            DiagLog.Info("mem",
                $"desktop: private={Mi(proc.PrivateMemorySize64)} working_set={Mi(proc.WorkingSet64)} " +
                $"gc_heap={Mi(gc.HeapSizeBytes)} gc_committed={Mi(gc.TotalCommittedBytes)} gc_fragmented={Mi(gc.FragmentedBytes)} " +
                $"gc_total_memory={Mi(GC.GetTotalMemory(false))} go_total={goTotal} " +
                $"session_pages={pages} chat_elements={chatElements} sidebar_entries={sidebar.Entries} sidebar_rows={sidebar.Rows} " +
                $"graph_nodes={Volatile.Read(ref UiStats.GraphNodes)} graph_textures={Volatile.Read(ref UiStats.GraphTextures)} " +
                $"graph_texture_bytes={Mi(Interlocked.Read(ref UiStats.GraphTextureBytes))} rendered_messages={Interlocked.Read(ref UiStats.RenderedMessages)}");
        }
        catch (Exception ex)
        {
            DiagLog.Warn("mem", "memory line failed", ex);
        }
        finally
        {
            Volatile.Write(ref _busy, 0);
        }
    }

    private static async Task<string> GoTotalMiB()
    {
        try
        {
            var response = await App.Core.CallAsync(NxMethods.DiagnosticsGet);
            if (response.Ok && response.Result is { ValueKind: JsonValueKind.Object } result &&
                result.TryGetProperty("memory", out var mem) &&
                mem.TryGetProperty("go_total", out var total) && total.TryGetInt64(out var bytes))
                return Mi(bytes);
        }
        catch (Exception) { }
        return "?";
    }

    private static string Mi(long bytes) => (bytes / 1048576.0).ToString("0.0", System.Globalization.CultureInfo.InvariantCulture) + "MiB";
}
