#nullable enable
using System.Text.Json;

namespace Nexus.Core;

/// <summary>
/// Keeps <see cref="AgentAvailability"/> current without ever blocking the UI:
/// the last answer is cached on disk and shown at once; harness.available
/// (PATH lookups and file checks only, no agent is started) runs on the
/// thread pool at launch and whenever the window regains focus.
/// </summary>
internal static class AgentAvailabilityService
{
    private static readonly object s_lock = new();
    private static IReadOnlyList<AgentState> s_states = LoadCache();
    private static DateTime s_checkedAt = DateTime.MinValue;
    private static int s_running;
    private static readonly TimeSpan MinInterval = TimeSpan.FromSeconds(5);

    /// Raised on a thread-pool thread after the list changed.
    public static event Action? Changed;

    public static IReadOnlyList<AgentState> Current { get { lock (s_lock) return s_states; } }

    public static AgentState? For(string agent)
    {
        var a = AgentAvailability.Normalize(agent);
        return Current.FirstOrDefault(s => s.Agent == a);
    }

    public static IReadOnlyList<(string Value, string Name)> PickerChoices() => AgentAvailability.PickerChoices(Current);

    /// Starts a background check unless one ran in the last few seconds.
    public static void Refresh(bool force = false)
    {
        if (!force && DateTime.UtcNow - s_checkedAt < MinInterval) return;
        if (Interlocked.Exchange(ref s_running, 1) == 1) return;
        _ = Task.Run(async () =>
        {
            try
            {
                var response = await App.Core.CallAsync(NxMethods.HarnessAvailable).ConfigureAwait(false);
                s_checkedAt = DateTime.UtcNow;
                if (!response.Ok) return;
                var states = AgentAvailability.Parse(response.Result);
                if (states.Count == 0) return;
                bool changed;
                lock (s_lock)
                {
                    changed = !states.SequenceEqual(s_states);
                    s_states = states;
                }
                if (!changed) return;
                SaveCache(response.Result);
                Changed?.Invoke();
            }
            catch (Exception ex) { Diagnostics.DiagLog.Warn("agents", "agent check failed", ex); }
            finally { Interlocked.Exchange(ref s_running, 0); }
        });
    }

    private static string CacheFile => Path.Combine(ProfilePaths.Folder(Environment.SpecialFolder.LocalApplicationData), "Nexus", "agents-available.json");

    private static IReadOnlyList<AgentState> LoadCache()
    {
        try
        {
            if (!File.Exists(CacheFile)) return Array.Empty<AgentState>();
            using var doc = JsonDocument.Parse(File.ReadAllText(CacheFile));
            return AgentAvailability.Parse(doc.RootElement.Clone());
        }
        catch { return Array.Empty<AgentState>(); }
    }

    private static void SaveCache(JsonElement? result)
    {
        if (result is not { } body) return;
        try
        {
            Directory.CreateDirectory(Path.GetDirectoryName(CacheFile)!);
            File.WriteAllText(CacheFile, body.GetRawText());
        }
        catch { /* the next launch just checks again */ }
    }
}
