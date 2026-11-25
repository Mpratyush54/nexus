#nullable enable
using System.Text.Json;
using Nexus.Core.Diagnostics;

namespace Nexus.Core;

/// <summary>
/// Whether the Nexus server can be reached, decided from real traffic: a
/// call that fails to connect triggers a quick /healthz probe (net.probe),
/// and a probe also runs every few seconds as a heartbeat. While the server
/// is unreachable, probes back off (2 s, 4 s, 8 s … 30 s) and the first one
/// that answers ends the outage, so the app recovers by itself. The OS
/// network state (net.status) is not enough: it says "online" while the
/// server is down.
/// </summary>
internal static class Connectivity
{
    private static readonly object Gate = new();
    private static readonly SemaphoreSlim Wake = new(0, 1);
    private static readonly TimeSpan Heartbeat = TimeSpan.FromSeconds(10);
    private static readonly TimeSpan[] Backoff = [TimeSpan.FromSeconds(2), TimeSpan.FromSeconds(4), TimeSpan.FromSeconds(8), TimeSpan.FromSeconds(16), TimeSpan.FromSeconds(30)];
    private static int _started;
    private static int _failedProbes;

    /// <summary>Raised (on a pool thread) when the server becomes unreachable or reachable again.</summary>
    public static event Action? Changed;

    public static bool Offline { get; private set; }

    /// <summary>Reference id of the current outage: logged once with the raw error, quoted in messages.</summary>
    public static string OutageRef { get; private set; } = "";

    /// <summary>Server host the probe talks to ("" until known).</summary>
    public static string Host { get; private set; } = "";

    /// <summary>When the next automatic attempt runs while offline.</summary>
    public static DateTime NextAttemptUtc { get; private set; }

    public static void Start()
    {
        if (Interlocked.Exchange(ref _started, 1) == 1) return;
        _ = Task.Run(LoopAsync);
    }

    /// <summary>"Try now": probes at once instead of waiting for the backoff.</summary>
    public static void ProbeNow()
    {
        try { Wake.Release(); } catch (SemaphoreFullException) { }
    }

    /// <summary>A call could not reach the server. Returns the reference id the failure is logged under.</summary>
    internal static string NoteUnreachable(string method, string raw)
    {
        lock (Gate)
        {
            if (Offline && OutageRef.Length > 0) return OutageRef;
        }
        var reference = NxErrors.NewRef();
        DiagLog.Warn("net", $"{method} could not reach the server (ref {reference}): {raw}");
        ProbeNow();
        return reference;
    }

    /// <summary>A call that needs the server succeeded.</summary>
    internal static void NoteReachable()
    {
        if (Offline) ProbeNow();
    }

    // Calls whose success proves the server answered (they always go to it).
    private static readonly HashSet<string> ServerMethods = new(StringComparer.Ordinal)
    {
        NxMethods.TimelineList, NxMethods.SessionsGet, NxMethods.SessionsTurns, NxMethods.DashboardGet, NxMethods.ProjectsList,
        NxMethods.MemorySearch, NxMethods.TeleportInbox, NxMethods.OrgsList, NxMethods.OrgsGet, NxMethods.UsageSummary,
        NxMethods.ProjectsBrief, NxMethods.ProjectsGraph, NxMethods.AuthTokensList
    };

    internal static bool IsServerMethod(string method) => ServerMethods.Contains(method);

    private static async Task LoopAsync()
    {
        while (true)
        {
            var wait = Offline ? Backoff[Math.Clamp(_failedProbes - 1, 0, Backoff.Length - 1)] : Heartbeat;
            NextAttemptUtc = DateTime.UtcNow + wait;
            try { await Wake.WaitAsync(wait).ConfigureAwait(false); } catch { }
            try { Probe(); }
            catch (Exception ex) { DiagLog.Warn("net", "reachability probe failed", ex); }
        }
    }

    private static void Probe()
    {
        var (ok, result) = NxClient.CallDirect(NxMethods.NetProbe, "{}");
        if (!ok || result is not { ValueKind: JsonValueKind.Object } body || NxJson.Text(body, "configured") != "true")
        {
            // Not signed in to a server (or an engine without net.probe):
            // nothing to be offline from.
            SetOffline(false, "");
            return;
        }
        Host = NxJson.Text(body, "host");
        if (NxJson.Text(body, "reachable") == "true")
        {
            _failedProbes = 0;
            SetOffline(false, "");
            return;
        }
        _failedProbes++;
        SetOffline(true, NxJson.Text(body, "detail"));
    }

    private static void SetOffline(bool offline, string detail)
    {
        lock (Gate)
        {
            if (Offline == offline) return;
            Offline = offline;
            if (offline)
            {
                OutageRef = NxErrors.NewRef();
                DiagLog.Warn("net", $"server unreachable (ref {OutageRef}) at {Host}: {detail}");
            }
            else
            {
                DiagLog.Info("net", $"server reachable again (outage ref {OutageRef})");
                _failedProbes = 0;
            }
        }
        Changed?.Invoke();
    }
}
