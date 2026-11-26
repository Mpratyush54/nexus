using System.Text.Json;

namespace Nexus.Core;

/// <summary>
/// What every chat's agent is doing, app-wide. The core owns the running
/// turns (internal/agentrun); this mirrors its live.status events so the
/// sidebar, the tray, notifications and the quit prompt can show them
/// whichever page is open. No UI types: the shell subscribes to <see cref="Changed"/>.
/// </summary>
public sealed class LiveSessions
{
    public static LiveSessions Current { get; } = new();

    public sealed record ChatStatus(string SessionId, string Status, bool Unread, string Agent, string Title, int Pending, string ApprovalSummary, string Error, int Followups)
    {
        public bool Working => Status is "running" or "approval" or "queued";
        public bool NeedsApproval => Status == "approval";
    }

    /// <summary>What the sidebar shows for a chat.</summary>
    public enum Indicator { None, Running, Queued, Approval, Unread, Error, Interrupted }

    private readonly Dictionary<string, ChatStatus> _chats = new(StringComparer.OrdinalIgnoreCase);
    private readonly object _gate = new();

    /// <summary>Raised (on the event's thread) after a chat's status changed.</summary>
    public event Action<ChatStatus?, ChatStatus>? Changed;

    /// <summary>Permission mode for chats without their own: ask, edits or full (Settings).</summary>
    public string DefaultMode { get; set; } = "edits";

    public int Running { get; private set; }
    public int Queued { get; private set; }
    public int Approvals { get; private set; }

    /// <summary>Chats whose agent is working: the quit prompt counts these.</summary>
    public int Working => Running + Queued;

    public ChatStatus? Get(string sessionId)
    {
        lock (_gate) return _chats.GetValueOrDefault(sessionId);
    }

    public IReadOnlyList<ChatStatus> All()
    {
        lock (_gate) return _chats.Values.ToList();
    }

    public Indicator IndicatorFor(string sessionId) => Get(sessionId) is { } s ? IndicatorOf(s) : Indicator.None;

    public static Indicator IndicatorOf(ChatStatus s) => s.Status switch
    {
        "approval" => Indicator.Approval,
        "running" => Indicator.Running,
        "queued" => Indicator.Queued,
        "interrupted" => Indicator.Interrupted,
        "error" when s.Unread => Indicator.Error,
        "done" when s.Unread => Indicator.Unread,
        _ => Indicator.None
    };

    /// <summary>Applies a core event. Returns true when it was a live.status event.</summary>
    public bool Apply(string json)
    {
        if (!json.Contains("\"type\":\"live.status\"", StringComparison.Ordinal)) return false;
        try
        {
            using var doc = JsonDocument.Parse(json);
            var root = doc.RootElement;
            var next = Parse(root);
            if (next.SessionId.Length == 0) return true;
            ChatStatus? previous;
            lock (_gate)
            {
                previous = _chats.GetValueOrDefault(next.SessionId);
                _chats[next.SessionId] = next;
                Running = Int(root, "running");
                Queued = Int(root, "queued");
                Approvals = Int(root, "approval_count");
            }
            Changed?.Invoke(previous, next);
        }
        catch (JsonException) { }
        return true;
    }

    /// <summary>Replaces everything with a live.list result (at start and after a reconnect).</summary>
    public void Load(JsonElement list)
    {
        var changed = new List<(ChatStatus?, ChatStatus)>();
        lock (_gate)
        {
            var fresh = new Dictionary<string, ChatStatus>(StringComparer.OrdinalIgnoreCase);
            if (list.ValueKind == JsonValueKind.Object && list.TryGetProperty("sessions", out var sessions) && sessions.ValueKind == JsonValueKind.Array)
                foreach (var item in sessions.EnumerateArray())
                {
                    var s = Parse(item);
                    if (s.SessionId.Length > 0) fresh[s.SessionId] = s;
                }
            foreach (var (id, s) in fresh)
                if (!_chats.TryGetValue(id, out var old) || old != s) changed.Add((old, s));
            foreach (var (id, old) in _chats)
                if (!fresh.ContainsKey(id)) changed.Add((old, old with { Status = "idle", Unread = false }));
            _chats.Clear();
            foreach (var (id, s) in fresh) _chats[id] = s;
            Running = Int(list, "running");
            Queued = Int(list, "queued");
            Approvals = Int(list, "approval");
        }
        foreach (var (old, s) in changed) Changed?.Invoke(old, s);
    }

    private static ChatStatus Parse(JsonElement e) => new(
        NxJson.Text(e, "session_id"), NxJson.Text(e, "status"),
        e.TryGetProperty("unread", out var u) && u.ValueKind == JsonValueKind.True,
        NxJson.Text(e, "agent"), NxJson.Text(e, "title"), Int(e, "pending"),
        NxJson.Text(e, "approval_summary"), NxJson.Text(e, "error"), Int(e, "followups"));

    private static int Int(JsonElement e, string name) =>
        e.ValueKind == JsonValueKind.Object && e.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Number && v.TryGetInt32(out var n) ? n : 0;

    /// <summary>One line for the tray tooltip and menu: "2 agents working · 1 needs you".</summary>
    public string Summary()
    {
        if (Working == 0 && Approvals == 0) return "";
        var parts = new List<string>();
        if (Running > 0) parts.Add(Running == 1 ? "1 agent working" : Running + " agents working");
        if (Queued > 0) parts.Add(Queued + " waiting");
        if (Approvals > 0) parts.Add(Approvals == 1 ? "1 needs you" : Approvals + " need you");
        return string.Join(" · ", parts);
    }

    /// <summary>
    /// The desktop notification a status change deserves, or null: a chat
    /// that finished, failed or needs approval while the window isn't focused.
    /// </summary>
    public static (string Title, string Body)? NotificationFor(ChatStatus? previous, ChatStatus next, bool windowFocused, string preference)
    {
        if (preference == "off") return null;
        // In the window, the sidebar dot and the open chat say it already.
        if (windowFocused) return null;
        var agent = AgentName(next.Agent);
        var chat = next.Title.Length > 0 ? next.Title : "a chat";
        if (next.NeedsApproval && previous?.Status != "approval")
            return (agent + " needs your approval", (next.ApprovalSummary.Length > 0 ? next.ApprovalSummary : "An action is waiting") + " · " + chat);
        if (preference == "approvals") return null;
        var wasWorking = previous is { Working: true };
        if (wasWorking && next.Status == "done")
            return (agent + " finished", chat);
        if (wasWorking && next.Status == "error")
            return (agent + " stopped", (next.Error.Length > 0 ? Clip(next.Error, 90) + " · " : "") + chat);
        return null;
    }

    private static string Clip(string s, int max) => s.Length <= max ? s : s[..(max - 1)] + "…";

    private static string AgentName(string agent) => agent switch
    {
        "claude" => "Claude Code",
        "codex" => "Codex",
        "cursor" => "Cursor",
        "opencode" => "OpenCode",
        "gemini" => "Gemini",
        "kimi" => "Kimi",
        "" => "The agent",
        _ => agent
    };

    /// <summary>The quit prompt's question: "2 agents are still working".</summary>
    public static string QuitQuestion(int working) =>
        working == 1 ? "An agent is still working" : working + " agents are still working";
}
